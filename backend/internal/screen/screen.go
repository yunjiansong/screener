// Package screen implements the four-layer "convex mispricing" funnel:
//
//	Layer 1 quality     – durable cash generation, margins/ROIC as moat footprints, sane balance sheet
//	Layer 2 dislocation – price far below its 5y high and cheap vs its own history, fundamentals intact
//	Layer 3 convexity   – re-rating upside to the median multiple vs downside to the 10th percentile
//	Layer 4 judgement   – external vs internal cause; the system only offers hints, the human decides
package screen

import (
	"fmt"
	"math"
	"slices"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/model"
	"stockscreener/internal/yahoo"
)

const taxRate = 0.21

type Check struct {
	Layer int    `json:"layer"`
	Name  string `json:"name"`
	Pass  bool   `json:"pass"`
	Value string `json:"value"`
	Rule  string `json:"rule"`
	Near  bool   `json:"near,omitempty"` // passed, but close to the threshold
	Warn  bool   `json:"warn,omitempty"` // a trend warning: never fails the layer, only flags it
}

// nearMargin: a passing value counts as borderline when it sits within this fraction
// of the threshold (40% of |threshold|).
const nearMargin = 0.4

// near flags a passing value close to its threshold (min-type when atLeast).
func near(v *float64, threshold float64, atLeast bool) bool {
	return nearIn(v, threshold, math.Abs(threshold), atLeast)
}

// nearIn is near for bounded ratios, where the margin is a share of the room between the
// threshold and the natural bound (e.g. "≥ 90% of the 3y high" has only 10 points of room).
func nearIn(v *float64, threshold, room float64, atLeast bool) bool {
	if v == nil {
		return false
	}
	m := room * nearMargin
	if atLeast {
		return *v >= threshold && *v < threshold+m
	}
	return *v <= threshold && *v > threshold-m
}

type Hint struct {
	Kind string `json:"kind"` // external | internal | positive
	Text string `json:"text"`
}

type Quality struct {
	Years          int      `json:"years"`
	FCFPositive5   int      `json:"fcfPositive5"`
	FCFMargin      *float64 `json:"fcfMargin"`
	GrossMargin    *float64 `json:"grossMargin"`
	GrossMarginStd *float64 `json:"grossMarginStd"`
	OpMargin       *float64 `json:"opMargin"`
	OpMarginStd    *float64 `json:"opMarginStd"`
	ROIC           *float64 `json:"roic"`
	RevenueCAGR    *float64 `json:"revenueCagr"`
	NetDebtToFCF   *float64 `json:"netDebtToFcf"`
	NetCash        bool     `json:"netCash"`
	ShareChange    *float64 `json:"shareChange"`
	Score          float64  `json:"score"`
}

type Point struct {
	Date     string   `json:"date"`
	Price    float64  `json:"price"`
	Multiple *float64 `json:"multiple,omitempty"`
}

type Valuation struct {
	Price              float64  `json:"price"`
	High5y             float64  `json:"high5y"`
	Drawdown           float64  `json:"drawdown"`
	MarketCapUSD       float64  `json:"marketCapUsd"`
	MarketCapEstimated bool     `json:"marketCapEstimated"`
	NormFCFUSD         float64  `json:"normFcfUsd"`
	FCFYield           *float64 `json:"fcfYield"`
	Multiple           *float64 `json:"multiple"`
	Percentile         *float64 `json:"percentile"`
	Median             *float64 `json:"median"`
	P10                *float64 `json:"p10"`
	Target             *float64 `json:"target"`
	Upside             *float64 `json:"upside"`
	Downside           *float64 `json:"downside"`
	Convexity          *float64 `json:"convexity"`
	HistoryMonths      int      `json:"historyMonths"`
	FCFRetention       *float64 `json:"fcfRetention"`
	LatestFCFvsAvg     *float64 `json:"latestFcfVsAvg"`
	RevenueRetention   *float64 `json:"revenueRetention"`
	FCFChangeSinceHigh *float64 `json:"fcfChangeSinceHigh"`
	Basis              string   `json:"basis"` // which period "latest" refers to, e.g. "TTM 截至 2026-08-28"
	Series             []Point  `json:"series"`
}

// Market is the per-company market data; nil fields mean the source returned nothing.
type Market struct {
	Chart     *yahoo.Chart
	MarketCap *yahoo.MarketCap
	Profile   *yahoo.Profile
	FX        []yahoo.Bar // monthly reporting-currency units per USD; nil for USD reporters
	FXSpot    float64     // the latest rate (Yahoo's current quote); 0 when unknown
	Errors    []string
}

type Evaluation struct {
	model.Company
	Sector    string     `json:"sector"`
	Industry  string     `json:"industry"`
	Quality   *Quality   `json:"quality"`
	Valuation *Valuation `json:"valuation,omitempty"`
	Checks    []Check    `json:"checks"`
	Stage     int        `json:"stage"` // number of consecutive layers passed (0-3)
	Score     float64    `json:"score"`
	Hints     []Hint     `json:"hints"`
	Notes     []string   `json:"notes"` // data caveats
	Analysis  *Analysis  `json:"analysis,omitempty"`
	// FX: reporting-currency units per USD used for today's valuation (the latest rate);
	// 0 for USD reporters. The P/FCF history uses each period's own rate.
	FX float64 `json:"fx,omitempty"`
}

// Evaluate runs layer 1 always, and layers 2-3 when market data is supplied.
func Evaluate(c *model.Company, m *Market, cfg config.Config, now time.Time) *Evaluation {
	e := &Evaluation{Company: *c}
	e.Quality, e.Checks = quality(c, cfg.Quality, now)
	if t := c.TTM; len(t) > 0 && t[len(t)-1].Source == "yahoo" {
		e.Notes = append(e.Notes, "该公司不提交 10-Q：最近 12 个月（截至 "+t[len(t)-1].End+"）由 Yahoo 的 4 个季度数据相加得到，不是 SEC XBRL，口径可能与年报略有出入；更早的期间仍用年报")
	}
	if latest := c.Latest(); len(c.TTM) == 0 && latest.End < now.AddDate(0, -6, 0).Format(time.DateOnly) {
		e.Notes = append(e.Notes, "财务数据截至 "+latest.End+"：该公司不提交季报（多为外国公司，季度业绩只发新闻稿），最近的变化请看新闻")
	}
	if latest := c.Latest(); latest.CapexImputed {
		e.Notes = append(e.Notes, "最近 12 个月未标注资本开支，FCF 按该公司历史资本开支/收入中位数估算")
	} else if latest.Capex == nil {
		e.Notes = append(e.Notes, "未找到资本开支科目，FCF 按经营现金流计，可能高估")
	}
	if m != nil {
		e.Notes = append(e.Notes, m.Errors...)
		if m.Profile != nil {
			e.Sector, e.Industry = m.Profile.Sector, m.Profile.Industry
		}
		var notes []string
		e.Valuation, notes = valuation(c, m, cfg.Convexity, now)
		e.Notes = append(e.Notes, notes...)
		e.Checks = append(e.Checks, dislocation(e, m, cfg.Dislocation)...)
		e.Checks = append(e.Checks, convexity(e.Valuation, cfg.Convexity)...)
	}
	fx := 1.0
	if m != nil {
		fx = spotFX(m, c.Latest().End)
	}
	e.Analysis = analyze(c, e.Valuation, fx)
	if c.Currency != "USD" && m != nil && (len(m.FX) > 0 || m.FXSpot > 0) {
		e.FX = fx
	}
	e.Stage = stage(e.Checks, m != nil)
	e.Score = e.Quality.Score * 0.5
	if v := e.Valuation; v != nil && v.Convexity != nil {
		e.Score += 5 * math.Min(*v.Convexity, 10)
	}
	e.Hints = hints(e)
	return e
}

func stage(checks []Check, hasMarket bool) int {
	maxLayer := 1
	if hasMarket {
		maxLayer = 3
	}
	for layer := 1; layer <= maxLayer; layer++ {
		for _, c := range checks {
			if c.Layer == layer && !c.Pass {
				return layer - 1
			}
		}
	}
	return maxLayer
}

// ---------- Layer 1: quality ----------

func quality(c *model.Company, r config.Quality, now time.Time) (*Quality, []Check) {
	ys := lastN(c.Years, 5)
	q := &Quality{Years: len(c.Years)}
	var fcfM, gm, opm, roic []float64
	for _, y := range ys {
		if *y.FCF > 0 {
			q.FCFPositive5++
		}
		if y.Revenue == nil || *y.Revenue <= 0 {
			continue
		}
		fcfM = append(fcfM, *y.FCF / *y.Revenue)
		if y.GrossProfit != nil {
			gm = append(gm, *y.GrossProfit / *y.Revenue)
		}
		if y.OpIncome != nil {
			opm = append(opm, *y.OpIncome / *y.Revenue)
			if y.Equity != nil {
				if ic := *y.Equity + *y.Debt - *y.Cash; ic > 0 {
					roic = append(roic, *y.OpIncome*(1-taxRate)/ic)
				}
			}
		}
	}
	q.FCFMargin, q.GrossMargin, q.OpMargin, q.ROIC = median(fcfM), median(gm), median(opm), median(roic)
	q.GrossMarginStd, q.OpMarginStd = stdev(gm), stdev(opm)

	first, last := ys[0], ys[len(ys)-1]
	if first.Revenue != nil && last.Revenue != nil && *first.Revenue > 0 && *last.Revenue > 0 && len(ys) > 1 {
		q.RevenueCAGR = model.F(math.Pow(*last.Revenue / *first.Revenue, 1/float64(len(ys)-1)) - 1)
	}
	bs := c.Latest() // balance sheet as of the latest filing (10-Q when available)
	netDebt := *bs.Debt - *bs.Cash
	q.NetCash = netDebt <= 0
	if avg := meanFCF(c.Recent()); avg > 0 {
		q.NetDebtToFCF = model.F(netDebt / avg)
	}
	if first.Shares != nil && last.Shares != nil && *first.Shares > 0 {
		// Unrestated pre-split counts show up as huge jumps; treat those as unknown.
		if ch := *last.Shares / *first.Shares - 1; ch > -0.45 && ch < 0.8 {
			q.ShareChange = model.F(ch)
		}
	}
	q.Score = qualityScore(q)

	end, _ := time.Parse(time.DateOnly, c.Latest().End)
	stale := now.AddDate(0, -r.MaxStaleMonths, 0)
	checks := []Check{
		{1, "年报数据年数", q.Years >= r.MinYears, fmt.Sprintf("%d 年", q.Years), fmt.Sprintf("≥ %d", r.MinYears), false, false},
		{1, "数据新鲜度", end.After(stale), latestLabel(c), fmt.Sprintf("近 %d 个月内", r.MaxStaleMonths), false, false},
		{1, "近5年FCF为正", q.FCFPositive5 >= r.MinFCFPositive5, fmt.Sprintf("%d/%d 年", q.FCFPositive5, len(ys)), fmt.Sprintf("≥ %d 年", r.MinFCFPositive5), q.FCFPositive5 == r.MinFCFPositive5, false},
		{1, "FCF利润率(中位)", atLeast(q.FCFMargin, r.MinFCFMargin), pct(q.FCFMargin), "≥ " + pct(&r.MinFCFMargin), near(q.FCFMargin, r.MinFCFMargin, true), false},
		{1, "营业利润率(中位)", q.OpMargin == nil || *q.OpMargin >= r.MinOpMargin, pctOr(q.OpMargin, "未披露(不判)"), "≥ " + pct(&r.MinOpMargin), near(q.OpMargin, r.MinOpMargin, true), false},
		{1, "收入5年复合增速", q.RevenueCAGR == nil || *q.RevenueCAGR >= r.MinRevenueCAGR, pctOr(q.RevenueCAGR, "未知(不判)"), "≥ " + pct(&r.MinRevenueCAGR), near(q.RevenueCAGR, r.MinRevenueCAGR, true), false},
		{1, "净债务/平均FCF", q.NetCash || (q.NetDebtToFCF != nil && *q.NetDebtToFCF <= r.MaxNetDebtToFCF), netDebtText(q), fmt.Sprintf("≤ %.1fx", r.MaxNetDebtToFCF), !q.NetCash && near(q.NetDebtToFCF, r.MaxNetDebtToFCF, false), false},
		{1, "质量分", q.Score >= r.MinScore, fmt.Sprintf("%.0f", q.Score), fmt.Sprintf("≥ %.0f", r.MinScore), near(&q.Score, r.MinScore, true), false},
	}
	return q, append(checks, trendChecks(c, q)...)
}

// Trend warnings (never fail L1): the latest 12 months against the 5-year history that L1
// judged. History can pass while the business is turning down.
const (
	grossMarginDrop = 0.03 // pp below the 5-year median
	fcfMarginDrop   = 0.05
	revenueSlowdown = 0.5 // latest growth below this share of the 5-year CAGR
	minCAGRForSlow  = 0.05
)

func trendChecks(c *model.Company, q *Quality) []Check {
	r := c.Recent()
	last := r[len(r)-1]
	label := "最近 12 个月"
	if last.TTM {
		label = "TTM"
	}
	if last.Revenue == nil || *last.Revenue <= 0 {
		return nil
	}
	var out []Check
	margin := func(name string, num, med *float64, drop float64) {
		if num == nil || med == nil {
			return
		}
		cur := *num / *last.Revenue
		d := cur - *med
		out = append(out, Check{Layer: 1, Name: "趋势：" + label + " " + name + " − 5 年中位", Pass: true,
			Value: fmt.Sprintf("%.1f%% → %.1f%%（%+.1fpp）", *med*100, cur*100, d*100),
			Rule:  fmt.Sprintf("跌幅 ≤ %.0fpp（预警，不淘汰）", drop*100), Warn: d < -drop})
	}
	margin("毛利率", last.GrossProfit, q.GrossMargin, grossMarginDrop)
	if g := latestGrowth(r); g != nil && q.RevenueCAGR != nil && *q.RevenueCAGR >= minCAGRForSlow {
		out = append(out, Check{Layer: 1, Name: "趋势：最近一年收入增速 vs 5 年年均", Pass: true,
			Value: fmt.Sprintf("%.1f%% → %.1f%%", *q.RevenueCAGR*100, *g*100),
			Rule:  fmt.Sprintf("≥ 年均的 %.0f%%（预警，不淘汰）", revenueSlowdown*100), Warn: *g < *q.RevenueCAGR*revenueSlowdown})
	}
	margin("营业利润率", last.OpIncome, q.OpMargin, opMarginDrop)
	margin("FCF 利润率", last.FCF, q.FCFMargin, fcfMarginDrop)
	return out
}

// latestGrowth: revenue growth of the latest 12-month period over the one before it.
func latestGrowth(r []model.Year) *float64 {
	n := len(r)
	if n < 2 || r[n-1].Revenue == nil || r[n-2].Revenue == nil || *r[n-2].Revenue <= 0 {
		return nil
	}
	return model.F(*r[n-1].Revenue / *r[n-2].Revenue - 1)
}

// qualityScore (0-100). ROIC and gross-margin level/stability stand in for a moat:
// a moat is invisible, but it leaves high and steady returns behind.
func qualityScore(q *Quality) float64 {
	s := 0.0
	s += valueOr(q.ROIC, func(v float64) float64 { return lin(v, 0.05, 0.25, 30) }, 10)
	s += valueOr(q.GrossMargin, func(v float64) float64 { return lin(v, 0.20, 0.60, 20) }, 8)
	std := q.GrossMarginStd
	if std == nil {
		std = q.OpMarginStd
	}
	s += valueOr(std, func(v float64) float64 { return lin(v, 0.10, 0.02, 15) }, 5)
	s += map[int]float64{5: 15, 4: 8}[q.FCFPositive5]
	s += valueOr(q.RevenueCAGR, func(v float64) float64 { return lin(v, -0.03, 0.10, 10) }, 3)
	switch {
	case q.NetCash:
		s += 10
	case q.NetDebtToFCF == nil:
	case *q.NetDebtToFCF <= 1:
		s += 7
	case *q.NetDebtToFCF <= 2:
		s += 5
	case *q.NetDebtToFCF <= 3:
		s += 3
	}
	return s
}

// ---------- Valuation history ----------

func valuation(c *model.Company, m *Market, r config.Convexity, now time.Time) (*Valuation, []string) {
	ch := m.Chart
	if ch == nil || len(ch.Bars) < 24 {
		return nil, []string{"价格历史不足，无法估值"}
	}
	if c.Currency != "USD" && len(m.FX) == 0 {
		return nil, []string{"缺少 " + c.Currency + " 汇率，无法估值"}
	}
	var notes []string
	v := &Valuation{Price: ch.Price}

	if m.MarketCap != nil {
		v.MarketCapUSD = m.MarketCap.Value * ch.Price / priceAt(ch.Bars, m.MarketCap.AsOf)
	} else if last := c.Years[len(c.Years)-1]; c.Currency == "USD" && last.Shares != nil {
		v.MarketCapUSD, v.MarketCapEstimated = ch.Price**last.Shares, true
		notes = append(notes, "市值为 价格×SEC稀释股数 估算")
	} else {
		return nil, []string{"无市值数据（ADR 需要 Yahoo 市值），无法估值"}
	}

	// The P/FCF history converts each year's FCF at that year's rate (the price then was in
	// that day's dollars); today's valuation converts at the latest rate.
	spot := 1.0
	if c.Currency != "USD" {
		spot = spotFX(m, c.Latest().End)
	}
	fcfUSD := make([]float64, len(c.Years))
	for i, y := range c.Years {
		fcfUSD[i] = *y.FCF / fxAt(m.FX, y.End)
	}
	// FCF for a fiscal year is only known after the report; ~100 days covers 10-K and most 20-F filers.
	normFrom := func(xs []float64, date string) (float64, bool) {
		var avail []float64
		for i, y := range c.Years {
			if addDays(y.End, 100) <= date {
				avail = append(avail, xs[i])
			}
		}
		if len(avail) < 2 {
			return 0, false
		}
		return mean(avail[max(0, len(avail)-3):]), true
	}
	normAt := func(date string) (float64, bool) { return normFrom(fcfUSD, date) }
	fcfLocal := make([]float64, len(c.Years))
	for i, y := range c.Years {
		fcfLocal[i] = *y.FCF
	}

	today := now.Format(time.DateOnly)
	cutoff5y := now.AddDate(-5, 0, 0).Format(time.DateOnly)
	v.High5y = ch.Price
	highDate := today
	var history []float64
	for i, b := range ch.Bars {
		if b.Date >= cutoff5y && b.Close > v.High5y {
			v.High5y, highDate = b.Close, b.Date
		}
		p := Point{Date: b.Date, Price: b.Close}
		if norm, ok := normAt(monthEnd(b.Date)); ok && norm > 0 {
			p.Multiple = model.F(v.MarketCapUSD * b.Close / ch.Price / norm)
			if i < len(ch.Bars)-1 { // the last bar is the current, incomplete month
				history = append(history, *p.Multiple)
			}
		}
		v.Series = append(v.Series, p)
	}
	v.Drawdown = ch.Price/v.High5y - 1
	v.HistoryMonths = len(history)

	normLocal, ok := normFrom(fcfLocal, today)
	if len(c.TTM) > 0 {
		// Today's normalized FCF: the three most recent non-overlapping 12-month periods —
		// three TTM periods from 10-Qs, or a Yahoo-derived TTM plus the two fiscal years before it.
		var sum float64
		rs := c.Recent()
		for _, y := range rs {
			sum += *y.FCF
		}
		normLocal, ok = sum/float64(len(rs)), true
	}
	norm := normLocal / spot
	v.Basis = latestLabel(c)
	if ok {
		v.NormFCFUSD = norm
		v.FCFYield = model.F(norm / v.MarketCapUSD)
		// In the reporting currency, so a currency move doesn't read as a business change.
		if normHigh, ok := normFrom(fcfLocal, monthEnd(highDate)); ok && normHigh > 0 {
			v.FCFChangeSinceHigh = model.F(normLocal/normHigh - 1)
		}
		if norm > 0 && len(history) >= 36 {
			cur := v.MarketCapUSD / norm
			sorted := slices.Sorted(slices.Values(history))
			below := 0
			for _, h := range sorted {
				if h <= cur {
					below++
				}
			}
			med, p10 := quantile(sorted, 0.5), quantile(sorted, 0.1)
			// Upside: re-rate to the historical median, capped. Downside: the multiple stays at
			// the lower of today and the 10th percentile while FCF also drops by the shock.
			target := math.Min(med, r.MaxTargetMultiple)
			up := target/cur - 1
			down := math.Min(p10, cur)*(1-r.FCFShock)/cur - 1
			v.Multiple, v.Median, v.P10, v.Target = &cur, &med, &p10, &target
			v.Percentile = model.F(float64(below) / float64(len(sorted)))
			v.Upside, v.Downside = &up, &down
			v.Convexity = model.F(math.Max(0, up) / -down)
			if n := len(v.Series); n > 0 {
				v.Series[n-1].Multiple = model.F(cur) // the current month uses today's (TTM) basis
			}
		} else if norm > 0 {
			notes = append(notes, fmt.Sprintf("估值历史仅 %d 个月（需 ≥36），无法计算分位与凸性", len(history)))
		}
	}

	// "Recent" = the latest three 12-month periods (TTM when a 10-Q is out); "prior" = the
	// fiscal years that end before the earliest recent period begins.
	recent := c.Recent()
	before := addDays(recent[0].End, -355)
	var older []model.Year
	for _, y := range c.Years {
		if y.End <= before {
			older = append(older, y)
		}
	}
	prior := lastN(older, 3)
	latest := recent[len(recent)-1]
	if len(prior) >= 2 {
		if p := meanFCF(prior); p > 0 {
			v.FCFRetention = model.F(meanFCF(recent) / p)
		}
	}
	if avg := meanFCF(recent); avg > 0 {
		v.LatestFCFvsAvg = model.F(*latest.FCF / avg)
	}
	best := 0.0
	for _, y := range recent {
		if y.Revenue != nil {
			best = math.Max(best, *y.Revenue)
		}
	}
	if last := latest.Revenue; last != nil && best > 0 {
		v.RevenueRetention = model.F(*last / best)
	}
	return v, notes
}

// ---------- Layer 2: dislocation ----------

func dislocation(e *Evaluation, m *Market, r config.Dislocation) []Check {
	v := e.Valuation
	sectorOK := !slices.Contains(r.ExcludeSectors, e.Sector)
	if m.Profile != nil && m.Profile.QuoteType != "" && m.Profile.QuoteType != "EQUITY" {
		sectorOK = false
	}
	checks := []Check{{2, "行业适用", sectorOK, orText(e.Sector, "未知"), "非金融/地产", false, false}}
	if v == nil {
		return append(checks, Check{2, "估值数据", false, "缺失", "需要价格与市值", false, false})
	}
	fcfRet := v.FCFRetention == nil || *v.FCFRetention >= r.MinFCFRetention
	return append(checks,
		Check{2, "市值", v.MarketCapUSD >= r.MinMarketCapUSD, money(v.MarketCapUSD), "≥ " + money(r.MinMarketCapUSD), near(&v.MarketCapUSD, r.MinMarketCapUSD, true), false},
		Check{2, "较5年高点回撤", v.Drawdown <= r.MaxDrawdown, pct(&v.Drawdown), "≤ " + pct(&r.MaxDrawdown), near(&v.Drawdown, r.MaxDrawdown, false), false},
		Check{2, "P/FCF 历史分位", atMost(v.Percentile, r.MaxPercentile), pctOr(v.Percentile, "历史不足"), "≤ " + pct(&r.MaxPercentile), near(v.Percentile, r.MaxPercentile, false), false},
		Check{2, "正常化FCF收益率", atLeast(v.FCFYield, r.MinFCFYield), pct(v.FCFYield), "≥ " + pct(&r.MinFCFYield), near(v.FCFYield, r.MinFCFYield, true), false},
		Check{2, "基本面未坏: FCF", fcfRet, pctOr(v.FCFRetention, "无可比(不判)"), "近3年均值 ≥ 之前的 " + pct(&r.MinFCFRetention), nearIn(v.FCFRetention, r.MinFCFRetention, 1-r.MinFCFRetention, true), false},
		Check{2, "基本面未坏: 最新年FCF", atLeast(v.LatestFCFvsAvg, r.MinLatestFCF), pct(v.LatestFCFvsAvg), "≥ 近3年均值的 " + pct(&r.MinLatestFCF), near(v.LatestFCFvsAvg, r.MinLatestFCF, true), false},
		Check{2, "基本面未坏: 收入", atLeast(v.RevenueRetention, r.MinRevenueRetain), pct(v.RevenueRetention), "最新 ≥ 近3年最高的 " + pct(&r.MinRevenueRetain), nearIn(v.RevenueRetention, r.MinRevenueRetain, 1-r.MinRevenueRetain, true), false},
	)
}

// ---------- Layer 3: convexity ----------

func convexity(v *Valuation, r config.Convexity) []Check {
	if v == nil || v.Convexity == nil {
		return []Check{{3, "凸性", false, "无法计算", "需要 ≥36 个月估值历史", false, false}}
	}
	return []Check{
		{3, "上行空间(估值修复)", *v.Upside >= r.MinUpside, fmt.Sprintf("%s（%.1fx → %.1fx P/FCF）", pct(v.Upside), *v.Multiple, *v.Target), "≥ " + pct(&r.MinUpside), near(v.Upside, r.MinUpside, true), false},
		{3, "下行风险(估值不修复且FCF再跌)", *v.Downside >= r.MaxDownside, pct(v.Downside), "≥ " + pct(&r.MaxDownside), near(v.Downside, r.MaxDownside, true), false},
		{3, "凸性(上行÷下行)", *v.Convexity >= r.MinRatio, fmt.Sprintf("%.1fx", *v.Convexity), fmt.Sprintf("≥ %.1fx", r.MinRatio), near(v.Convexity, r.MinRatio, true), false},
	}
}

// ---------- Layer 4 hints: the human decides external vs internal ----------

// opMarginDrop: the latest 12-month operating margin this far below the 5-year median is
// flagged as internal deterioration, even when the median still passes L1.
const opMarginDrop = 0.03

func hints(e *Evaluation) []Hint {
	var h []Hint
	switch e.Currency {
	case "USD":
	case "CNY", "HKD":
		h = append(h, Hint{"external", "中国相关公司：股价常受政策、地缘与中概折价影响"})
	default:
		h = append(h, Hint{"external", "以 " + e.Currency + " 报告的外国公司：可能受本国市场与汇率影响"})
	}
	ys := e.Company.Recent()
	if len(e.TTM) < 2 {
		ys = e.Years
	}
	if n := len(ys); n >= 2 && ys[n-1].Revenue != nil && ys[n-2].Revenue != nil && *ys[n-2].Revenue > 0 {
		if g := *ys[n-1].Revenue / *ys[n-2].Revenue - 1; g < -0.05 {
			h = append(h, Hint{"internal", "最近 12 个月收入同比下滑 " + pct(&g)})
		}
	}
	last := ys[len(ys)-1]
	if last := e.Company.Latest(); e.Quality.OpMargin != nil && last.OpIncome != nil && last.Revenue != nil && *last.Revenue > 0 {
		if cur := *last.OpIncome / *last.Revenue; cur-*e.Quality.OpMargin < -opMarginDrop {
			h = append(h, Hint{"internal", fmt.Sprintf("最近 12 个月营业利润率 %.1f%%，较 5 年中位 %.1f%% 下降 %.1f 个百分点：L1 通过的是历史，趋势在变差",
				cur*100, *e.Quality.OpMargin*100, -(cur-*e.Quality.OpMargin)*100)})
		}
	}
	// Margins against the 5-year median use the latest 12 months, as the L1 trend checks do.
	if last := e.Company.Latest(); e.Quality.GrossMargin != nil && last.GrossProfit != nil && last.Revenue != nil && *last.Revenue > 0 {
		if cur := *last.GrossProfit / *last.Revenue; cur-*e.Quality.GrossMargin < -grossMarginDrop {
			h = append(h, Hint{"internal", fmt.Sprintf("最近 12 个月毛利率 %.1f%%，较 5 年中位 %.1f%% 下降 %.1f 个百分点：定价权或产品结构在变",
				cur*100, *e.Quality.GrossMargin*100, -(cur-*e.Quality.GrossMargin)*100)})
		}
	}
	if g, cagr := latestGrowth(e.Company.Recent()), e.Quality.RevenueCAGR; g != nil && cagr != nil && *cagr >= minCAGRForSlow && *g < *cagr*revenueSlowdown && *g >= -0.05 { // a decline has its own hint above
		h = append(h, Hint{"internal", fmt.Sprintf("收入增速从 5 年年均 %.1f%% 降到最近一年 %.1f%%：增长换挡，历史估值未必能回去", *cagr*100, *g*100)})
	}
	if *last.FCF < 0 {
		h = append(h, Hint{"internal", "最近 12 个月 FCF 为负"})
	} else if v := e.Valuation; v != nil && v.LatestFCFvsAvg != nil && *v.LatestFCFvsAvg < 0.7 {
		h = append(h, Hint{"internal", fmt.Sprintf("最近 12 个月 FCF 只有近3年均值的 %s：要弄清是主动投入还是竞争恶化", pct(v.LatestFCFvsAvg))})
	}
	if s := e.Quality.ShareChange; s != nil && *s > 0.10 {
		h = append(h, Hint{"internal", "5年股本稀释 " + pct(s)})
	}
	if v := e.Valuation; v != nil && v.FCFChangeSinceHigh != nil && v.Drawdown <= -0.25 && *v.FCFChangeSinceHigh >= 0 {
		h = append(h, Hint{"positive", fmt.Sprintf("股价较 5 年高点 %s，而正常化 FCF（近 3 个 12 个月期间均值）比股价见顶时 %+.0f%%：价格与基本面背离", pct(&v.Drawdown), *v.FCFChangeSinceHigh*100)})
	}
	return h
}

// AddPeerHints flags industries that fell together, which points to an external cause.
func AddPeerHints(evals []*Evaluation) {
	byIndustry := map[string][]float64{}
	for _, e := range evals {
		if e.Valuation != nil && e.Industry != "" {
			byIndustry[e.Industry] = append(byIndustry[e.Industry], e.Valuation.Drawdown)
		}
	}
	for _, e := range evals {
		dds := byIndustry[e.Industry]
		if e.Valuation == nil || len(dds) < 3 {
			continue
		}
		if med := *median(dds); med <= -0.25 {
			e.Hints = append(e.Hints, Hint{"external", fmt.Sprintf("同行业(%s)优质公司 %d 家，中位回撤 %s：可能是行业或宏观因素", e.Industry, len(dds), pct(&med))})
		}
	}
}

// ---------- helpers ----------

func lastN(ys []model.Year, n int) []model.Year { return ys[max(0, len(ys)-n):] }

func meanFCF(ys []model.Year) float64 {
	xs := make([]float64, len(ys))
	for i, y := range ys {
		xs[i] = *y.FCF
	}
	return mean(xs)
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func median(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	return model.F(quantile(slices.Sorted(slices.Values(xs)), 0.5))
}

func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[lo+1]-sorted[lo])*(pos-float64(lo))
}

func stdev(xs []float64) *float64 {
	if len(xs) < 3 {
		return nil
	}
	m, s := mean(xs), 0.0
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return model.F(math.Sqrt(s / float64(len(xs))))
}

// lin maps x linearly from [a,b] onto [0,pts], clamped; a > b rewards smaller x.
func lin(x, a, b, pts float64) float64 {
	return math.Max(0, math.Min(1, (x-a)/(b-a))) * pts
}

func valueOr(p *float64, f func(float64) float64, fallback float64) float64 {
	if p == nil {
		return fallback
	}
	return f(*p)
}

func atLeast(p *float64, min float64) bool { return p != nil && *p >= min }
func atMost(p *float64, max float64) bool  { return p != nil && *p <= max }

// spotFX: the rate for today's valuation – Yahoo's latest quote, else the latest monthly close,
// else (no history) the average around the latest period.
func spotFX(m *Market, end string) float64 {
	if m.FXSpot > 0 {
		return m.FXSpot
	}
	if n := len(m.FX); n > 0 && m.FX[n-1].Close > 0 {
		return m.FX[n-1].Close
	}
	return fxAt(m.FX, end)
}

func fxAt(fx []yahoo.Bar, end string) float64 {
	if len(fx) == 0 {
		return 1
	}
	from := addDays(end, -365)
	var xs []float64
	for _, b := range fx {
		if b.Date > from && b.Date <= end {
			xs = append(xs, b.Close)
		}
	}
	if len(xs) == 0 { // older than the FX history: use the earliest rate
		return fx[0].Close
	}
	return mean(xs)
}

func priceAt(bars []yahoo.Bar, date string) float64 {
	p := bars[0].Close
	for _, b := range bars {
		if b.Date <= date {
			p = b.Close
		}
	}
	return p
}

func addDays(date string, days int) string {
	t, _ := time.Parse(time.DateOnly, date)
	return t.AddDate(0, 0, days).Format(time.DateOnly)
}

func monthEnd(monthStart string) string {
	t, _ := time.Parse(time.DateOnly, monthStart)
	return t.AddDate(0, 1, -1).Format(time.DateOnly)
}

func pct(p *float64) string { return pctOr(p, "—") }

func pctOr(p *float64, missing string) string {
	if p == nil {
		return missing
	}
	return fmt.Sprintf("%.1f%%", *p*100)
}

func money(v float64) string {
	switch {
	case v >= 1e12:
		return fmt.Sprintf("$%.2fT", v/1e12)
	case v >= 1e9:
		return fmt.Sprintf("$%.1fB", v/1e9)
	default:
		return fmt.Sprintf("$%.0fM", v/1e6)
	}
}

func netDebtText(q *Quality) string {
	if q.NetCash {
		return "净现金"
	}
	if q.NetDebtToFCF == nil {
		return "FCF≤0"
	}
	return fmt.Sprintf("%.1fx", *q.NetDebtToFCF)
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// latestLabel names the period the "latest" figures come from.
func latestLabel(c *model.Company) string {
	if len(c.TTM) > 0 {
		t := c.TTM[len(c.TTM)-1]
		if t.Source == "yahoo" {
			return "TTM 截至 " + t.End + "（Yahoo 季报汇总）"
		}
		return "TTM 截至 " + t.End
	}
	return "财年截至 " + c.Years[len(c.Years)-1].End
}
