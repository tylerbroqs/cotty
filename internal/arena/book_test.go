package arena

import (
	"math"
	"strings"
	"testing"
)

var testQuotes = map[string]Quote{
	"NVDA": {Bid: 199, Ask: 200},
	"TSLA": {Bid: 399, Ask: 400},
	"HALT": {Bid: 10, Ask: 10, Halted: true},
}

func TestBuyAndSellFillAtTheTouch(t *testing.T) {
	p := &Portfolio{Cash: 10000}
	p.Execute([]Order{{Symbol: "nvda", Side: "buy", USD: 2000}}, testQuotes)
	if p.Cash != 8000 || p.Tokens["NVDA"] != 10 {
		t.Fatalf("after buy: cash %v, NVDA %v", p.Cash, p.Tokens["NVDA"])
	}
	// Marked at the mid (199.5), so the spread is an immediate cost.
	if eq := p.Equity(testQuotes); eq != 8000+10*199.5 {
		t.Errorf("equity = %v", eq)
	}
	fills := p.Execute([]Order{{Symbol: "NVDA", Side: "sell", USD: 1e9}}, testQuotes)
	if _, ok := p.Tokens["NVDA"]; ok || p.Cash != 8000+10*199 {
		t.Errorf("after sell-all: cash %v, tokens %v (%s)", p.Cash, p.Tokens, fills)
	}
}

func TestOrdersAreClamped(t *testing.T) {
	p := &Portfolio{Cash: 500}
	fills := p.Execute([]Order{
		{Symbol: "TSLA", Side: "buy", USD: 5000}, // only $500 to spend
		{Symbol: "TSLA", Side: "buy", USD: 100},  // nothing left
		{Symbol: "NVDA", Side: "sell", USD: 100}, // nothing held
		{Symbol: "HALT", Side: "buy", USD: 100},  // ignored: past the 3-order limit
	}, testQuotes)
	if p.Cash != 0 || math.Abs(p.Tokens["TSLA"]-1.25) > 1e-12 {
		t.Errorf("cash %v, TSLA %v", p.Cash, p.Tokens["TSLA"])
	}
	for _, want := range []string{"BUY TSLA $500.00", "no cash", "nothing to sell", "ignored 1 more"} {
		if !strings.Contains(fills, want) {
			t.Errorf("fills %q lack %q", fills, want)
		}
	}
}

func TestRejectsUnknownHaltedAndNegative(t *testing.T) {
	p := &Portfolio{Cash: 1000}
	fills := p.Execute([]Order{
		{Symbol: "DOGE", Side: "buy", USD: 100},
		{Symbol: "HALT", Side: "buy", USD: 100},
		{Symbol: "NVDA", Side: "buy", USD: -500},
	}, testQuotes)
	if p.Cash != 1000 || len(p.Tokens) != 0 {
		t.Errorf("portfolio changed: cash %v, tokens %v (%s)", p.Cash, p.Tokens, fills)
	}
}

func TestParseDecision(t *testing.T) {
	d, err := parseDecision("Sure! ```json\n{\"trades\":[{\"symbol\":\"NVDA\",\"side\":\"buy\",\"usd\":1500}],\"note\":\"AI capex.\"}\n```")
	if err != nil || len(d.Trades) != 1 || d.Trades[0].USD != 1500 || d.Note != "AI capex." {
		t.Errorf("got %+v, %v", d, err)
	}
	if _, err := parseDecision("I'll hold."); err == nil {
		t.Error("prose without JSON accepted")
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{512.4: "512.40", 12345.6: "12,346", -1500: "-1,500"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}
