package alerts

import (
	"math"
	"strings"
	"testing"
)

func TestMerge(t *testing.T) {
	r := DefaultRules().Merge(Rules{PriceBelow: ptr(90.0), DailyDrop: ptr(0.0), Review: ptr(false)})
	if r.PriceBelow == nil || *r.PriceBelow != 90 {
		t.Error("override not applied")
	}
	if r.DailyDrop != nil {
		t.Error("0 should turn a default off")
	}
	if on(r.Review) || !on(r.StageDrop) || r.NewsMove == nil {
		t.Errorf("bools / untouched defaults wrong: %+v", r)
	}
}

func TestRePrice(t *testing.T) {
	// Screened at $100 with $10B cap and $1B normalized FCF (10x); price falls to $80.
	mcap, mult, yield, pctl, up, dd := rePrice(80, 100, 10e9, 1e9, 160, 30, ptr(20.0), []float64{8, 9, 12, 20, 25})
	near := func(name string, got, want float64) {
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	near("mcap", mcap, 8e9)
	near("multiple", *mult, 8)
	near("yield", *yield, 0.125)
	near("percentile", *pctl, 0.2) // 8 ≤ 8
	near("upside", *up, 1.5)       // 20/8 - 1
	near("drawdown", dd, -0.5)
}

func TestLevelConditions(t *testing.T) {
	s := Snapshot{Price: 80, DayChange: ptr(-0.09), Drawdown: -0.5, FCFYield: ptr(0.12)}
	cs := levelConditions(Rules{PriceBelow: ptr(85.0), DailyDrop: ptr(-0.07), FCFYield: ptr(0.15)}, s)
	got := map[string]bool{}
	for _, c := range cs {
		got[c.Key] = c.Active
	}
	if !got["price_below"] || !got["daily_drop"] || got["fcf_yield"] {
		t.Fatalf("got %v", got)
	}
}

func TestDigestHTML(t *testing.T) {
	d := Digest{BaseURL: "http://x", Items: []DigestItem{{Ticker: "LULU", Name: "lululemon", Fired: []Condition{{Label: "单日大跌", Text: "-9%"}}, Snap: Snapshot{Price: 1}}}}
	h, err := d.HTML()
	if err != nil || !strings.Contains(h, "http://x/#/c/LULU") || !strings.Contains(h, "单日大跌") {
		t.Fatalf("err=%v html=%s", err, h[:200])
	}
	if !strings.Contains(d.Subject(), "LULU") {
		t.Error(d.Subject())
	}
}
