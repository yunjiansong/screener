// Package alerts watches the companies on the watchlist: it re-prices them from fresh daily
// quotes against the latest screen's fundamentals, checks the alert rules, and emails a digest
// of rules that newly became true. Alerts are edge-triggered: one email when a condition starts
// holding, none while it keeps holding, and it re-arms once the condition clears.
package alerts

import (
	"fmt"
	"math"
)

// Rules are thresholds; nil (or false) disables a rule. Per-company overrides are merged over
// the defaults field by field.
type Rules struct {
	PriceBelow *float64 `json:"priceBelow,omitempty"` // price ≤ (your buy zone)
	PriceAbove *float64 `json:"priceAbove,omitempty"` // price ≥ (take another look / trim)
	DailyDrop  *float64 `json:"dailyDrop,omitempty"`  // one-session return ≤ (e.g. -0.07)
	Drawdown   *float64 `json:"drawdown,omitempty"`   // from the 5-year high ≤ (e.g. -0.5)
	FCFYield   *float64 `json:"fcfYield,omitempty"`   // normalized FCF yield ≥
	Multiple   *float64 `json:"multiple,omitempty"`   // P/FCF ≤
	Upside     *float64 `json:"upside,omitempty"`     // re-rating upside to the median multiple ≥
	NewsMove   *float64 `json:"newsMove,omitempty"`   // news whose first session moved |return| ≥
	StageDrop  *bool    `json:"stageDrop,omitempty"`  // fell out of the layer it was in when added
	NewQuarter *bool    `json:"newQuarter,omitempty"` // a new 10-Q / annual report changed the data basis
	Review     *bool    `json:"review,omitempty"`     // your "下次复查" date has arrived
}

func ptr[T any](v T) *T { return &v }

func DefaultRules() Rules {
	return Rules{DailyDrop: ptr(-0.07), NewsMove: ptr(0.08), StageDrop: ptr(true), NewQuarter: ptr(true), Review: ptr(true)}
}

// Merge overlays o's set fields on r. A per-company override of 0 / false turns a default off.
func (r Rules) Merge(o Rules) Rules {
	pick := func(a, b *float64) *float64 {
		if b != nil {
			if *b == 0 {
				return nil
			}
			return b
		}
		return a
	}
	pickB := func(a, b *bool) *bool {
		if b != nil {
			return b
		}
		return a
	}
	return Rules{
		PriceBelow: pick(r.PriceBelow, o.PriceBelow), PriceAbove: pick(r.PriceAbove, o.PriceAbove),
		DailyDrop: pick(r.DailyDrop, o.DailyDrop), Drawdown: pick(r.Drawdown, o.Drawdown),
		FCFYield: pick(r.FCFYield, o.FCFYield), Multiple: pick(r.Multiple, o.Multiple), Upside: pick(r.Upside, o.Upside),
		NewsMove: pick(r.NewsMove, o.NewsMove), StageDrop: pickB(r.StageDrop, o.StageDrop),
		NewQuarter: pickB(r.NewQuarter, o.NewQuarter), Review: pickB(r.Review, o.Review),
	}
}

func on(b *bool) bool { return b != nil && *b }

// Snapshot is a company re-priced at the latest quote.
type Snapshot struct {
	AsOf       string   `json:"asOf"` // date of the latest daily bar
	Price      float64  `json:"price"`
	DayChange  *float64 `json:"dayChange"`
	High5y     float64  `json:"high5y"`
	Drawdown   float64  `json:"drawdown"`
	MarketCap  float64  `json:"marketCap"`
	Multiple   *float64 `json:"multiple"`
	FCFYield   *float64 `json:"fcfYield"`
	Percentile *float64 `json:"percentile"`
	Upside     *float64 `json:"upside"`
	Stage      int      `json:"stage"`
	Basis      string   `json:"basis"`
	ReviewOn   string   `json:"reviewOn,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// Condition is one rule's verdict for this scan. Key identifies the edge-trigger state;
// Value is stored with it (used by rules that fire on change rather than on level).
type Condition struct {
	Key    string
	Label  string // rule name for the log / email
	Active bool
	Text   string
	Value  string
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

// levelConditions evaluates the threshold rules against a snapshot.
func levelConditions(r Rules, s Snapshot) []Condition {
	var out []Condition
	add := func(key, label string, set bool, active bool, text string) {
		if set {
			out = append(out, Condition{Key: key, Label: label, Active: active, Text: text})
		}
	}
	if r.PriceBelow != nil {
		add("price_below", "价格下穿", true, s.Price <= *r.PriceBelow, fmt.Sprintf("股价 $%.2f ≤ 你设的 $%.2f", s.Price, *r.PriceBelow))
	}
	if r.PriceAbove != nil {
		add("price_above", "价格上穿", true, s.Price >= *r.PriceAbove, fmt.Sprintf("股价 $%.2f ≥ 你设的 $%.2f", s.Price, *r.PriceAbove))
	}
	if r.DailyDrop != nil && s.DayChange != nil {
		add("daily_drop", "单日大跌", true, *s.DayChange <= *r.DailyDrop, fmt.Sprintf("%s 单日 %s（阈值 %s）", s.AsOf, pct(*s.DayChange), pct(*r.DailyDrop)))
	}
	if r.Drawdown != nil {
		add("drawdown", "回撤", true, s.Drawdown <= *r.Drawdown, fmt.Sprintf("较5年高点 %s（阈值 %s）", pct(s.Drawdown), pct(*r.Drawdown)))
	}
	if r.FCFYield != nil && s.FCFYield != nil {
		add("fcf_yield", "FCF 收益率", true, *s.FCFYield >= *r.FCFYield, fmt.Sprintf("正常化 FCF 收益率 %s ≥ %s", pct(*s.FCFYield), pct(*r.FCFYield)))
	}
	if r.Multiple != nil && s.Multiple != nil {
		add("multiple", "P/FCF", true, *s.Multiple <= *r.Multiple, fmt.Sprintf("P/FCF %.1fx ≤ %.1fx", *s.Multiple, *r.Multiple))
	}
	if r.Upside != nil && s.Upside != nil {
		add("upside", "上行空间", true, *s.Upside >= *r.Upside, fmt.Sprintf("估值修复上行 %s ≥ %s", pct(*s.Upside), pct(*r.Upside)))
	}
	return out
}

// rePrice scales the screen's valuation to a new price (market cap moves with the price;
// normalized FCF and the multiple history stay as of the screen).
func rePrice(price, screenPrice, screenMcap, normFCF, high5y, maxTarget float64, median *float64, history []float64) (mcap float64, multiple, yield, percentile, upside *float64, drawdown float64) {
	mcap = screenMcap * price / screenPrice
	high := math.Max(high5y, price)
	drawdown = price/high - 1
	if normFCF <= 0 {
		return
	}
	m := mcap / normFCF
	y := normFCF / mcap
	multiple, yield = &m, &y
	if len(history) > 0 {
		below := 0
		for _, h := range history {
			if h <= m {
				below++
			}
		}
		p := float64(below) / float64(len(history))
		percentile = &p
	}
	if median != nil {
		u := math.Min(*median, maxTarget)/m - 1
		upside = &u
	}
	return
}
