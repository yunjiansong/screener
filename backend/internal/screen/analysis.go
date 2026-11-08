package screen

import (
	"math"

	"stockscreener/internal/model"
)

// Professional metrics shown on the detail page to support the human judgement (layer 4).
// They do not affect the screen's stage or score.

type MetricPoint struct {
	End string   `json:"end"`
	TTM bool     `json:"ttm,omitempty"`
	V   *float64 `json:"v"`
}

type Metric struct {
	Name   string        `json:"name"`
	Value  *float64      `json:"value"`  // latest period that has the item
	AsOf   string        `json:"asOf"`   // that period's end (prefixed "TTM " for TTM)
	Format string        `json:"format"` // pct | x | days | num
	Tone   string        `json:"tone"`   // good | warn | bad | ""
	Note   string        `json:"note"`   // how to read it
	Series []MetricPoint `json:"series"` // fiscal years (last 6) + latest TTM
}

type MetricGroup struct {
	Title   string   `json:"title"`
	Metrics []Metric `json:"metrics"`
}

type ScoreItem struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
}

type FScore struct {
	Value  int         `json:"value"`
	Max    int         `json:"max"`
	Items  []ScoreItem `json:"items"`
	Period string      `json:"period"` // "latest vs a year earlier"
}

type ZScore struct {
	Value float64 `json:"value"`
	Zone  string  `json:"zone"` // safe | grey | distress
}

// ReverseDCF solves for the 10-year FCF growth that today's enterprise value implies.
type ReverseDCF struct {
	ImpliedGrowth float64  `json:"impliedGrowth"`
	Discount      float64  `json:"discount"`
	Terminal      float64  `json:"terminal"`
	Years         int      `json:"years"`
	HistRevenue   *float64 `json:"histRevenue"` // 5-year revenue CAGR, for comparison
	HistFCF       *float64 `json:"histFcf"`     // 5-year FCF CAGR (both ends positive)
	// The solve above prices the operating business (EV = market cap + debt − cash); these
	// show the inputs and the same solve against the market cap, for comparison.
	EVUSD       float64  `json:"evUsd"`
	ByMarketCap *float64 `json:"byMarketCap,omitempty"`
}

type Analysis struct {
	Groups     []MetricGroup `json:"groups"`
	FScore     *FScore       `json:"fscore"`
	ZScore     *ZScore       `json:"zscore"`
	ReverseDCF *ReverseDCF   `json:"reverseDcf"`
	Value      *ValueInputs  `json:"value,omitempty"`
}

func div(a, b *float64) *float64 {
	if a == nil || b == nil || *b == 0 {
		return nil
	}
	return model.F(*a / *b)
}

func sub(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	return model.F(*a - *b)
}

func add(a, b *float64) *float64 {
	if a == nil && b == nil {
		return nil
	}
	return model.F(orZero(a) + orZero(b))
}

func orZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func positive(p *float64) *float64 {
	if p == nil || *p <= 0 {
		return nil
	}
	return p
}

// tone grades v against thresholds: higher is better unless lowerBetter.
func tone(v *float64, good, bad float64, lowerBetter bool) string {
	if v == nil {
		return ""
	}
	x := *v
	if lowerBetter {
		x, good, bad = -x, -good, -bad
	}
	switch {
	case x >= good:
		return "good"
	case x < bad:
		return "bad"
	default:
		return "warn"
	}
}

func analyze(c *model.Company, v *Valuation, fxLatest float64) *Analysis {
	hist := lastN(c.Years, 6)
	periods := append([]model.Year{}, hist...)
	if len(c.TTM) > 0 {
		periods = append(periods, c.TTM[len(c.TTM)-1])
	}
	latest := c.Latest()

	// series evaluates f on every period; f may use the previous period (nil for the first).
	series := func(f func(y, prev *model.Year) *float64) []MetricPoint {
		out := make([]MetricPoint, len(periods))
		for i := range periods {
			var prev *model.Year
			if i > 0 {
				prev = &periods[i-1]
				if periods[i].TTM { // compare TTM with the TTM a year earlier, not the overlapping fiscal year
					prev = nil
					if n := len(c.TTM); n >= 2 {
						prev = &c.TTM[n-2]
					}
				}
			}
			out[i] = MetricPoint{End: periods[i].End, TTM: periods[i].TTM, V: f(&periods[i], prev)}
		}
		return out
	}
	metric := func(name, format, note string, f func(y, prev *model.Year) *float64, grade func(*float64) string) Metric {
		s := series(f)
		m := Metric{Name: name, Format: format, Note: note, Series: s}
		for i := len(s) - 1; i >= 0; i-- {
			if s[i].V != nil {
				m.Value, m.AsOf = s[i].V, s[i].End
				if s[i].TTM {
					m.AsOf = "TTM " + s[i].End
				}
				break
			}
		}
		if grade != nil {
			m.Tone = grade(m.Value)
		}
		return m
	}
	avg := func(a, b *float64) *float64 {
		if a == nil {
			return b
		}
		if b == nil {
			return a
		}
		return model.F((*a + *b) / 2)
	}
	nopat := func(y *model.Year) *float64 {
		if y.OpIncome == nil {
			return nil
		}
		rate := taxRate
		if r := div(y.Tax, y.Pretax); r != nil && *r > 0 && *r < 0.5 {
			rate = *r
		}
		return model.F(*y.OpIncome * (1 - rate))
	}
	ebitda := func(y *model.Year) *float64 {
		if y.OpIncome == nil || y.DA == nil {
			return nil
		}
		return model.F(*y.OpIncome + *y.DA)
	}
	cogs := func(y *model.Year) *float64 { return sub(y.Revenue, y.GrossProfit) }

	a := &Analysis{}
	a.Groups = append(a.Groups, MetricGroup{Title: "盈利质量：利润是不是真金白银", Metrics: []Metric{
		metric("FCF / 净利润", "pct", "现金转化率；长期 < 70% 说明利润含水分或投入很重",
			func(y, _ *model.Year) *float64 { return div(y.FCF, positive(y.NetIncome)) },
			func(v *float64) string { return tone(v, 0.9, 0.7, false) }),
		metric("应计比率", "pct", "(净利润 − 经营现金流) ÷ 平均总资产；越高越可能是会计利润，> 10% 警惕",
			func(y, p *model.Year) *float64 {
				var pa *float64
				if p != nil {
					pa = p.Assets
				}
				return div(sub(y.NetIncome, y.OCF), avg(y.Assets, pa))
			},
			func(v *float64) string { return tone(v, 0.0, 0.1, true) }),
		metric("股权激励 / 收入", "pct", "SBC 是真实成本；> 10% 时，FCF 被明显美化",
			func(y, _ *model.Year) *float64 { return div(y.SBC, y.Revenue) },
			func(v *float64) string { return tone(v, 0.03, 0.10, true) }),
		metric("扣除 SBC 后 FCF 利润率", "pct", "把股权激励当现金成本扣掉后的 FCF 利润率",
			func(y, _ *model.Year) *float64 {
				if y.SBC == nil {
					return nil
				}
				return div(sub(y.FCF, y.SBC), y.Revenue)
			},
			func(v *float64) string { return tone(v, 0.10, 0.03, false) }),
		metric("资本开支 / 折旧", "x", "< 1 可能在吃老本；> 2 说明在扩张投入，看回报是否跟上",
			func(y, _ *model.Year) *float64 { return div(y.Capex, y.DA) }, nil),
	}})
	a.Groups = append(a.Groups, MetricGroup{Title: "资本回报：护城河的直接证据", Metrics: []Metric{
		metric("ROIC", "pct", "税后经营利润 ÷ (有息负债 + 股东权益 − 现金)；现金多到让分母为负时改为不扣现金（偏保守）；持续 > 15% 通常意味着护城河",
			func(y, _ *model.Year) *float64 {
				ic := sub(add(y.Debt, y.Equity), y.Cash)
				if ic != nil && *ic <= 0 {
					ic = add(y.Debt, y.Equity)
				}
				return div(nopat(y), positive(ic))
			},
			func(v *float64) string { return tone(v, 0.15, 0.08, false) }),
		metric("ROE", "pct", "净利润 ÷ 股东权益；高杠杆或大额回购会把 ROE 抬高，结合负债看",
			func(y, _ *model.Year) *float64 { return div(y.NetIncome, positive(y.Equity)) },
			func(v *float64) string { return tone(v, 0.15, 0.08, false) }),
		metric("毛利率", "pct", "定价权的直接体现；看趋势比看水平更重要",
			func(y, _ *model.Year) *float64 { return div(y.GrossProfit, y.Revenue) }, nil),
		metric("营业利润率", "pct", "连续下滑时，要分清是主动投入还是竞争恶化",
			func(y, _ *model.Year) *float64 { return div(y.OpIncome, y.Revenue) }, nil),
		metric("收入同比", "pct", "TTM 与一年前的 TTM 比较",
			func(y, p *model.Year) *float64 {
				if p == nil {
					return nil
				}
				return sub(div(y.Revenue, positive(p.Revenue)), model.F(1))
			},
			func(v *float64) string { return tone(v, 0.05, -0.03, false) }),
	}})
	a.Groups = append(a.Groups, MetricGroup{Title: "偿债能力：坏情况下能不能扛住", Metrics: []Metric{
		metric("净债务 / EBITDA", "x", "< 1 很稳；> 3 在下行周期里有压力；负数表示净现金",
			func(y, _ *model.Year) *float64 { return div(sub(y.Debt, y.Cash), positive(ebitda(y))) },
			func(v *float64) string { return tone(v, 1, 3, true) }),
		metric("利息保障倍数", "x", "营业利润 ÷ 利息支出；< 3 时，利润下滑可能伤到偿债",
			func(y, _ *model.Year) *float64 { return div(y.OpIncome, positive(y.Interest)) },
			func(v *float64) string { return tone(v, 8, 3, false) }),
		metric("流动比率", "x", "流动资产 ÷ 流动负债；< 1 要看现金流是否足以覆盖短债",
			func(y, _ *model.Year) *float64 { return div(y.CurAssets, positive(y.CurLiab)) },
			func(v *float64) string { return tone(v, 1.5, 1, false) }),
		metric("商誉与无形资产 / 总资产", "pct", "> 40% 时，账面资产大多来自收购，减值风险更大",
			func(y, _ *model.Year) *float64 { return div(add(y.Goodwill, y.Intangibles), positive(y.Assets)) },
			func(v *float64) string { return tone(v, 0.2, 0.4, true) }),
	}})
	shareholder := MetricGroup{Title: "股东回报与稀释", Metrics: []Metric{
		metric("股本变化（同比）", "pct", "负数 = 回购减少了股本；持续为正 = 稀释",
			func(y, p *model.Year) *float64 {
				if p == nil {
					return nil
				}
				return sub(div(y.Shares, positive(p.Shares)), model.F(1))
			},
			func(v *float64) string { return tone(v, -0.01, 0.02, true) }),
		metric("回购 / 股权激励", "x", "< 1 说明回购连股权激励带来的稀释都抵不掉",
			func(y, _ *model.Year) *float64 { return div(y.Buyback, positive(y.SBC)) },
			func(v *float64) string { return tone(v, 1.5, 1, false) }),
		metric("回购 + 分红 / FCF", "pct", "FCF 里有多少还给了股东；> 100% 可能在借钱回购",
			func(y, _ *model.Year) *float64 { return div(add(y.Buyback, y.Dividends), positive(y.FCF)) }, nil),
	}}
	if v != nil && v.MarketCapUSD > 0 {
		mcapLocal := v.MarketCapUSD * fxLatest
		shareholder.Metrics = append(shareholder.Metrics, Metric{Name: "股东收益率（回购 + 分红）÷ 市值", Format: "pct",
			Note:  "按最近 12 个月的现金回购和分红计算",
			AsOf:  latestLabel(c),
			Value: div(add(latest.Buyback, latest.Dividends), model.F(mcapLocal)),
		})
		last := &shareholder.Metrics[len(shareholder.Metrics)-1]
		last.Tone = tone(last.Value, 0.04, 0.01, false)
	}
	a.Groups = append(a.Groups, shareholder)
	a.Groups = append(a.Groups, MetricGroup{Title: "营运效率", Metrics: []Metric{
		metric("应收账款周转天数", "days", "持续上升可能是放宽信用条件来冲收入",
			func(y, _ *model.Year) *float64 {
				d := div(y.Receivables, positive(y.Revenue))
				if d == nil {
					return nil
				}
				return model.F(*d * 365)
			}, nil),
		metric("存货周转天数", "days", "持续上升可能是需求变弱、存货积压",
			func(y, _ *model.Year) *float64 {
				d := div(y.Inventory, positive(cogs(y)))
				if d == nil {
					return nil
				}
				return model.F(*d * 365)
			}, nil),
		metric("研发 / 收入", "pct", "科技与医药公司的未来投入",
			func(y, _ *model.Year) *float64 { return div(y.RD, y.Revenue) }, nil),
		metric("销售及管理费用 / 收入", "pct", "上升而收入不涨，说明获客变贵或经营效率下降",
			func(y, _ *model.Year) *float64 { return div(y.SGA, y.Revenue) }, nil),
	}})

	a.FScore = piotroski(c)
	if v != nil && v.MarketCapUSD > 0 {
		a.ZScore = altman(latest, v.MarketCapUSD*fxLatest)
		a.ReverseDCF = reverseDCF(c, v, fxLatest)
		a.Value = valueInputs(c, v, fxLatest, a.ReverseDCF)
	}
	return a
}

// piotroski: nine binary tests of profitability, leverage/liquidity and efficiency,
// comparing the latest 12 months with the 12 months a year earlier.
func piotroski(c *model.Company) *FScore {
	var cur, prev model.Year
	period := ""
	switch {
	case len(c.TTM) >= 2:
		cur, prev = c.TTM[len(c.TTM)-1], c.TTM[len(c.TTM)-2]
		period = "TTM " + cur.End + " 对比一年前"
	case len(c.Years) >= 2:
		cur, prev = c.Years[len(c.Years)-1], c.Years[len(c.Years)-2]
		period = "财年 " + cur.End + " 对比上一财年"
	default:
		return nil
	}
	if cur.Assets == nil || prev.Assets == nil || cur.NetIncome == nil || prev.NetIncome == nil {
		return nil
	}
	roa := func(y model.Year) float64 { return *y.NetIncome / *y.Assets }
	lev := func(y model.Year) float64 { return orZero(y.Debt) / *y.Assets }
	cr := func(y model.Year) *float64 { return div(y.CurAssets, positive(y.CurLiab)) }
	gm := func(y model.Year) *float64 { return div(y.GrossProfit, positive(y.Revenue)) }
	turn := func(y model.Year) *float64 { return div(y.Revenue, y.Assets) }
	gt := func(a, b *float64) bool { return a != nil && b != nil && *a > *b }
	items := []ScoreItem{
		{"净利润为正", *cur.NetIncome > 0},
		{"经营现金流为正", *cur.OCF > 0},
		{"ROA 提高", roa(cur) > roa(prev)},
		{"经营现金流 > 净利润", *cur.OCF > *cur.NetIncome},
		{"杠杆下降", lev(cur) <= lev(prev)},
		{"流动比率提高", gt(cr(cur), cr(prev))},
		{"没有增发股本", cur.Shares != nil && prev.Shares != nil && *cur.Shares <= *prev.Shares*1.005},
		{"毛利率提高", gt(gm(cur), gm(prev))},
		{"资产周转率提高", gt(turn(cur), turn(prev))},
	}
	f := &FScore{Max: 9, Items: items, Period: period}
	for _, it := range items {
		if it.Pass {
			f.Value++
		}
	}
	return f
}

// altman: the original public-manufacturer Z-score; for asset-light businesses it is only a
// rough distress screen.
func altman(y model.Year, marketCap float64) *ZScore {
	if y.Assets == nil || *y.Assets <= 0 || y.Liabilities == nil || *y.Liabilities <= 0 || y.CurAssets == nil || y.CurLiab == nil ||
		y.OpIncome == nil || y.Revenue == nil {
		return nil
	}
	ta := *y.Assets
	z := 1.2*(*y.CurAssets-*y.CurLiab)/ta + 1.4*orZero(y.Retained)/ta + 3.3**y.OpIncome/ta +
		0.6*marketCap / *y.Liabilities + 1.0**y.Revenue/ta
	zone := "grey"
	switch {
	case z > 2.99:
		zone = "safe"
	case z < 1.81:
		zone = "distress"
	}
	return &ZScore{Value: z, Zone: zone}
}

const (
	dcfDiscount = 0.10
	dcfTerminal = 0.03
	dcfYears    = 10
)

// reverseDCF finds g such that the present value of normalized FCF growing at g for ten
// years, plus a terminal value growing at 3%, equals today's enterprise value at a 10% discount rate.
func reverseDCF(c *model.Company, v *Valuation, fx float64) *ReverseDCF {
	if v.NormFCFUSD <= 0 {
		return nil
	}
	latest := c.Latest()
	ev := v.MarketCapUSD + (orZero(latest.Debt)-orZero(latest.Cash))/fx
	if ev <= 0 {
		return nil
	}
	pv := func(g float64) float64 {
		total, f := 0.0, v.NormFCFUSD
		for t := 1; t <= dcfYears; t++ {
			f *= 1 + g
			total += f / math.Pow(1+dcfDiscount, float64(t))
		}
		tv := f * (1 + dcfTerminal) / (dcfDiscount - dcfTerminal)
		return total + tv/math.Pow(1+dcfDiscount, dcfYears)
	}
	solve := func(target float64) *float64 {
		lo, hi := -0.5, 1.0
		if pv(lo) > target || pv(hi) < target {
			return nil
		}
		for range 60 {
			mid := (lo + hi) / 2
			if pv(mid) < target {
				lo = mid
			} else {
				hi = mid
			}
		}
		return model.F((lo + hi) / 2)
	}
	g := solve(ev)
	if g == nil {
		return nil
	}
	r := &ReverseDCF{ImpliedGrowth: *g, Discount: dcfDiscount, Terminal: dcfTerminal, Years: dcfYears, EVUSD: ev}
	if math.Abs(ev-v.MarketCapUSD) > 0.02*v.MarketCapUSD {
		r.ByMarketCap = solve(v.MarketCapUSD)
	}
	ys := lastN(c.Years, 6)
	if n := len(ys); n >= 4 {
		first, last := ys[0], ys[n-1]
		years := float64(n - 1)
		if first.Revenue != nil && last.Revenue != nil && *first.Revenue > 0 && *last.Revenue > 0 {
			r.HistRevenue = model.F(math.Pow(*last.Revenue / *first.Revenue, 1/years) - 1)
		}
		if *first.FCF > 0 && *last.FCF > 0 {
			r.HistFCF = model.F(math.Pow(*last.FCF / *first.FCF, 1/years) - 1)
		}
	}
	return r
}
