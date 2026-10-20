// Package desk is a read-only Robinhood Chain market dashboard for the
// terminal. Run it inside `cotty host` and everyone in the session
// watches the same screen: Stock Token quotes, Chainlink feeds, how far
// the two disagree, and how far prices have moved since the last
// regular US session (the weekend and overnight gap).
//
// It holds no keys and sends no transactions.
package desk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // session hours are in New York time on any host

	"golang.org/x/term"

	"github.com/tylerbroqs/cotty/internal/hood"
)

// DefaultSymbols is the watch list when none is given.
var DefaultSymbols = []string{"TSLA", "NVDA", "AAPL", "MSTR", "COIN", "SPY", "QQQ", "ETH", "BTC"}

// Options configures the desk.
type Options struct {
	// RPC is the JSON-RPC endpoint; empty means the public mainnet RPC.
	RPC string
	// Symbols to watch. "all" watches every Stock Token with a feed.
	Symbols []string
	// Interval between refreshes. The quote API caches for 15s, so
	// faster refreshes only repeat data.
	Interval time.Duration
	// Once prints a single snapshot without clearing the screen.
	Once bool
	// AlertPct flags a quote/feed disagreement or session gap of at
	// least this many percent.
	AlertPct float64
	// State is where last regular-session prices are kept between runs;
	// empty means the user config directory.
	State string
	// CarryAPR flags a Lighter perp whose annualized funding is at least
	// this many percent either way.
	CarryAPR float64
	// NoPerps skips the Lighter basis section.
	NoPerps bool
}

// Run refreshes the dashboard until interrupted (or once, with Once).
func Run(opts Options) error {
	if opts.Interval < 5*time.Second {
		opts.Interval = 5 * time.Second
	}
	if opts.AlertPct <= 0 {
		opts.AlertPct = 1
	}
	if opts.CarryAPR <= 0 {
		opts.CarryAPR = 10
	}
	if len(opts.Symbols) == 0 {
		opts.Symbols = DefaultSymbols
	}
	if opts.State == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			opts.State = filepath.Join(dir, "cotty", "desk-state.json")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := &desk{
		opts:  opts,
		c:     hood.New(opts.RPC),
		out:   os.Stdout,
		color: term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "",
		refs:  loadRefs(opts.State),
	}
	if err := d.load(ctx); err != nil {
		return err
	}
	if opts.Once {
		return d.refresh(ctx)
	}
	t := time.NewTicker(opts.Interval)
	defer t.Stop()
	for {
		if err := d.refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A failed refresh keeps the loop alive: public endpoints
			// rate limit and blip, and the next tick usually recovers.
			fmt.Fprintf(d.out, "\r\n%s\r\n", d.paint(red, "refresh failed: "+err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// row is one watched symbol and where its data comes from.
type row struct {
	sym   string
	token string     // Stock Token address, "" for crypto
	feed  *hood.Feed // nil when Chainlink has no USD feed for it
}

// ref is a price seen during a regular session.
type ref struct {
	Price float64   `json:"price"`
	At    time.Time `json:"at"`
}

type desk struct {
	opts  Options
	c     *hood.Client
	out   io.Writer
	color bool
	rows  []row
	refs  map[string]ref
}

// load resolves symbols to token and feed addresses once at startup;
// listings change rarely and both sources are rate limited.
func (d *desk) load(ctx context.Context) error {
	assets, err := d.c.Assets(ctx)
	if err != nil {
		return fmt.Errorf("stock token list: %w", err)
	}
	feeds, err := d.c.Feeds(ctx)
	if err != nil {
		return fmt.Errorf("chainlink feed list: %w", err)
	}
	tokens := make(map[string]string, len(assets))
	for _, a := range assets {
		if a.Active {
			tokens[a.Symbol] = a.Address
		}
	}

	syms := d.opts.Symbols
	if len(syms) == 1 && strings.EqualFold(syms[0], "all") {
		syms = nil
		for s := range tokens {
			if _, ok := feeds[s]; ok {
				syms = append(syms, s)
			}
		}
		sort.Strings(syms)
	}
	var unknown []string
	for _, s := range syms {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		r := row{sym: s, token: tokens[s]}
		if f, ok := feeds[s]; ok {
			r.feed = &f
		}
		if r.token == "" && r.feed == nil {
			unknown = append(unknown, s)
			continue
		}
		d.rows = append(d.rows, r)
	}
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "cotty: no Stock Token or Chainlink feed for %s; skipping\n", strings.Join(unknown, ", "))
	}
	if len(d.rows) == 0 {
		return fmt.Errorf("nothing to watch")
	}
	return nil
}

func (d *desk) refresh(ctx context.Context) error {
	var feeds []hood.Feed
	var tokens []string
	for _, r := range d.rows {
		if r.feed != nil {
			feeds = append(feeds, *r.feed)
		}
		if r.token != "" {
			tokens = append(tokens, r.token)
		}
	}
	snap, err := d.c.Read(ctx, feeds, tokens)
	if err != nil {
		return err
	}
	if snap.ChainID != hood.ChainID {
		return fmt.Errorf("RPC is chain %d, not Robinhood Chain mainnet (%d)", snap.ChainID, hood.ChainID)
	}
	v := view{snap: snap, now: time.Now()}
	v.sess = sessionAt(v.now)
	v.quotes, v.qerr = d.c.Quotes(ctx)
	if !d.opts.NoPerps {
		v.perps, v.perr = d.c.Perps(ctx)
	}
	if v.sess == regular && v.qerr == nil {
		d.recordRefs(v.quotes, v.now)
	}

	var b strings.Builder
	if !d.opts.Once {
		b.WriteString("\x1b[H\x1b[2J")
	}
	d.render(&b, v)
	_, err = io.WriteString(d.out, strings.ReplaceAll(b.String(), "\n", "\r\n"))
	return err
}

// recordRefs remembers each token's latest regular-session mid so the
// gap can be measured once the session closes.
func (d *desk) recordRefs(quotes map[string]hood.Quote, now time.Time) {
	changed := false
	for _, r := range d.rows {
		q, ok := quotes[r.sym]
		if !ok || q.Mid() <= 0 || q.Halted {
			continue
		}
		d.refs[r.sym] = ref{Price: q.Mid(), At: now}
		changed = true
	}
	if changed {
		saveRefs(d.opts.State, d.refs)
	}
}

// view is everything one refresh read.
type view struct {
	snap   *hood.Snapshot
	quotes map[string]hood.Quote
	qerr   error
	perps  map[string]hood.Perp
	perr   error
	sess   session
	now    time.Time
}

func (d *desk) render(b *strings.Builder, v view) {
	s, quotes, sess, now := v.snap, v.quotes, v.sess, v.now
	ny := now.In(newYork)
	fmt.Fprintf(b, "%s  Robinhood Chain %d  block %s  (%s ago)  base fee %s gwei\n",
		d.paint(bold+orange, "COTTY DESK"), s.ChainID, commas(s.Head.Number),
		age(now.Sub(s.Head.Time)), gwei(s.Head))
	fmt.Fprintf(b, "%s  US market: %s\n\n", ny.Format("Mon 2006-01-02 15:04 MST"), d.paint(bold, sess.String()))

	fmt.Fprintf(b, "%s\n", d.paint(dim, fmt.Sprintf("%-6s %11s %11s %7s %11s %8s %9s %8s  %s",
		"SYMBOL", "BID", "ASK", "SPREAD", "CHAINLINK", "Δ FEED", "FEED AGE", "GAP", "FLAGS")))

	var alerts []string
	for _, r := range d.rows {
		q, hasQ := quotes[r.sym]
		hasQ = hasQ && q.Mid() > 0
		var rd hood.Round
		hasR := false
		if r.feed != nil {
			rd, hasR = s.Rounds[r.feed.Proxy]
		}
		ts, hasT := s.Tokens[r.token]

		bid, ask, spread := "-", "-", "-"
		if hasQ {
			bid, ask = price(q.TokenBid), price(q.TokenAsk)
			spread = fmt.Sprintf("%.2f%%", (q.TokenAsk-q.TokenBid)/q.Mid()*100)
		}
		feedPx, feedAge, dev := "-", "-", "-"
		if hasR {
			feedPx = price(rd.Price)
			feedAge = age(now.Sub(rd.UpdatedAt))
		}
		if hasQ && hasR {
			pct := (q.Mid() - rd.Price) / rd.Price * 100
			dev = d.signed(pct)
			// The feed only moves 24/5; on weekends a gap to the live
			// quote is expected and the gap column covers it.
			if math.Abs(pct) >= d.opts.AlertPct && sess != weekend {
				alerts = append(alerts, fmt.Sprintf("%s quote is %+.2f%% from its Chainlink feed", r.sym, pct))
			}
		}
		gap := "-"
		if rf, ok := d.refs[r.sym]; ok && hasQ && sess != regular {
			pct := (q.Mid() - rf.Price) / rf.Price * 100
			gap = d.signed(pct)
			if math.Abs(pct) >= d.opts.AlertPct {
				alerts = append(alerts, fmt.Sprintf("%s has moved %+.2f%% since the regular session (%s)",
					r.sym, pct, rf.At.In(newYork).Format("Mon 15:04")))
			}
		}

		var flags []string
		if hasQ && q.Halted {
			flags = append(flags, d.paint(red, "HALT"))
			alerts = append(alerts, r.sym+" is halted")
		}
		if hasT && ts.OraclePaused {
			flags = append(flags, d.paint(yellow, "ORACLE-PAUSED"))
			alerts = append(alerts, r.sym+" oracle is paused for a corporate action; treat its feed price as unavailable")
		}
		// Stock feeds pause over the weekend; crypto feeds never do.
		if hasR && (r.token == "" || sess != weekend) && now.Sub(rd.UpdatedAt) > r.feed.Heartbeat {
			flags = append(flags, d.paint(yellow, "STALE"))
			alerts = append(alerts, fmt.Sprintf("%s feed has not updated for %s (heartbeat %s)", r.sym, age(now.Sub(rd.UpdatedAt)), age(r.feed.Heartbeat)))
		}
		if hasT && ts.Multiplier > 0 && math.Abs(ts.Multiplier-1) > 1e-9 {
			flags = append(flags, fmt.Sprintf("x%.4f", ts.Multiplier))
		}
		if _, ok := s.Errors[r.token]; ok && r.token != "" {
			flags = append(flags, d.paint(red, "token read failed"))
		}
		if r.feed != nil {
			if _, ok := s.Errors[r.feed.Proxy]; ok {
				flags = append(flags, d.paint(red, "feed read failed"))
			}
		}

		fmt.Fprintf(b, "%s %11s %11s %7s %11s %s %9s %s  %s\n",
			padRight(d.paint(bold, r.sym), 6), bid, ask, spread, feedPx, pad(dev, 8), feedAge, pad(gap, 8), strings.Join(flags, " "))
	}

	b.WriteString("\n")
	if !d.opts.NoPerps {
		alerts = append(alerts, d.renderBasis(b, v)...)
	}
	if v.qerr != nil {
		fmt.Fprintf(b, "%s\n", d.paint(red, "quotes unavailable: "+v.qerr.Error()))
	}
	if len(alerts) == 0 {
		fmt.Fprintf(b, "%s\n", d.paint(green, "no alerts"))
	}
	for _, a := range alerts {
		fmt.Fprintf(b, "%s %s\n", d.paint(yellow, "!"), a)
	}
	fmt.Fprintf(b, "\n%s\n", d.paint(dim, fmt.Sprintf(
		"prices are per token (multiplier-adjusted) · Δ FEED: quote mid vs Chainlink · GAP: quote mid vs the last regular-session mid this desk saw · alert ≥ %.2g%%, carry ≥ %.3g%% APR · refresh %s · read-only",
		d.opts.AlertPct, d.opts.CarryAPR, d.opts.Interval)))
}

// renderBasis writes the Lighter perps section: for each watched symbol
// with a perp, the spot it hedges against, the basis, and the funding
// carry. It returns the alerts it raised.
func (d *desk) renderBasis(b *strings.Builder, v view) []string {
	if v.perr != nil {
		fmt.Fprintf(b, "%s\n\n", d.paint(red, "lighter perps unavailable: "+v.perr.Error()))
		return nil
	}
	var alerts []string
	header := false
	for _, r := range d.rows {
		p, ok := v.perps[r.sym]
		if !ok {
			continue
		}
		spot, src := 0.0, ""
		if q, ok := v.quotes[r.sym]; ok && q.Mid() > 0 {
			spot, src = q.Mid(), "token"
		} else if r.feed != nil {
			if rd, ok := v.snap.Rounds[r.feed.Proxy]; ok {
				spot, src = rd.Price, "chainlink"
			}
		}
		if spot <= 0 {
			continue
		}
		mult := 1.0
		if ts, ok := v.snap.Tokens[r.token]; ok && ts.Multiplier > 0 {
			mult = ts.Multiplier
		}
		if !header {
			fmt.Fprintf(b, "%s\n", d.paint(dim, fmt.Sprintf("%-6s %11s %11s %8s %11s %8s  %s",
				"LIGHTER", "PERP MARK", "SPOT", "BASIS", "FUNDING/8H", "APR", "CARRY")))
			header = true
		}

		underlying := spot / mult
		basis := basisPct(p.Mark, spot, mult)
		fund, apr, carry := "-", "-", ""
		if p.HasFunding {
			fund = fmt.Sprintf("%+.4f%%", p.Funding8h*100)
			a := p.FundingAPR() * 100
			apr = d.signed(a)
			carry = carryHint(a)
			if math.Abs(a) >= d.opts.CarryAPR {
				alerts = append(alerts, fmt.Sprintf("%s perp funding is %+.1f%% APR: %s", r.sym, a, carry))
			}
		}
		// The basis is only meaningful while both sides trade; Stock
		// Token quotes and feeds freeze over the weekend while perps don't.
		if math.Abs(basis) >= d.opts.AlertPct && (r.token == "" || v.sess != weekend) {
			alerts = append(alerts, fmt.Sprintf("%s perp is %+.2f%% from %s spot", r.sym, basis, src))
		}
		hedge := ""
		if mult != 1 {
			hedge = d.paint(dim, fmt.Sprintf("  (1 token = %.4f perp)", mult))
		}
		fmt.Fprintf(b, "%s %11s %11s %s %11s %s  %s%s\n",
			padRight(d.paint(bold, r.sym), 6), price(p.Mark), price(underlying),
			pad(d.signed(basis), 8), fund, pad(apr, 8), carry, hedge)
	}
	if header {
		b.WriteString("\n")
	}
	return alerts
}

// basisPct is how far a perp trades from the spot it hedges, in percent.
// Stock perps track the underlying share, while a Stock Token is worth
// multiplier shares, so the token price is divided down first.
func basisPct(perpMark, spot, multiplier float64) float64 {
	underlying := spot / multiplier
	return (perpMark - underlying) / underlying * 100
}

// carryHint names the side that collects funding.
func carryHint(apr float64) string {
	switch {
	case apr > 0:
		return "longs pay: long spot + short perp collects"
	case apr < 0:
		return "shorts pay: long perp collects"
	}
	return ""
}

func loadRefs(path string) map[string]ref {
	refs := make(map[string]ref)
	if path == "" {
		return refs
	}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &refs)
	}
	return refs
}

func saveRefs(path string, refs map[string]ref) {
	if path == "" {
		return
	}
	b, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}
