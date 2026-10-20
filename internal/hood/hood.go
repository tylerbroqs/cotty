// Package hood reads Robinhood Chain market data without keys or
// contracts of its own: the chain over public JSON-RPC, Stock Token
// metadata and quotes from Robinhood's read-only REST API, and the
// Chainlink feed directory for on-chain price feed addresses.
package hood

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Public endpoints. The RPC is free and rate limited, with no SLA;
// pass a provider endpoint for anything heavier than a dashboard.
const (
	MainnetRPC = "https://rpc.mainnet.chain.robinhood.com"
	ChainID    = 4663

	assetsURL = "https://api.robinhood.com/rhj/assets"
	pricesURL = "https://api.robinhood.com/rhj/prices"
	feedsURL  = "https://reference-data-directory.vercel.app/feeds-robinhood-mainnet.json"
)

// Function selectors (first 4 bytes of keccak256 of the signature).
const (
	selLatestRoundData = "0xfeaf968c" // latestRoundData()
	selUIMultiplier    = "0xa60bf13d" // uiMultiplier()
	selOraclePaused    = "0x7706ba52" // oraclePaused()
)

// Client talks to one RPC endpoint plus the fixed HTTP APIs.
type Client struct {
	RPC  string
	HTTP *http.Client
}

// New returns a client for rpc, or the public mainnet RPC when empty.
func New(rpc string) *Client {
	if rpc == "" {
		rpc = MainnetRPC
	}
	return &Client{RPC: rpc, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Asset is a Stock Token as listed by the Robinhood API.
type Asset struct {
	Symbol     string
	Name       string
	Address    string // on Robinhood Chain mainnet
	Multiplier float64
	Pending    string // pending multiplier, "" when none
	Active     bool
}

// Quote is a live API quote for one Stock Token.
type Quote struct {
	Symbol   string
	TokenBid float64 // multiplier-adjusted, comparable to Chainlink
	TokenAsk float64
	High     float64 // underlying daily range, not multiplier-adjusted
	Low      float64
	Halted   bool
	At       time.Time
}

// Mid is the token mid price.
func (q Quote) Mid() float64 { return (q.TokenBid + q.TokenAsk) / 2 }

// Feed is a Chainlink price feed proxy on Robinhood Chain.
type Feed struct {
	Name     string
	Proxy    string
	Decimals int
	// Heartbeat is the longest the feed may go without an update
	// while its market is open.
	Heartbeat time.Duration
}

// Round is a feed's latest answer.
type Round struct {
	Price     float64
	UpdatedAt time.Time
}

// TokenState is the on-chain state of a Stock Token that matters for
// pricing.
type TokenState struct {
	Multiplier   float64
	OraclePaused bool
}

// Head is the latest block.
type Head struct {
	Number  uint64
	Time    time.Time
	BaseFee *big.Int // wei
}

func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	return retry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return transient{err}
		}
		defer resp.Body.Close()
		if err := statusErr("GET "+url, resp); err != nil {
			return err
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			// A body cut off mid-stream decodes as an unexpected EOF.
			return transient{fmt.Errorf("GET %s: %w", url, err)}
		}
		return nil
	})
}

type deployment struct {
	ContractAddress string `json:"contractAddress"`
	ChainID         int    `json:"chainId"`
}

func mainnetAddress(ds []deployment) string {
	for _, d := range ds {
		if d.ChainID == ChainID {
			return d.ContractAddress
		}
	}
	return ""
}

// Assets lists the Stock Tokens deployed on Robinhood Chain mainnet.
func (c *Client) Assets(ctx context.Context) ([]Asset, error) {
	var r struct {
		Assets []struct {
			TokenSymbol       string       `json:"tokenSymbol"`
			TokenName         string       `json:"tokenName"`
			Deployments       []deployment `json:"deployments"`
			CurrentMultiplier string       `json:"currentMultiplier"`
			PendingMultiplier string       `json:"pendingMultiplier"`
			Status            string       `json:"status"`
		} `json:"assets"`
	}
	if err := c.getJSON(ctx, assetsURL, &r); err != nil {
		return nil, err
	}
	var out []Asset
	for _, a := range r.Assets {
		addr := mainnetAddress(a.Deployments)
		if addr == "" {
			continue
		}
		m, _ := strconv.ParseFloat(a.CurrentMultiplier, 64)
		out = append(out, Asset{
			Symbol:     a.TokenSymbol,
			Name:       a.TokenName,
			Address:    addr,
			Multiplier: m,
			Pending:    a.PendingMultiplier,
			Active:     a.Status == "ASSET_STATUS_ACTIVE",
		})
	}
	return out, nil
}

// Quotes returns live quotes for every Stock Token, keyed by symbol.
func (c *Client) Quotes(ctx context.Context) (map[string]Quote, error) {
	var r struct {
		Quotes []struct {
			TokenSymbol   string    `json:"tokenSymbol"`
			TokenBid      string    `json:"tokenBid"`
			TokenAsk      string    `json:"tokenAsk"`
			DailyHigh     string    `json:"dailyHigh"`
			DailyLow      string    `json:"dailyLow"`
			IsTradingHalt bool      `json:"isTradingHalt"`
			GeneratedAt   time.Time `json:"generatedAt"`
		} `json:"quotes"`
	}
	if err := c.getJSON(ctx, pricesURL, &r); err != nil {
		return nil, err
	}
	num := func(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
	out := make(map[string]Quote, len(r.Quotes))
	for _, q := range r.Quotes {
		out[q.TokenSymbol] = Quote{
			Symbol:   q.TokenSymbol,
			TokenBid: num(q.TokenBid),
			TokenAsk: num(q.TokenAsk),
			High:     num(q.DailyHigh),
			Low:      num(q.DailyLow),
			Halted:   q.IsTradingHalt,
			At:       q.GeneratedAt,
		}
	}
	return out, nil
}

// Feeds returns Chainlink's USD price feeds on Robinhood Chain, keyed by
// base symbol: "TSLA" for the Stock Token feed "Robinhood TSLA / USD",
// "ETH" for "ETH / USD".
func (c *Client) Feeds(ctx context.Context) (map[string]Feed, error) {
	var r []struct {
		Name         string `json:"name"`
		ProxyAddress string `json:"proxyAddress"`
		Decimals     int    `json:"decimals"`
		Heartbeat    int    `json:"heartbeat"`
	}
	if err := c.getJSON(ctx, feedsURL, &r); err != nil {
		return nil, err
	}
	out := make(map[string]Feed)
	for _, f := range r {
		sym, ok := feedSymbol(f.Name)
		if !ok || f.ProxyAddress == "" {
			continue
		}
		out[sym] = Feed{
			Name:      f.Name,
			Proxy:     f.ProxyAddress,
			Decimals:  f.Decimals,
			Heartbeat: time.Duration(f.Heartbeat) * time.Second,
		}
	}
	return out, nil
}

// feedSymbol extracts the base symbol from a USD feed name, accepting
// both "Robinhood TSLA / USD" and "Robinhood DELL-USD" spellings.
func feedSymbol(name string) (string, bool) {
	name = strings.TrimPrefix(name, "Robinhood ")
	for _, sep := range []string{" / USD", "-USD"} {
		if base, ok := strings.CutSuffix(name, sep); ok && !strings.ContainsAny(base, " /") {
			return strings.ToUpper(base), true
		}
	}
	return "", false
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResp struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// maxBatch keeps each JSON-RPC batch under the public RPC's limit,
// which rejects batches of more than about 40 calls with a 429.
const maxBatch = 25

// batch sends calls as JSON-RPC batches of at most maxBatch, which keeps
// a refresh to a few round trips against the rate-limited public
// endpoint. Results come back in call order; a per-call error leaves its
// result nil.
func (c *Client) batch(ctx context.Context, calls []rpcReq) ([]json.RawMessage, []error, error) {
	results := make([]json.RawMessage, len(calls))
	errs := make([]error, len(calls))
	for i := range calls {
		calls[i].JSONRPC = "2.0"
		calls[i].ID = i
		errs[i] = fmt.Errorf("rpc: no response for %s", calls[i].Method)
	}
	for lo := 0; lo < len(calls); lo += maxBatch {
		chunk := calls[lo:min(lo+maxBatch, len(calls))]
		var rs []rpcResp
		err := retry(ctx, func() error {
			var err error
			rs, err = c.post(ctx, chunk)
			return err
		})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rs {
			if r.ID < lo || r.ID >= lo+len(chunk) {
				continue
			}
			if r.Error != nil {
				errs[r.ID] = fmt.Errorf("rpc: %s: %s", calls[r.ID].Method, r.Error.Message)
				continue
			}
			results[r.ID], errs[r.ID] = r.Result, nil
		}
	}
	return results, errs, nil
}

func (c *Client) post(ctx context.Context, calls []rpcReq) ([]rpcResp, error) {
	body, _ := json.Marshal(calls)
	req, err := http.NewRequestWithContext(ctx, "POST", c.RPC, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, transient{err}
	}
	defer resp.Body.Close()
	if err := statusErr("rpc", resp); err != nil {
		return nil, err
	}
	var rs []rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
		return nil, fmt.Errorf("rpc: %w", err)
	}
	return rs, nil
}

// transient marks an error worth retrying: a dropped connection, a rate
// limit, or a server error.
type transient struct{ error }

func (t transient) Unwrap() error { return t.error }

func statusErr(what string, resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	err := fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(b)))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return transient{err}
	}
	return err
}

// retry runs fn, retrying transient failures with backoff (1s, 2s, 4s).
func retry(ctx context.Context, fn func() error) error {
	wait := time.Second
	for attempt := 0; ; attempt++ {
		err := fn()
		var t transient
		if err == nil || attempt == 3 || !errors.As(err, &t) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func ethCall(to, data string) rpcReq {
	return rpcReq{Method: "eth_call", Params: []any{map[string]string{"to": to, "data": data}, "latest"}}
}

// words decodes an eth_call result into its 32-byte ABI words.
func words(raw json.RawMessage) ([][]byte, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b)%32 != 0 {
		return nil, fmt.Errorf("unexpected return data length %d", len(b))
	}
	var w [][]byte
	for i := 0; i < len(b); i += 32 {
		w = append(w, b[i:i+32])
	}
	return w, nil
}

// signed interprets a word as a two's-complement int256.
func signed(w []byte) *big.Int {
	n := new(big.Int).SetBytes(w)
	if w[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	return n
}

// scaled returns n / 10^dec as a float.
func scaled(n *big.Int, dec int) float64 {
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(n), new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil))).Float64()
	return f
}

func hexUint(raw json.RawMessage) (*big.Int, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("bad hex quantity %q", s)
	}
	return n, nil
}

// Snapshot is one consistent read of the chain for a set of tokens and
// feeds.
type Snapshot struct {
	ChainID uint64
	Head    Head
	Rounds  map[string]Round      // by feed proxy address
	Tokens  map[string]TokenState // by token address
	Errors  map[string]error      // per address, for reads that failed
}

// Read fetches the chain head plus the latest round of each feed and
// the state of each token, in a single RPC batch.
func (c *Client) Read(ctx context.Context, feeds []Feed, tokens []string) (*Snapshot, error) {
	calls := []rpcReq{
		{Method: "eth_chainId", Params: []any{}},
		{Method: "eth_getBlockByNumber", Params: []any{"latest", false}},
	}
	for _, f := range feeds {
		calls = append(calls, ethCall(f.Proxy, selLatestRoundData))
	}
	for _, t := range tokens {
		calls = append(calls, ethCall(t, selUIMultiplier), ethCall(t, selOraclePaused))
	}
	res, errs, err := c.batch(ctx, calls)
	if err != nil {
		return nil, err
	}
	if errs[0] != nil {
		return nil, errs[0]
	}
	if errs[1] != nil {
		return nil, errs[1]
	}

	s := &Snapshot{
		Rounds: make(map[string]Round),
		Tokens: make(map[string]TokenState),
		Errors: make(map[string]error),
	}
	id, err := hexUint(res[0])
	if err != nil {
		return nil, err
	}
	s.ChainID = id.Uint64()
	var blk struct {
		Number        string `json:"number"`
		Timestamp     string `json:"timestamp"`
		BaseFeePerGas string `json:"baseFeePerGas"`
	}
	if err := json.Unmarshal(res[1], &blk); err != nil {
		return nil, fmt.Errorf("rpc: block: %w", err)
	}
	num, _ := strconv.ParseUint(strings.TrimPrefix(blk.Number, "0x"), 16, 64)
	ts, _ := strconv.ParseInt(strings.TrimPrefix(blk.Timestamp, "0x"), 16, 64)
	fee, _ := new(big.Int).SetString(strings.TrimPrefix(blk.BaseFeePerGas, "0x"), 16)
	s.Head = Head{Number: num, Time: time.Unix(ts, 0), BaseFee: fee}

	i := 2
	for _, f := range feeds {
		r, err := decodeRound(res[i], errs[i], f.Decimals)
		if err != nil {
			s.Errors[f.Proxy] = err
		} else {
			s.Rounds[f.Proxy] = r
		}
		i++
	}
	for _, t := range tokens {
		ts, err := decodeToken(res[i], errs[i], res[i+1], errs[i+1])
		if err != nil {
			s.Errors[t] = err
		} else {
			s.Tokens[t] = ts
		}
		i += 2
	}
	return s, nil
}

func decodeRound(raw json.RawMessage, callErr error, decimals int) (Round, error) {
	if callErr != nil {
		return Round{}, callErr
	}
	w, err := words(raw)
	if err != nil {
		return Round{}, err
	}
	if len(w) < 5 {
		return Round{}, fmt.Errorf("latestRoundData: short return")
	}
	answer := signed(w[1])
	if answer.Sign() <= 0 {
		return Round{}, fmt.Errorf("latestRoundData: non-positive answer")
	}
	updated := new(big.Int).SetBytes(w[3]).Int64()
	return Round{Price: scaled(answer, decimals), UpdatedAt: time.Unix(updated, 0)}, nil
}

func decodeToken(mRaw json.RawMessage, mErr error, pRaw json.RawMessage, pErr error) (TokenState, error) {
	if mErr != nil {
		return TokenState{}, mErr
	}
	mw, err := words(mRaw)
	if err != nil {
		return TokenState{}, err
	}
	ts := TokenState{Multiplier: scaled(new(big.Int).SetBytes(mw[0]), 18)}
	// oraclePaused is advisory and may be absent on older tokens; treat a
	// failed read as "not paused" rather than failing the whole token.
	if pErr == nil {
		if pw, err := words(pRaw); err == nil {
			ts.OraclePaused = new(big.Int).SetBytes(pw[0]).Sign() != 0
		}
	}
	return ts, nil
}
