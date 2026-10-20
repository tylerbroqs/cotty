// Command cotty is a multiplayer terminal: host your shell, let others
// join over a websocket, watch together, type together.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tylerbroqs/cotty/internal/agent"
	"github.com/tylerbroqs/cotty/internal/client"
	"github.com/tylerbroqs/cotty/internal/ctl"
	"github.com/tylerbroqs/cotty/internal/desk"
	"github.com/tylerbroqs/cotty/internal/host"
	"github.com/tylerbroqs/cotty/internal/relay"
	"github.com/tylerbroqs/cotty/internal/replay"
)

const version = "1.0.0-dev"

const usage = `cotty — the multiplayer terminal

Usage:
  cotty host [flags]           Share your shell with guests
  cotty join [flags] <url>     Join a hosted session
  cotty ctl [flags] <command>  Manage the guests of a running session
  cotty relay [flags]          Run a relay server for NAT-friendly sessions
  cotty replay [flags] <file>  Play back a recorded session
  cotty desk [flags]           Robinhood Chain market dashboard (read-only)
  cotty agent [flags] <url>    Bring an AI agent into a session as a guest
  cotty ask <question>         Ask the session's agent (run inside the session)
  cotty version                Print version

Host flags:
  -addr string    Listen address for guests (default ":7373")
  -relay string   Host through a relay instead of listening locally,
                  e.g. -relay relay.example.com:7374 (works behind NAT)
  -shell string   Shell to run (default $SHELL, then /bin/sh)
  -write          Let guests type by default (otherwise view-only until
                  granted with 'cotty ctl allow NAME')
  -code string    Use a fixed session code instead of a random one
  -plain          Disable end-to-end encryption for relayed sessions
                  (relayed sessions are encrypted by default; the join
                  URL's #k= part carries the key and never reaches the
                  relay)
  -record string  Record the session as an asciicast v2 file (playable
                  with 'cotty replay' or asciinema)
  -audit string   Write a JSON-lines audit trail: applied keystrokes by
                  participant, joins/leaves, permission changes, kicks

Replay flags:
  -speed float      Playback speed multiplier (default 1)
  -max-idle duration  Cap pauses between events, e.g. 2s (default 2s;
                      0 keeps original timing)

Agent flags (the agent joins view-only; 'cotty ctl allow agent' lets it
run commands, which everyone sees and the audit trail attributes to it):
  -name string      Display name (default "agent")
  -provider string  anthropic (default) or openai, which covers any
                    OpenAI-compatible endpoint (OpenRouter, Ollama, ...)
  -model string     Model (default claude-opus-5-5 for anthropic;
                    required for openai)
  -base-url string  API endpoint override, e.g. http://localhost:11434/v1
  -key-env string   Env var holding the API key (default ANTHROPIC_API_KEY
                    or OPENAI_API_KEY)
  -steps int        Max model calls per question (default 4)

Desk flags (run it inside 'cotty host' to watch it together):
  -symbols string     Comma-separated watch list, or "all" for every Stock
                      Token with a Chainlink feed
                      (default TSLA,NVDA,AAPL,MSTR,COIN,SPY,QQQ,ETH,BTC)
  -rpc string         Robinhood Chain RPC endpoint (default: the public
                      mainnet RPC, which is rate limited)
  -interval duration  Refresh interval (default 15s, minimum 5s)
  -alert float        Alert threshold in percent for quote/feed
                      disagreement and session gaps (default 1)
  -carry float        Alert when a Lighter perp's funding reaches this
                      annualized percent either way (default 10)
  -no-perps           Skip the Lighter perps basis section
  -once               Print one snapshot and exit

Join flags:
  -name string    Display name other participants see (default $USER)

Ctl commands (run on the host machine, e.g. from another terminal):
  cotty ctl list          Show connected guests and their permissions
  cotty ctl allow NAME    Let a guest type into the session
  cotty ctl deny NAME     Make a guest view-only again
  cotty ctl kick NAME     Disconnect a guest
  -code string            Target a specific session (default: $COTTY_SESSION,
                          then the only active session)

Relay flags:
  -addr string        Listen address (default ":7374")
  -public-url string  Base URL guests use to reach this relay,
                      e.g. wss://relay.example.com (default: request host)

Join:
  The host prints the URL to use, e.g.
    cotty join "ws://192.168.1.10:7373/ws?code=XJ4K2P"
  Press Ctrl-] to leave a session.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "host":
		printBanner(os.Stderr)
		fs := flag.NewFlagSet("host", flag.ExitOnError)
		addr := fs.String("addr", ":7373", "listen address for guests")
		relayAddr := fs.String("relay", "", "relay server to host through")
		shell := fs.String("shell", "", "shell to run (default $SHELL)")
		write := fs.Bool("write", false, "allow guests to type")
		code := fs.String("code", "", "fixed session code")
		plain := fs.Bool("plain", false, "disable end-to-end encryption for relayed sessions")
		recordPath := fs.String("record", "", "record the session as an asciicast v2 file")
		auditPath := fs.String("audit", "", "write a JSON-lines audit trail")
		fs.Parse(os.Args[2:])
		err := host.Run(host.Options{
			Addr:       *addr,
			Relay:      *relayAddr,
			Shell:      *shell,
			AllowWrite: *write,
			Code:       *code,
			Plain:      *plain,
			Record:     *recordPath,
			Audit:      *auditPath,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "join":
		fs := flag.NewFlagSet("join", flag.ExitOnError)
		name := fs.String("name", "", "display name other participants see")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: cotty join [-name NAME] <url>")
			os.Exit(2)
		}
		if err := client.Run(fs.Arg(0), *name); err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "ctl":
		fs := flag.NewFlagSet("ctl", flag.ExitOnError)
		code := fs.String("code", "", "session code to target")
		fs.Parse(os.Args[2:])
		op := fs.Arg(0)
		name := fs.Arg(1)
		switch {
		case op == "list" && fs.NArg() == 1:
		case (op == "allow" || op == "deny" || op == "kick") && fs.NArg() == 2:
		default:
			fmt.Fprintln(os.Stderr, "usage: cotty ctl [-code CODE] list | allow NAME | deny NAME | kick NAME")
			os.Exit(2)
		}
		path, err := ctl.Discover(*code)
		if err == nil {
			var text string
			text, err = ctl.Call(path, op, name)
			if err == nil {
				fmt.Println(text)
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "relay":
		fs := flag.NewFlagSet("relay", flag.ExitOnError)
		addr := fs.String("addr", ":7374", "listen address")
		publicURL := fs.String("public-url", "", "base URL guests use to reach this relay")
		fs.Parse(os.Args[2:])
		if err := relay.Run(relay.Options{Addr: *addr, PublicURL: *publicURL}); err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "replay":
		fs := flag.NewFlagSet("replay", flag.ExitOnError)
		speed := fs.Float64("speed", 1, "playback speed multiplier")
		maxIdle := fs.Duration("max-idle", 2*time.Second, "cap pauses between events (0 keeps original timing)")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: cotty replay [-speed N] [-max-idle DUR] <file.cast>")
			os.Exit(2)
		}
		if err := replay.Run(fs.Arg(0), replay.Options{Speed: *speed, MaxIdle: *maxIdle}); err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "desk":
		fs := flag.NewFlagSet("desk", flag.ExitOnError)
		symbols := fs.String("symbols", strings.Join(desk.DefaultSymbols, ","), `watch list, or "all"`)
		rpc := fs.String("rpc", "", "Robinhood Chain RPC endpoint")
		interval := fs.Duration("interval", 15*time.Second, "refresh interval")
		alert := fs.Float64("alert", 1, "alert threshold in percent")
		carry := fs.Float64("carry", 10, "funding alert threshold in annualized percent")
		noPerps := fs.Bool("no-perps", false, "skip the Lighter perps section")
		once := fs.Bool("once", false, "print one snapshot and exit")
		fs.Parse(os.Args[2:])
		err := desk.Run(desk.Options{
			RPC:      *rpc,
			Symbols:  strings.Split(*symbols, ","),
			Interval: *interval,
			Once:     *once,
			AlertPct: *alert,
			CarryAPR: *carry,
			NoPerps:  *noPerps,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "agent":
		fs := flag.NewFlagSet("agent", flag.ExitOnError)
		name := fs.String("name", "agent", "display name")
		provider := fs.String("provider", "anthropic", "anthropic or openai (any OpenAI-compatible endpoint)")
		model := fs.String("model", "", "model to use")
		baseURL := fs.String("base-url", "", "API endpoint override")
		keyEnv := fs.String("key-env", "", "environment variable holding the API key")
		steps := fs.Int("steps", 4, "max model calls per question")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: cotty agent [-provider anthropic|openai] [-model M] [-name NAME] <join-url>")
			os.Exit(2)
		}
		err := agent.Run(agent.Options{
			URL:      fs.Arg(0),
			Name:     *name,
			MaxSteps: *steps,
			ProviderOptions: agent.ProviderOptions{
				Provider: *provider,
				Model:    *model,
				BaseURL:  *baseURL,
				KeyEnv:   *keyEnv,
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "ask":
		fs := flag.NewFlagSet("ask", flag.ExitOnError)
		code := fs.String("code", "", "session code to target")
		fs.Parse(os.Args[2:])
		question := strings.Join(fs.Args(), " ")
		if strings.TrimSpace(question) == "" {
			fmt.Fprintln(os.Stderr, "usage: cotty ask [-code CODE] <question>")
			os.Exit(2)
		}
		path, err := ctl.Discover(*code)
		if err == nil {
			var answer string
			answer, err = ctl.Ask(path, question)
			if err == nil {
				fmt.Println(answer)
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "cotty: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("cotty " + version)
	case "-h", "--help", "help":
		printBanner(os.Stderr)
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "cotty: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
