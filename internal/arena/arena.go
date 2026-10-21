// Package arena implements `cotty arena`: AI models compete at paper
// trading Robinhood Chain Stock Tokens on live prices, on one leaderboard
// that a whole cotty session can watch.
//
// Every round each model sees the same market (live quotes, Chainlink
// feeds, Lighter perp funding) and its own virtual portfolio, and replies
// with up to three trades and one sentence of reasoning. Buys fill at the
// ask and sells at the bid; positions are marked at the mid. A passive
// buy-and-hold contestant gives the models something to beat. Nothing is
// signed or sent on-chain: it is paper trading, read-only end to end.
package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/tylerbroqs/cotty/internal/agent"
	"github.com/tylerbroqs/cotty/internal/desk"
	"github.com/tylerbroqs/cotty/internal/hood"
)

// DefaultModels are the contestants when none are named: one from each
// of three labs, all reachable with one Orbio key.
var DefaultModels = []string{"anthropic/claude-opus-5.5", "openai/gpt-6-astra", "google/gemini-3.8-flash"}

// DefaultSymbols is the tradable universe when none is given.
var DefaultSymbols = []string{"TSLA", "NVDA", "AAPL", "MSFT", "AMZN", "META", "GOOGL", "MSTR", "COIN", "PLTR", "SPY", "QQQ"}

// Options configures an arena.
type Options struct {
	// Models are the contestants' model IDs on the provider.
	Models []string
	// Provider selects where the models run; its Model field is ignored.
	Provider agent.ProviderOptions
	// Symbols is the tradable universe of Stock Tokens.
	Symbols []string
	// Round is the time between trading rounds (minimum 1 minute).
	Round time.Duration
	// Cash is each contestant's starting cash in dollars.
	Cash float64
	// Benchmark is the symbol the buy-and-hold contestant holds.
	Benchmark string
	// State is where the competition is saved between runs; empty means
	// the user config directory.
	State string
	// Once plays a single round, prints the board, and exits.
	Once bool
	// RPC is the Robinhood Chain RPC endpoint (default public mainnet).
	RPC string
}

const (
	refresh      = 15 * time.Second
	modelTimeout = 2 * time.Minute
)

// competition is what's saved between runs.
type competition struct {
	Started  time.Time             `json:"started"`
	Round    int                   `json:"round"`
	Cash     float64               `json:"cash"`
	LastPlay time.Time             `json:"last_play"`
	Players  map[string]*Portfolio `json:"players"`
}

type contestant struct {
	name  string // short display name
	model *agent.Model
}

type arena struct {
	opts    Options
	hood    *hood.Client
	players []contestant
	bench   string // display name of the buy-and-hold contestant
	comp    *competition
	feeds   map[string]hood.Feed
	tokens  map[string]string // symbol → token address
	out     io.Writer
	color   bool
}

// Run plays rounds until interrupted (or one round, with Once).
func Run(opts Options) error {
	if len(opts.Models) == 0 {
		opts.Models = DefaultModels
	}
	if len(opts.Symbols) == 0 {
		opts.Symbols = DefaultSymbols
	}
	if opts.Round < time.Minute {
		opts.Round = time.Minute
	}
	if opts.Cash <= 0 {
		opts.Cash = 10000
	}
	if opts.Benchmark == "" {
		opts.Benchmark = "SPY"
	}
	if opts.State == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			opts.State = filepath.Join(dir, "cotty", "arena.json")
		}
	}

	a := &arena{
		opts:  opts,
		hood:  hood.New(opts.RPC),
		out:   os.Stdout,
		color: term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "",
		bench: "HODL " + strings.ToUpper(opts.Benchmark),
	}
	for _, id := range opts.Models {
		po := opts.Provider
		po.Model = id
		m, err := agent.NewModel(po)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		a.players = append(a.players, contestant{name: shortName(id), model: m})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.load(ctx); err != nil {
		return err
	}

	if opts.Once {
		if err := a.play(ctx); err != nil {
			return err
		}
		return a.draw(ctx, time.Time{})
	}

	// Pick up where a previous run left off rather than trading again
	// the moment it restarts.
	next := a.comp.LastPlay.Add(opts.Round)
	tick := time.NewTicker(refresh)
	defer tick.Stop()
	for {
		if !time.Now().Before(next) {
			if err := a.play(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(a.out, "\r\nround failed: %v\r\n", err)
			}
			next = time.Now().Add(opts.Round)
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := a.draw(ctx, next); err != nil && ctx.Err() == nil {
			fmt.Fprintf(a.out, "\r\nrefresh failed: %v\r\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// shortName drops a provider prefix: anthropic/claude-opus-5.5 →
// claude-opus-5.5.
func shortName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// load resolves the universe and restores (or starts) the competition.
func (a *arena) load(ctx context.Context) error {
	assets, err := a.hood.Assets(ctx)
	if err != nil {
		return fmt.Errorf("stock token list: %w", err)
	}
	a.tokens = make(map[string]string)
	for _, as := range assets {
		if as.Active {
			a.tokens[as.Symbol] = as.Address
		}
	}
	var universe []string
	for _, s := range a.opts.Symbols {
		s = strings.ToUpper(strings.TrimSpace(s))
		if _, ok := a.tokens[s]; ok {
			universe = append(universe, s)
		} else if s != "" {
			fmt.Fprintf(os.Stderr, "cotty: no Stock Token %s; leaving it out\n", s)
		}
	}
	if len(universe) == 0 {
		return fmt.Errorf("no tradable symbols")
	}
	a.opts.Symbols = universe
	if a.feeds, err = a.hood.Feeds(ctx); err != nil {
		a.feeds = nil // feeds are context for the models, not required
	}

	a.comp = &competition{Started: time.Now(), Cash: a.opts.Cash, Players: map[string]*Portfolio{}}
	if b, err := os.ReadFile(a.opts.State); err == nil {
		var c competition
		if json.Unmarshal(b, &c) == nil && c.Players != nil {
			a.comp = &c
		}
	}
	names := []string{a.bench}
	for _, p := range a.players {
		names = append(names, p.name)
	}
	for _, n := range names {
		if a.comp.Players[n] == nil {
			// A contestant joining later starts with the same cash; the
			// board shows each one's return on its own start.
			a.comp.Players[n] = &Portfolio{Cash: a.comp.Cash, Tokens: map[string]float64{}}
		}
	}
	return nil
}

func (a *arena) save() {
	if a.opts.State == "" {
		return
	}
	b, err := json.MarshalIndent(a.comp, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(a.opts.State), 0o700) != nil {
		return
	}
	tmp := a.opts.State + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, a.opts.State)
	}
}

// quotes fetches live quotes for the universe (plus anything still held).
func (a *arena) quotes(ctx context.Context) (map[string]Quote, map[string]hood.Quote, error) {
	all, err := a.hood.Quotes(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]Quote)
	for _, s := range a.opts.Symbols {
		if q, ok := all[s]; ok && q.TokenBid > 0 && q.TokenAsk > 0 {
			out[s] = Quote{Bid: q.TokenBid, Ask: q.TokenAsk, Halted: q.Halted}
		}
	}
	return out, all, nil
}

// play runs one trading round: every model decides in parallel on the
// same market, then the decisions are executed in a fixed order.
func (a *arena) play(ctx context.Context) error {
	quotes, raw, err := a.quotes(ctx)
	if err != nil {
		return fmt.Errorf("quotes: %w", err)
	}
	if len(quotes) == 0 {
		return fmt.Errorf("no quotes for the universe")
	}
	a.comp.Round++
	round := a.comp.Round
	market := a.marketBrief(ctx, quotes, raw)

	// The benchmark buys once and holds.
	if b := a.comp.Players[a.bench]; b.Trades == 0 {
		fills := b.Execute([]Order{{Symbol: a.opts.Benchmark, Side: "buy", USD: b.Cash}}, quotes)
		b.addNote(Note{Round: round, Text: "Buy the index and hold.", Fills: fills})
	}

	type result struct {
		d   Decision
		err error
	}
	results := make([]result, len(a.players))
	var wg sync.WaitGroup
	for i, c := range a.players {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mctx, cancel := context.WithTimeout(ctx, modelTimeout)
			defer cancel()
			reply, err := c.model.Ask(mctx, systemPrompt, a.prompt(c.name, round, market, quotes))
			if err == nil {
				results[i].d, err = parseDecision(reply)
			}
			results[i].err = err
		}()
	}
	wg.Wait()

	for i, c := range a.players {
		p := a.comp.Players[c.name]
		r := results[i]
		if r.err != nil {
			p.Errors++
			p.addNote(Note{Round: round, Text: "(no decision: " + firstLine(r.err.Error()) + ")"})
			continue
		}
		fills := p.Execute(r.d.Trades, quotes)
		if fills == "" {
			fills = "hold"
		}
		p.addNote(Note{Round: round, Text: r.d.Note, Fills: fills})
	}
	a.comp.LastPlay = time.Now()
	a.save()
	return nil
}

// pct formats a return as a signed percentage, without a "-0.00%".
func pct(r float64) string {
	s := fmt.Sprintf("%+.2f%%", r*100)
	if s == "-0.00%" {
		return "+0.00%"
	}
	return s
}

// age formats a duration compactly: 2m, 4h55m, 3d2h.
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// marketBrief renders the market every model sees this round: quotes,
// day range, Chainlink feed, corporate-action multiplier, and the Lighter
// perp's basis and funding where one exists.
func (a *arena) marketBrief(ctx context.Context, quotes map[string]Quote, raw map[string]hood.Quote) string {
	var feeds []hood.Feed
	var tokens []string
	for _, s := range a.opts.Symbols {
		if f, ok := a.feeds[s]; ok {
			feeds = append(feeds, f)
		}
		tokens = append(tokens, a.tokens[s])
	}
	snap, _ := a.hood.Read(ctx, feeds, tokens)
	perps, _ := a.hood.Perps(ctx)

	var b strings.Builder
	fmt.Fprintf(&b, "symbol | bid | ask | day low-high (per share) | chainlink | shares/token | perp basis | perp funding APR | status\n")
	for _, s := range a.opts.Symbols {
		q, ok := quotes[s]
		if !ok {
			fmt.Fprintf(&b, "%s | no quote\n", s)
			continue
		}
		feed, mult := "-", 1.0
		if snap != nil {
			if f, ok := a.feeds[s]; ok {
				if r, ok := snap.Rounds[f.Proxy]; ok {
					feed = fmt.Sprintf("%.2f (%s old)", r.Price, age(time.Since(r.UpdatedAt)))
				}
			}
			if t, ok := snap.Tokens[a.tokens[s]]; ok && t.Multiplier > 0 {
				mult = t.Multiplier
			}
		}
		basis, funding := "-", "-"
		if p, ok := perps[s]; ok {
			basis = fmt.Sprintf("%+.2f%%", (p.Mark-q.mid()/mult)/(q.mid()/mult)*100)
			if p.HasFunding {
				funding = fmt.Sprintf("%+.1f%%", p.FundingAPR()*100)
			}
		}
		status := "open"
		if q.Halted {
			status = "HALTED"
		}
		r := raw[s]
		fmt.Fprintf(&b, "%s | %.2f | %.2f | %.2f-%.2f | %s | %.4f | %s | %s | %s\n",
			s, q.Bid, q.Ask, r.Low, r.High, feed, mult, basis, funding, status)
	}
	return b.String()
}

func (a *arena) prompt(name string, round int, market string, quotes map[string]Quote) string {
	p := a.comp.Players[name]
	var b strings.Builder
	now := time.Now()
	fmt.Fprintf(&b, "Round %d. %s UTC. US market session: %s.\n\n", round, now.UTC().Format("Mon 2006-01-02 15:04"), desk.Session(now))
	b.WriteString("Market (Stock Token prices in USD per token; buys fill at the ask, sells at the bid):\n")
	b.WriteString(market)

	eq := p.Equity(quotes)
	fmt.Fprintf(&b, "\nYour portfolio: equity $%s (%s since start), cash $%s.\n", money(eq), pct(eq/a.comp.Cash-1), money(p.Cash))
	for _, h := range p.Holdings(quotes) {
		fmt.Fprintf(&b, "- %s: %.4f tokens, worth $%s\n", h.Symbol, h.Tokens, money(h.Value))
	}
	if len(p.Notes) > 0 {
		b.WriteString("\nYour recent rounds:\n")
		for _, n := range p.Notes[max(0, len(p.Notes)-3):] {
			fmt.Fprintf(&b, "- round %d: %s [%s]\n", n.Round, n.Text, n.Fills)
		}
	}
	b.WriteString("\nLeaderboard:\n")
	for i, s := range a.standings(quotes) {
		you := ""
		if s.name == name {
			you = " (you)"
		}
		fmt.Fprintf(&b, "%d. %s%s %s\n", i+1, s.name, you, pct(s.ret))
	}
	return b.String()
}

type standing struct {
	name   string
	equity float64
	ret    float64
}

func (a *arena) standings(quotes map[string]Quote) []standing {
	var ss []standing
	add := func(name string) {
		if p := a.comp.Players[name]; p != nil {
			eq := p.Equity(quotes)
			ss = append(ss, standing{name, eq, eq/a.comp.Cash - 1})
		}
	}
	add(a.bench)
	for _, c := range a.players {
		add(c.name)
	}
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].equity > ss[j].equity })
	return ss
}

const systemPrompt = `You are a contestant in COTTY ARENA, a live paper-trading competition between AI models. You trade Robinhood Chain Stock Tokens (tokenized US stocks and ETFs) with a virtual portfolio: no real money is involved. A passive buy-and-hold contestant is on the leaderboard too; your goal is the highest equity, ahead of it and the other models.

Each round you get the live market and your portfolio. Reply with only a JSON object, no other text:
{"trades": [{"symbol": "NVDA", "side": "buy", "usd": 1500}], "note": "One sentence on your reasoning."}

Rules:
- Long only: "buy" spends cash, "sell" sells tokens you hold. "usd" is the dollar amount; it is clamped to your cash or to what you hold (sell a large amount to close a position).
- At most 3 trades per round; an empty "trades" list holds.
- Buys fill at the ask and sells at the bid, so every round trip pays the spread: don't churn.
- Only the listed symbols trade, and not while halted.
- "note" is shown live to the audience: one plain sentence, at most 140 characters, about why. No hype.

Context: a Stock Token can be worth more than one share ("shares/token", which rises as dividends are reinvested). Day range is per share. Perp basis compares the Lighter perpetual with the share price implied by the token; positive funding APR means perp longs pay shorts.`

// draw renders the board: standings marked to live quotes, each
// contestant's biggest positions, and the latest note from each.
func (a *arena) draw(ctx context.Context, next time.Time) error {
	quotes, _, err := a.quotes(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	if !a.opts.Once {
		b.WriteString("\x1b[H\x1b[2J")
	}
	elapsed := time.Since(a.comp.Started).Round(time.Minute)
	fmt.Fprintf(&b, "%s  Robinhood Chain · round %d · running %s · US market: %s\n",
		a.paint(bold+orange, "COTTY ARENA"), a.comp.Round, elapsed, desk.Session(time.Now()))
	fmt.Fprintf(&b, "%s\n\n", a.paint(dim, fmt.Sprintf("paper trading · $%s each · buys at ask, sells at bid, marked at mid · not financial advice", money(a.comp.Cash))))

	fmt.Fprintf(&b, "%s\n", a.paint(dim, fmt.Sprintf(" %-3s %-26s %11s %9s %7s  %s", "#", "CONTESTANT", "EQUITY", "RETURN", "TRADES", "TOP POSITIONS")))
	for i, s := range a.standings(quotes) {
		p := a.comp.Players[s.name]
		var pos []string
		for _, h := range p.Holdings(quotes) {
			if len(pos) == 3 {
				pos = append(pos, "…")
				break
			}
			pos = append(pos, fmt.Sprintf("%s $%s", h.Symbol, money(h.Value)))
		}
		if cashPct := p.Cash / s.equity * 100; cashPct >= 1 || len(pos) == 0 {
			pos = append(pos, fmt.Sprintf("cash %.0f%%", cashPct))
		}
		ret := pct(s.ret)
		switch ret[0] {
		case '+':
			if ret != "+0.00%" {
				ret = a.paint(green, ret)
			}
		case '-':
			ret = a.paint(red, ret)
		}
		name := padRight(s.name, 26)
		if s.name == a.bench {
			name = a.paint(dim, name)
		} else {
			name = a.paint(bold, name)
		}
		fmt.Fprintf(&b, " %-3d %s %11s %s %7d  %s\n", i+1, name, "$"+money(s.equity), padLeft(ret, 9), p.Trades, strings.Join(pos, " · "))
	}

	b.WriteString("\n")
	for _, c := range a.players {
		p := a.comp.Players[c.name]
		if len(p.Notes) == 0 {
			continue
		}
		n := p.Notes[len(p.Notes)-1]
		fmt.Fprintf(&b, "%s %s\n", a.paint(bold+orange, "["+c.name+"]"), n.Text)
		if n.Fills != "" {
			fmt.Fprintf(&b, "%s\n", a.paint(dim, fmt.Sprintf("%*s round %d: %s", len(c.name)+1, "", n.Round, n.Fills)))
		}
	}
	if !next.IsZero() {
		fmt.Fprintf(&b, "\n%s\n", a.paint(dim, fmt.Sprintf("next round in %s · round every %s · Ctrl-C to stop (progress is saved)",
			time.Until(next).Round(time.Second), a.opts.Round)))
	}
	_, err = io.WriteString(a.out, strings.ReplaceAll(b.String(), "\n", "\r\n"))
	return err
}

const (
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	orange = "\x1b[38;5;208m"
	reset  = "\x1b[0m"
)

func (a *arena) paint(code, s string) string {
	if !a.color {
		return s
	}
	return code + s + reset
}

func visible(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc:
			esc = r != 'm'
		default:
			n++
		}
	}
	return n
}

func padRight(s string, n int) string {
	if w := visible(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func padLeft(s string, n int) string {
	if w := visible(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}
