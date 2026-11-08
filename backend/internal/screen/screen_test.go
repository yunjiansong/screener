package screen

import (
	"math"
	"strings"
	"testing"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/model"
	"stockscreener/internal/yahoo"
)

// A steady, cash-rich company whose price halves in the last year: it should pass all layers,
// with the drawdown, multiple percentile and capped upside computed exactly.
func TestSteadyCompanyAfterSelloff(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	c := &model.Company{CIK: 1, Ticker: "TEST", Currency: "USD"}
	for y := 2016; y <= 2025; y++ {
		c.Years = append(c.Years, model.Year{End: time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Revenue: model.F(1000), GrossProfit: model.F(600), OpIncome: model.F(250), OCF: model.F(220), Capex: model.F(20),
			FCF: model.F(200), Cash: model.F(500), Debt: model.F(100), Equity: model.F(800), Shares: model.F(100)})
	}
	// Monthly bars: flat at 100 for ~9 years, then 50 for the last 12 months.
	var bars []yahoo.Bar
	start := now.AddDate(-10, 0, 0)
	for d := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC); !d.After(now); d = d.AddDate(0, 1, 0) {
		price := 100.0
		if d.After(now.AddDate(-1, 0, 0)) {
			price = 50
		}
		bars = append(bars, yahoo.Bar{Date: d.Format(time.DateOnly), Close: price})
	}
	m := &Market{
		Chart:     &yahoo.Chart{Currency: "USD", Price: 50, Bars: bars},
		MarketCap: &yahoo.MarketCap{AsOf: "2026-09-30", Value: 2500}, // 50 shares × $50
		Profile:   &yahoo.Profile{Sector: "Technology", QuoteType: "EQUITY"},
	}
	cfg := config.Default()
	cfg.Dislocation.MinMarketCapUSD = 0

	e := Evaluate(c, m, cfg, now)
	for _, ch := range e.Checks {
		if !ch.Pass {
			t.Errorf("unexpected failure L%d %s: %s (rule %s)", ch.Layer, ch.Name, ch.Value, ch.Rule)
		}
	}
	if e.Stage != 3 {
		t.Fatalf("stage = %d, want 3", e.Stage)
	}
	v := e.Valuation
	approx(t, "drawdown", v.Drawdown, -0.5)
	approx(t, "multiple", *v.Multiple, 12.5) // 2500 / 200
	approx(t, "median", *v.Median, 25)       // most history at price 100
	approx(t, "target", *v.Target, 25)       // median, under the 30x cap
	approx(t, "upside", *v.Upside, 1.0)      // 25 / 12.5 - 1
	approx(t, "downside", *v.Downside, -0.2) // multiple holds at 12.5, FCF -20%
	approx(t, "convexity", *v.Convexity, 5)
	approx(t, "fcf yield", *v.FCFYield, 0.08)
	if *v.Percentile > 0.15 {
		t.Errorf("percentile = %.2f, want near the bottom of history", *v.Percentile)
	}
	if e.Quality.Score < 80 {
		t.Errorf("quality score = %.0f, want high for a steady net-cash business", e.Quality.Score)
	}
}

// A company whose latest year of FCF collapses must fail "fundamentals intact" even though
// its 3-year average still looks fine.
func TestLatestFCFCollapseFailsLayer2(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	c := &model.Company{CIK: 2, Ticker: "DROP", Currency: "USD"}
	for y := 2016; y <= 2025; y++ {
		fcf := 200.0
		if y == 2025 {
			fcf = 40
		}
		c.Years = append(c.Years, model.Year{End: time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Revenue: model.F(1000), OpIncome: model.F(250), OCF: model.F(fcf), Capex: model.F(0), FCF: model.F(fcf),
			Cash: model.F(500), Debt: model.F(0), Equity: model.F(800)})
	}
	var bars []yahoo.Bar
	for d := time.Date(2016, 12, 1, 0, 0, 0, 0, time.UTC); !d.After(now); d = d.AddDate(0, 1, 0) {
		bars = append(bars, yahoo.Bar{Date: d.Format(time.DateOnly), Close: 100})
	}
	m := &Market{Chart: &yahoo.Chart{Price: 40, Bars: bars}, MarketCap: &yahoo.MarketCap{AsOf: "2026-09-30", Value: 4000},
		Profile: &yahoo.Profile{Sector: "Industrials", QuoteType: "EQUITY"}}
	e := Evaluate(c, m, config.Default(), now)
	for _, ch := range e.Checks {
		if ch.Name == "基本面未坏: 最新年FCF" && ch.Pass {
			t.Errorf("latest-year FCF collapse passed: %s", ch.Value)
		}
	}
	if e.Stage >= 2 {
		t.Errorf("stage = %d, want < 2", e.Stage)
	}
}

// The annual report looks fine, but the quarters since show FCF collapsing: TTM must drive
// "latest" checks and normalized FCF, so layer 2 catches it before the next 10-K.
func TestTTMOverridesStaleAnnual(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	c := &model.Company{CIK: 1, Ticker: "TEST", Currency: "USD"}
	for y := 2016; y <= 2025; y++ {
		c.Years = append(c.Years, model.Year{End: time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Revenue: model.F(1000), GrossProfit: model.F(600), OpIncome: model.F(250), OCF: model.F(220), Capex: model.F(20),
			FCF: model.F(200), Cash: model.F(500), Debt: model.F(100), Equity: model.F(800), Shares: model.F(100)})
	}
	for i, fcf := range []float64{200, 200, 40} {
		c.TTM = append(c.TTM, model.Year{End: time.Date(2024+i, 9, 30, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), TTM: true,
			Revenue: model.F(1000), GrossProfit: model.F(600), OpIncome: model.F(250), OCF: model.F(fcf + 20), Capex: model.F(20),
			FCF: model.F(fcf), Cash: model.F(500), Debt: model.F(100), Equity: model.F(800), Shares: model.F(100)})
	}
	var bars []yahoo.Bar
	for d := time.Date(2016, 11, 1, 0, 0, 0, 0, time.UTC); !d.After(now); d = d.AddDate(0, 1, 0) {
		price := 100.0
		if d.After(now.AddDate(-1, 0, 0)) {
			price = 50
		}
		bars = append(bars, yahoo.Bar{Date: d.Format(time.DateOnly), Close: price})
	}
	m := &Market{Chart: &yahoo.Chart{Currency: "USD", Price: 50, Bars: bars},
		MarketCap: &yahoo.MarketCap{AsOf: "2026-09-30", Value: 2500}, Profile: &yahoo.Profile{Sector: "Technology", QuoteType: "EQUITY"}}
	cfg := config.Default()
	cfg.Dislocation.MinMarketCapUSD = 0
	e := Evaluate(c, m, cfg, now)
	v := e.Valuation
	approx(t, "normalized FCF", v.NormFCFUSD, 440.0/3)
	approx(t, "latest vs avg", *v.LatestFCFvsAvg, 40/(440.0/3))
	if v.Basis != "TTM 截至 2026-09-30" {
		t.Errorf("basis = %q", v.Basis)
	}
	if e.Stage >= 2 {
		t.Errorf("stage = %d: TTM FCF collapse should fail layer 2", e.Stage)
	}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %.4f, want %.4f", name, got, want)
	}
}

func TestEvalFalsify(t *testing.T) {
	m := map[string]float64{"grossMargin": 0.548, "price": 120}
	hits := EvalFalsify([]FalsifyRule{{Metric: "grossMargin", Op: "<", Value: 0.55}, {Metric: "price", Op: "<", Value: 100}, {Metric: "fscore", Op: "<", Value: 4}}, m)
	if len(hits) != 1 || hits[0].Text != "毛利率 54.8%，跌破你设的 55.0%" {
		t.Fatalf("%+v", hits)
	}
}

func TestRecentWithSingleTTM(t *testing.T) {
	f := model.F
	c := &model.Company{Years: []model.Year{{End: "2023-12-31", FCF: f(1)}, {End: "2024-12-31", FCF: f(2)}, {End: "2025-12-31", FCF: f(3)}},
		TTM: []model.Year{{End: "2026-06-30", TTM: true, FCF: f(4)}}}
	r := c.Recent()
	if len(r) != 3 || r[0].End != "2023-12-31" || r[1].End != "2024-12-31" || r[2].End != "2026-06-30" {
		t.Fatalf("%+v", r)
	}
}

// Fast growth that stalls with a squeezed gross margin: the gross-margin and revenue trend
// rules warn, the steady operating / FCF margins don't, and none of them fails L1.
func TestTrendChecks(t *testing.T) {
	c := &model.Company{CIK: 1, Ticker: "T", Currency: "USD"}
	rev := []float64{100, 150, 225, 340, 500, 520} // ~50% a year, then +4%
	for i, r := range rev {
		gm := 0.6
		if i == len(rev)-1 {
			gm = 0.5
		}
		c.Years = append(c.Years, model.Year{End: time.Date(2020+i, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Revenue: model.F(r), GrossProfit: model.F(r * gm), OpIncome: model.F(r * 0.2), FCF: model.F(r * 0.15),
			Cash: model.F(100), Debt: model.F(0), Equity: model.F(300), Shares: model.F(10)})
	}
	_, checks := quality(c, config.Default().Quality, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	warn := map[string]bool{}
	for _, ch := range checks {
		if after, ok := strings.CutPrefix(ch.Name, "趋势："); ok && ch.Pass {
			warn["趋势："+after] = ch.Warn
		}
	}
	want := map[string]bool{
		"趋势：最近 12 个月 毛利率 − 5 年中位":     true,
		"趋势：最近一年收入增速 vs 5 年年均":        true,
		"趋势：最近 12 个月 营业利润率 − 5 年中位":   false,
		"趋势：最近 12 个月 FCF 利润率 − 5 年中位": false,
	}
	for k, v := range want {
		got, ok := warn[k]
		if !ok || got != v {
			t.Errorf("%s: warn=%v (present %v), want %v; all %v", k, got, ok, v, warn)
		}
	}
}

// A CNY reporter: today's valuation converts at the latest quote, not the average around the
// latest fiscal year; the P/FCF history keeps each year's own rate.
func TestTodayUsesLatestFX(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	c := &model.Company{CIK: 1, Ticker: "CN", Currency: "CNY"}
	for y := 2016; y <= 2025; y++ {
		c.Years = append(c.Years, model.Year{End: time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Revenue: model.F(7000), GrossProfit: model.F(4200), OpIncome: model.F(1750), OCF: model.F(1540), Capex: model.F(140),
			FCF: model.F(1400), Cash: model.F(3500), Debt: model.F(700), Equity: model.F(5600), Shares: model.F(100)})
	}
	var bars, fx []yahoo.Bar
	start := now.AddDate(-10, 0, 0)
	for d := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC); !d.After(now); d = d.AddDate(0, 1, 0) {
		bars = append(bars, yahoo.Bar{Date: d.Format(time.DateOnly), Close: 50})
		fx = append(fx, yahoo.Bar{Date: d.Format(time.DateOnly), Close: 7.0})
	}
	m := &Market{
		Chart:     &yahoo.Chart{Currency: "USD", Price: 50, Bars: bars},
		MarketCap: &yahoo.MarketCap{AsOf: "2026-09-30", Value: 2500},
		Profile:   &yahoo.Profile{Sector: "Technology", QuoteType: "EQUITY"},
		FX:        fx,
		FXSpot:    6.5,
	}
	cfg := config.Default()
	cfg.Dislocation.MinMarketCapUSD = 0
	e := Evaluate(c, m, cfg, now)
	approx(t, "fx", e.FX, 6.5)
	approx(t, "normalized FCF", e.Valuation.NormFCFUSD, 1400/6.5)
	approx(t, "FCF change since high", *e.Valuation.FCFChangeSinceHigh, 0) // flat in CNY
	if e.Analysis.ReverseDCF != nil {
		approx(t, "EV", e.Analysis.ReverseDCF.EVUSD, 2500+(700-3500)/6.5)
	}
}

// The quality-screen ("去劣") warnings: thin interest cover, profit that doesn't turn into
// cash and a low gross margin warn, but never fail L1.
func TestWeedChecks(t *testing.T) {
	c := &model.Company{CIK: 1, Ticker: "W", Currency: "USD"}
	for i := range 6 {
		c.Years = append(c.Years, model.Year{End: time.Date(2020+i, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Revenue: model.F(1000), GrossProfit: model.F(120), OpIncome: model.F(90), NetIncome: model.F(60), OCF: model.F(36),
			Interest: model.F(60), FCF: model.F(60), Cash: model.F(100), Debt: model.F(50), Equity: model.F(500), Shares: model.F(10)})
	}
	q, checks := quality(c, config.Default().Quality, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	got := map[string]bool{}
	for _, ch := range checks {
		if strings.HasPrefix(ch.Name, "去劣：") {
			if !ch.Pass {
				t.Errorf("%s failed L1; it should only warn", ch.Name)
			}
			got[ch.Name] = ch.Warn
		}
	}
	for _, k := range []string{"去劣：利息覆盖（营业利润 ÷ 利息，近 3 期中位）", "去劣：5 年经营现金流 ÷ 净利润", "去劣：毛利率（5 年中位）"} {
		if !got[k] {
			t.Errorf("%s: want a warning; got %v", k, got)
		}
	}
	if q.InterestCover == nil || *q.InterestCover != 1.5 || q.OCFToNI == nil || *q.OCFToNI != 0.6 {
		t.Errorf("interestCover %v, ocfToNI %v", q.InterestCover, q.OCFToNI)
	}
	// One impaired year doesn't make the cover a warning: the median of the last three is used.
	c.Years[5].OpIncome = model.F(-200)
	for i := range 5 {
		c.Years[i].OpIncome = model.F(300)
	}
	if v := interestCover(c); v == nil || *v != 5 {
		t.Errorf("interestCover with one impaired year = %v, want 5", v)
	}
	// Losses overall: cash conversion is unknown rather than a warning.
	for i := range c.Years {
		c.Years[i].NetIncome = model.F(-10)
	}
	if v := ocfToNI(lastN(c.Years, 5)); v != nil {
		t.Errorf("ocfToNI with losses = %v, want nil", *v)
	}
}
