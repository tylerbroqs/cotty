package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// message is one turn of a conversation with the model.
type message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// provider is a chat model behind an HTTP API.
type provider interface {
	complete(ctx context.Context, system string, msgs []message) (string, error)
	describe() string
}

// ProviderOptions selects and configures the model an agent uses.
type ProviderOptions struct {
	// Provider is "anthropic" (the Messages API) or "openai" (any
	// endpoint speaking the OpenAI chat-completions format: OpenAI,
	// OpenRouter, Groq, Ollama, LM Studio, vLLM, ...).
	Provider string
	// Model overrides the provider's default model. Required for
	// "openai", which has no sensible default across endpoints.
	Model string
	// BaseURL overrides the provider's API endpoint.
	BaseURL string
	// KeyEnv names the environment variable holding the API key
	// (default ANTHROPIC_API_KEY or OPENAI_API_KEY).
	KeyEnv string
}

const (
	defaultAnthropicModel = "claude-opus-5-5"
	anthropicBaseURL      = "https://api.anthropic.com"
	openAIBaseURL         = "https://api.openai.com/v1"
	requestTimeout        = 3 * time.Minute
	maxOutputTokens       = 16000
)

func newProvider(o ProviderOptions) (provider, error) {
	switch strings.ToLower(o.Provider) {
	case "", "anthropic", "claude":
		env := o.KeyEnv
		if env == "" {
			env = "ANTHROPIC_API_KEY"
		}
		key := os.Getenv(env)
		if key == "" {
			return nil, fmt.Errorf("set %s to your Anthropic API key (or pick another -provider)", env)
		}
		model := o.Model
		if model == "" {
			model = defaultAnthropicModel
		}
		base := strings.TrimSuffix(o.BaseURL, "/")
		if base == "" {
			base = anthropicBaseURL
		}
		return &anthropic{key: key, model: model, base: base}, nil

	case "openai", "openai-compatible":
		env := o.KeyEnv
		if env == "" {
			env = "OPENAI_API_KEY"
		}
		base := strings.TrimSuffix(o.BaseURL, "/")
		if base == "" {
			base = openAIBaseURL
		}
		key := os.Getenv(env)
		if key == "" && base == openAIBaseURL {
			return nil, fmt.Errorf("set %s to your OpenAI API key, or point -base-url at another endpoint", env)
		}
		if o.Model == "" {
			return nil, errors.New("-model is required with -provider openai (e.g. -model gpt-5 or a model your endpoint serves)")
		}
		return &openAI{key: key, model: o.Model, base: base}, nil

	default:
		return nil, fmt.Errorf("unknown provider %q (use anthropic or openai)", o.Provider)
	}
}

// postJSON sends body to url and decodes a JSON reply into out, turning
// non-2xx replies into errors that carry the API's own message.
func postJSON(ctx context.Context, url string, headers map[string]string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("%s: %s", resp.Status, apiErr.Error.Message)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// anthropic talks to the Claude Messages API.
type anthropic struct {
	key, model, base string
}

func (a *anthropic) describe() string { return "anthropic " + a.model }

func (a *anthropic) complete(ctx context.Context, system string, msgs []message) (string, error) {
	body := map[string]any{
		"model":      a.model,
		"max_tokens": maxOutputTokens,
		"system":     system,
		"messages":   msgs,
	}
	headers := map[string]string{
		"x-api-key":         a.key,
		"anthropic-version": "2023-06-01",
	}
	// On Anthropic's own API, a request the model's safety classifiers
	// decline is retried server-side on the recommended fallback model.
	if a.base == anthropicBaseURL {
		body["fallbacks"] = "default"
		headers["anthropic-beta"] = "server-side-fallback-2026-07-01"
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := postJSON(ctx, a.base+"/v1/messages", headers, body, &resp); err != nil {
		return "", err
	}
	if resp.StopReason == "refusal" {
		return "", errors.New("the model declined to answer")
	}
	var b strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
}

// openAI talks to any OpenAI-compatible chat-completions endpoint.
type openAI struct {
	key, model, base string
}

func (o *openAI) describe() string { return "openai-compatible " + o.model + " at " + o.base }

func (o *openAI) complete(ctx context.Context, system string, msgs []message) (string, error) {
	all := append([]message{{Role: "system", Content: system}}, msgs...)
	headers := map[string]string{}
	if o.key != "" {
		headers["Authorization"] = "Bearer " + o.key
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	body := map[string]any{"model": o.model, "messages": all}
	if err := postJSON(ctx, o.base+"/chat/completions", headers, body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("the model returned no answer")
	}
	if m := resp.Choices[0].Message; m.Content == "" && m.Refusal != "" {
		return "", errors.New("the model declined to answer: " + m.Refusal)
	}
	return resp.Choices[0].Message.Content, nil
}
