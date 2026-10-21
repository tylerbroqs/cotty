package arena

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Quote is what a trade fills against: buys pay the ask, sells get the
// bid, positions are marked at the mid.
type Quote struct {
	Bid, Ask float64
	Halted   bool
}

func (q Quote) mid() float64 { return (q.Bid + q.Ask) / 2 }

// Portfolio is one contestant's virtual account. Positions are in Stock
// Tokens, fractional like the tokens themselves.
type Portfolio struct {
	Model  string             `json:"model"`
	Cash   float64            `json:"cash"`
	Tokens map[string]float64 `json:"tokens"`
	Notes  []Note             `json:"notes"`
	Trades int                `json:"trades"`
	Errors int                `json:"errors"`
}

// Note is what a contestant did and said in one round.
type Note struct {
	Round int    `json:"round"`
	Text  string `json:"text"`
	Fills string `json:"fills"`
}

const keepNotes = 6

// Equity is cash plus positions marked at the mid. Positions without a
// quote count at zero, which is conservative and rare.
func (p *Portfolio) Equity(quotes map[string]Quote) float64 {
	e := p.Cash
	for sym, n := range p.Tokens {
		if q, ok := quotes[sym]; ok {
			e += n * q.mid()
		}
	}
	return e
}

// Holdings lists positions by value, largest first.
func (p *Portfolio) Holdings(quotes map[string]Quote) []Holding {
	var hs []Holding
	for sym, n := range p.Tokens {
		if n <= 0 {
			continue
		}
		hs = append(hs, Holding{Symbol: sym, Tokens: n, Value: n * quotes[sym].mid()})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Value > hs[j].Value })
	return hs
}

// Holding is one position.
type Holding struct {
	Symbol string
	Tokens float64
	Value  float64
}

func (p *Portfolio) addNote(n Note) {
	p.Notes = append(p.Notes, n)
	if len(p.Notes) > keepNotes {
		p.Notes = p.Notes[len(p.Notes)-keepNotes:]
	}
}

// Order is one trade a model asks for: a dollar amount to buy or sell.
type Order struct {
	Symbol string  `json:"symbol"`
	Side   string  `json:"side"`
	USD    float64 `json:"usd"`
}

// Decision is a model's reply for one round.
type Decision struct {
	Trades []Order `json:"trades"`
	Note   string  `json:"note"`
}

const (
	maxOrders = 3
	minOrder  = 1.0 // dollars; smaller orders are noise
	dust      = 1e-9
)

// parseDecision reads the JSON object out of a model's reply, tolerating
// prose or code fences around it.
func parseDecision(reply string) (Decision, error) {
	i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if i < 0 || j <= i {
		return Decision{}, fmt.Errorf("no JSON object in reply")
	}
	var d Decision
	if err := json.Unmarshal([]byte(reply[i:j+1]), &d); err != nil {
		return Decision{}, fmt.Errorf("bad JSON in reply: %v", err)
	}
	d.Note = strings.TrimSpace(d.Note)
	if len(d.Note) > 200 {
		d.Note = strings.ToValidUTF8(d.Note[:200], "") + "…"
	}
	return d, nil
}

// Execute applies a decision's orders in turn against quotes, clamping
// each to what the portfolio can afford or holds, and returns a summary
// of what filled (or why not). Only symbols in quotes can trade.
func (p *Portfolio) Execute(orders []Order, quotes map[string]Quote) string {
	if p.Tokens == nil {
		p.Tokens = make(map[string]float64)
	}
	var fills []string
	for i, o := range orders {
		if i == maxOrders {
			fills = append(fills, fmt.Sprintf("ignored %d more orders (max %d)", len(orders)-maxOrders, maxOrders))
			break
		}
		sym := strings.ToUpper(strings.TrimSpace(o.Symbol))
		q, ok := quotes[sym]
		switch {
		case !ok:
			fills = append(fills, sym+": not tradable here")
			continue
		case q.Halted:
			fills = append(fills, sym+": halted")
			continue
		case math.IsNaN(o.USD) || o.USD < minOrder:
			continue
		}
		switch strings.ToLower(o.Side) {
		case "buy":
			if q.Ask <= 0 {
				fills = append(fills, sym+": no ask")
				continue
			}
			usd := math.Min(o.USD, p.Cash)
			if usd < minOrder {
				fills = append(fills, "BUY "+sym+": no cash")
				continue
			}
			p.Cash -= usd
			p.Tokens[sym] += usd / q.Ask
			p.Trades++
			fills = append(fills, fmt.Sprintf("BUY %s $%s", sym, money(usd)))
		case "sell":
			held := p.Tokens[sym]
			if held <= dust || q.Bid <= 0 {
				fills = append(fills, "SELL "+sym+": nothing to sell")
				continue
			}
			n := math.Min(o.USD/q.Bid, held)
			if held-n < 1e-6 { // close out instead of leaving dust
				n = held
			}
			p.Tokens[sym] -= n
			if p.Tokens[sym] <= dust {
				delete(p.Tokens, sym)
			}
			p.Cash += n * q.Bid
			p.Trades++
			fills = append(fills, fmt.Sprintf("SELL %s $%s", sym, money(n*q.Bid)))
		default:
			fills = append(fills, fmt.Sprintf("%s: unknown side %q", sym, o.Side))
		}
	}
	return strings.Join(fills, " · ")
}

// money formats dollars with thousands separators and no cents above
// $1,000 (e.g. 12,345 or 512.40).
func money(v float64) string {
	neg := v < 0
	v = math.Abs(v)
	var s string
	if v >= 1000 {
		whole := fmt.Sprintf("%.0f", v)
		var b strings.Builder
		for i, c := range whole {
			if i > 0 && (len(whole)-i)%3 == 0 {
				b.WriteByte(',')
			}
			b.WriteRune(c)
		}
		s = b.String()
	} else {
		s = fmt.Sprintf("%.2f", v)
	}
	if neg {
		return "-" + s
	}
	return s
}
