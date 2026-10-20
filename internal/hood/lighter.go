package hood

import (
	"context"
	"strconv"
)

// Lighter is the perps venue that launched alongside Robinhood Chain.
// Its order books, positions, and funding live in Lighter's own zk
// engine rather than as chain transactions, so they are read from its
// public API.
const lighterAPI = "https://mainnet.zklighter.elliot.ai/api/v1"

// Perp is a Lighter perpetual market. Stock perps track the underlying
// share price, not the multiplier-adjusted Stock Token price.
type Perp struct {
	Symbol       string
	Mark         float64
	Index        float64
	OpenInterest float64 // in base units
	// Funding8h is the current funding rate as a fraction per 8 hours
	// (Lighter settles hourly, an eighth of this each hour). Positive
	// means longs pay shorts.
	Funding8h  float64
	HasFunding bool
}

// FundingAPR annualizes the funding rate: three 8-hour periods a day.
func (p Perp) FundingAPR() float64 { return p.Funding8h * 3 * 365 }

// Perps returns Lighter's active perpetual markets keyed by symbol, with
// their current funding rates.
func (c *Client) Perps(ctx context.Context) (map[string]Perp, error) {
	var books struct {
		OrderBookDetails []struct {
			Symbol       string  `json:"symbol"`
			MarketType   string  `json:"market_type"`
			Status       string  `json:"status"`
			MarkPrice    string  `json:"mark_price"`
			IndexPrice   string  `json:"index_price"`
			OpenInterest float64 `json:"open_interest"`
		} `json:"order_book_details"`
	}
	if err := c.getJSON(ctx, lighterAPI+"/orderBookDetails?filter=perp", &books); err != nil {
		return nil, err
	}
	// The funding endpoint also carries other venues' rates for
	// comparison; only Lighter's own apply here.
	var rates struct {
		FundingRates []struct {
			Exchange string  `json:"exchange"`
			Symbol   string  `json:"symbol"`
			Rate     float64 `json:"rate"`
		} `json:"funding_rates"`
	}
	if err := c.getJSON(ctx, lighterAPI+"/funding-rates", &rates); err != nil {
		return nil, err
	}

	out := make(map[string]Perp)
	for _, b := range books.OrderBookDetails {
		if b.MarketType != "perp" || b.Status != "active" {
			continue
		}
		mark, err := strconv.ParseFloat(b.MarkPrice, 64)
		if err != nil || mark <= 0 {
			continue
		}
		index, _ := strconv.ParseFloat(b.IndexPrice, 64)
		out[b.Symbol] = Perp{Symbol: b.Symbol, Mark: mark, Index: index, OpenInterest: b.OpenInterest}
	}
	for _, r := range rates.FundingRates {
		if p, ok := out[r.Symbol]; ok && r.Exchange == "lighter" {
			p.Funding8h, p.HasFunding = r.Rate, true
			out[r.Symbol] = p
		}
	}
	return out, nil
}
