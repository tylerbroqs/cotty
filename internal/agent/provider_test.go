package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicRequestAndResponse(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("missing auth/version headers: %v", r.Header)
		}
		json.NewDecoder(r.Body).Decode(&got)
		// Thinking blocks come back empty and are skipped; only text is read.
		w.Write([]byte(`{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"{\"say\":\"hi\"}"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "k")
	p, err := newProvider(ProviderOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := p.complete(context.Background(), "sys", []message{{Role: "user", Content: "q"}})
	if err != nil || reply != `{"say":"hi"}` {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
	if got["model"] != defaultAnthropicModel || got["system"] != "sys" {
		t.Errorf("request body = %v", got)
	}
	// Server-side fallbacks are a Claude API feature; a custom endpoint
	// (proxy, gateway) doesn't get them.
	if _, ok := got["fallbacks"]; ok {
		t.Error("fallbacks sent to a non-Anthropic base URL")
	}
}

func TestAnthropicRefusalAndErrors(t *testing.T) {
	status, body := 200, `{"content":[],"stop_reason":"refusal"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	p := &anthropic{key: "k", model: "m", base: srv.URL}
	if _, err := p.complete(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Errorf("refusal: err = %v", err)
	}
	status, body = 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	if _, err := p.complete(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("API error: err = %v", err)
	}
}

func TestOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		// Local endpoints like Ollama need no key, so none is sent.
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header")
		}
		var req struct {
			Model    string    `json:"model"`
			Messages []message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "llama" || req.Messages[0].Role != "system" || req.Messages[1].Content != "q" {
			t.Errorf("request = %+v", req)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "")
	p, err := newProvider(ProviderOptions{Provider: "openai", Model: "llama", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := p.complete(context.Background(), "sys", []message{{Role: "user", Content: "q"}}); err != nil || reply != "ok" {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
	if _, err := newProvider(ProviderOptions{Provider: "openai", BaseURL: srv.URL}); err == nil {
		t.Error("openai without -model should fail")
	}
}

func TestOrbio(t *testing.T) {
	var model string
	var maxTokens float64
	status, body := 200, `{"choices":[{"message":{"content":"ok"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ok-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var req struct {
			Model     string  `json:"model"`
			MaxTokens float64 `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		model, maxTokens = req.Model, req.MaxTokens
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	t.Setenv("ORBIO_API_KEY", "")
	if _, err := newProvider(ProviderOptions{Provider: "orbio"}); err == nil || !strings.Contains(err.Error(), "ORBIO_API_KEY") {
		t.Errorf("no key: err = %v", err)
	}
	t.Setenv("ORBIO_API_KEY", "ok-key")
	p, err := newProvider(ProviderOptions{Provider: "orbio", BaseURL: srv.URL + "/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := p.complete(context.Background(), "sys", []message{{Role: "user", Content: "q"}}); err != nil || reply != "ok" {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
	if model != defaultOrbioModel {
		t.Errorf("model = %q, want %q", model, defaultOrbioModel)
	}
	// Orbio reserves the cost of the whole cap, so one is always sent.
	if maxTokens != maxOutputTokens {
		t.Errorf("max_tokens = %v, want %d", maxTokens, maxOutputTokens)
	}

	status, body = 403, `{"error":{"code":"private_encryption_required","type":"incognito_error","message":"encrypt first"}}`
	if _, err := p.complete(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "Incognito") {
		t.Errorf("incognito: err = %v", err)
	}
}
