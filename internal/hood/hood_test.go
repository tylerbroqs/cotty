package hood

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFeedSymbol(t *testing.T) {
	cases := map[string]string{
		"Robinhood TSLA / USD":           "TSLA",
		"Robinhood DELL-USD":             "DELL",
		"ETH / USD":                      "ETH",
		"WSTETH / STETH Exchange Rate":   "",
		"SYRUPUSDC / USDC Exchange Rate": "",
	}
	for name, want := range cases {
		got, ok := feedSymbol(name)
		if (want == "") == ok || got != want {
			t.Errorf("feedSymbol(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
}

func TestDecodeRound(t *testing.T) {
	// A real TSLA / USD latestRoundData() return from Robinhood Chain.
	raw, _ := json.Marshal("0x" +
		"000000000000000000000000000000000000000000000001000000000000063e" +
		"0000000000000000000000000000000000000000000000000000000884fde72c" +
		"000000000000000000000000000000000000000000000000000000006ad6f72b" +
		"000000000000000000000000000000000000000000000000000000006ad6f737" +
		"000000000000000000000000000000000000000000000001000000000000063e")
	r, err := decodeRound(raw, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Price != 365.90970668 {
		t.Errorf("price = %v, want 365.90970668", r.Price)
	}
	if want := time.Unix(0x6ad6f737, 0); !r.UpdatedAt.Equal(want) {
		t.Errorf("updatedAt = %v, want %v", r.UpdatedAt, want)
	}
}

func TestDecodeRoundRejectsNegative(t *testing.T) {
	neg := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	zero := "0000000000000000000000000000000000000000000000000000000000000000"
	raw, _ := json.Marshal("0x" + zero + neg + zero + zero + zero)
	if _, err := decodeRound(raw, nil, 8); err == nil {
		t.Error("negative answer accepted")
	}
}

func TestDecodeToken(t *testing.T) {
	mult, _ := json.Marshal("0x0000000000000000000000000000000000000000000000000e92596fd6290000") // 1.05e18
	paused, _ := json.Marshal("0x0000000000000000000000000000000000000000000000000000000000000001")
	ts, err := decodeToken(mult, nil, paused, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Multiplier < 1.0499 || ts.Multiplier > 1.0501 || !ts.OraclePaused {
		t.Errorf("got %+v, want multiplier 1.05, paused", ts)
	}
}
