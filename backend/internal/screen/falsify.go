package screen

import (
	"fmt"
	"math"
)

// Quantified falsification conditions: "if gross margin falls below 55%, my thesis is wrong".
// The person writes them in the judgement panel; they are re-checked whenever the data or
// the price changes (list page, alert scans).

type FalsifyRule struct {
	Metric string  `json:"metric"` // key of FalsifyMetrics
	Op     string  `json:"op"`     // < | <= | > | >=
	Value  float64 `json:"value"`  // ratios as fractions (0.55 = 55%)
	Note   string  `json:"note,omitempty"`
}

type FalsifyHit struct {
	Rule   FalsifyRule `json:"rule"`
	Actual float64     `json:"actual"`
	Text   string      `json:"text"` // e.g. 毛利率 54.8% 跌破你设的 55%
}

type MetricDef struct {
	Key, Label, Format string // format: pct | x | usd | num
}

// FalsifyMetricDefs lists what a condition can refer to, in display order.
var FalsifyMetricDefs = []MetricDef{
	{"grossMargin", "毛利率", "pct"},
	{"opMargin", "营业利润率", "pct"},
	{"fcfMargin", "FCF 利润率", "pct"},
	{"revenueGrowth", "收入同比", "pct"},
	{"fcfVsAvg", "最新 FCF / 近期均值", "pct"},
	{"price", "股价", "usd"},
	{"drawdown", "较 5 年高点", "pct"},
	{"fcfYield", "FCF 收益率", "pct"},
	{"percentile", "估值历史分位", "pct"},
	{"fscore", "Piotroski F 分数", "num"},
}

func metricDef(k string) MetricDef {
	for _, d := range FalsifyMetricDefs {
		if d.Key == k {
			return d
		}
	}
	return MetricDef{k, k, "num"}
}

func fmtMetric(format string, v float64) string {
	switch format {
	case "pct":
		return fmt.Sprintf("%.1f%%", v*100)
	case "usd":
		return fmt.Sprintf("$%.2f", v)
	case "x":
		return fmt.Sprintf("%.1fx", v)
	}
	return fmt.Sprintf("%g", math.Round(v*100)/100)
}

// FalsifyMetrics computes the current values; livePrice (when > 0) replaces the screen-time
// price and re-scales the price-based ratios.
func FalsifyMetrics(e *Evaluation, livePrice float64) map[string]float64 {
	m := map[string]float64{}
	ys := e.Company.Recent()
	if len(ys) == 0 {
		return m
	}
	last := ys[len(ys)-1]
	if last.Revenue != nil && *last.Revenue > 0 {
		r := *last.Revenue
		if last.GrossProfit != nil {
			m["grossMargin"] = *last.GrossProfit / r
		}
		if last.OpIncome != nil {
			m["opMargin"] = *last.OpIncome / r
		}
		if last.FCF != nil {
			m["fcfMargin"] = *last.FCF / r
		}
		if n := len(ys); n >= 2 && ys[n-2].Revenue != nil && *ys[n-2].Revenue > 0 {
			m["revenueGrowth"] = r / *ys[n-2].Revenue - 1
		}
	}
	if v := e.Valuation; v != nil {
		price, scale := v.Price, 1.0
		if livePrice > 0 && v.Price > 0 {
			price, scale = livePrice, livePrice/v.Price
		}
		m["price"] = price
		if v.High5y > 0 {
			m["drawdown"] = price/math.Max(v.High5y, price) - 1
		}
		if v.FCFYield != nil {
			m["fcfYield"] = *v.FCFYield / scale
		}
		if v.Percentile != nil {
			m["percentile"] = *v.Percentile // historical rank: not re-ranked on live price
		}
		if v.LatestFCFvsAvg != nil {
			m["fcfVsAvg"] = *v.LatestFCFvsAvg
		}
	}
	if a := e.Analysis; a != nil && a.FScore != nil {
		m["fscore"] = float64(a.FScore.Value)
	}
	return m
}

var opText = map[string]string{"<": "跌破", "<=": "跌到", ">": "升破", ">=": "升到"}

// EvalFalsify returns the rules that currently hold (i.e. the thesis is contradicted).
func EvalFalsify(rules []FalsifyRule, m map[string]float64) []FalsifyHit {
	var hits []FalsifyHit
	for _, r := range rules {
		v, ok := m[r.Metric]
		if !ok {
			continue
		}
		var hit bool
		switch r.Op {
		case "<":
			hit = v < r.Value
		case "<=":
			hit = v <= r.Value
		case ">":
			hit = v > r.Value
		case ">=":
			hit = v >= r.Value
		}
		if hit {
			d := metricDef(r.Metric)
			hits = append(hits, FalsifyHit{r, v, fmt.Sprintf("%s %s，%s你设的 %s", d.Label, fmtMetric(d.Format, v), opText[r.Op], fmtMetric(d.Format, r.Value))})
		}
	}
	return hits
}

func ValidFalsifyRule(r FalsifyRule) bool {
	if _, ok := opText[r.Op]; !ok || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
		return false
	}
	for _, d := range FalsifyMetricDefs {
		if d.Key == r.Metric {
			return true
		}
	}
	return false
}
