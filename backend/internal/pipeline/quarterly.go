package pipeline

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"stockscreener/internal/model"
	"stockscreener/internal/yahoo"
)

// Foreign private issuers (20-F / 40-F) file no 10-Q, so SEC data stops at the annual report.
// Their quarterly results are only in 6-K press releases (no XBRL). Yahoo's quarterly
// fundamentals cover them; summing the latest four quarters gives a trailing-twelve-month
// period, in the company's reporting currency. Yahoo keeps about five quarters, so only the
// latest TTM can be built; the two earlier 12-month periods stay fiscal years.

var quarterTypes = []string{"TotalRevenue", "GrossProfit", "OperatingIncome", "NetIncome", "OperatingCashFlow",
	"CapitalExpenditure", "FreeCashFlow", "CashCashEquivalentsAndShortTermInvestments", "CurrentDebt", "LongTermDebt",
	"StockholdersEquity"}

func yahooTTM(ctx context.Context, yc *yahoo.Client, c *model.Company) (*model.Year, error) {
	if len(c.Years) == 0 {
		return nil, fmt.Errorf("no annual data")
	}
	fy := c.Years[len(c.Years)-1]
	q, err := yc.Quarterly(ctx, c.Ticker, quarterTypes)
	if err != nil {
		return nil, err
	}
	pick := func(t string) []yahoo.QuarterPoint {
		var xs []yahoo.QuarterPoint
		for _, p := range q[t] {
			if p.Currency == c.Currency {
				xs = append(xs, p)
			}
		}
		slices.SortFunc(xs, func(a, b yahoo.QuarterPoint) int { return cmpStr(a.End, b.End) })
		return xs
	}
	rev := pick("TotalRevenue")
	if len(rev) < 4 {
		return nil, fmt.Errorf("Yahoo 季度收入不足 4 个季度（%s）", c.Currency)
	}
	end := rev[len(rev)-1].End
	if end <= fy.End {
		return nil, fmt.Errorf("Yahoo 最新季度 %s 不晚于年报 %s", end, fy.End)
	}
	day := func(s string) time.Time { t, _ := time.Parse(time.DateOnly, s); return t }
	// sum4 adds the four quarters ending at end, which must be consecutive (~3 months apart).
	sum4 := func(t string) *float64 {
		xs := pick(t)
		i := slices.IndexFunc(xs, func(p yahoo.QuarterPoint) bool { return p.End == end })
		if i < 3 {
			return nil
		}
		span := day(xs[i].End).Sub(day(xs[i-3].End)).Hours() / 24
		if span < 250 || span > 300 {
			return nil
		}
		s := 0.0
		for _, p := range xs[i-3 : i+1] {
			s += p.Value
		}
		return &s
	}
	at := func(t string) *float64 {
		xs := pick(t)
		if len(xs) > 0 && xs[len(xs)-1].End == end {
			v := xs[len(xs)-1].Value
			return &v
		}
		return nil
	}
	y := model.Year{End: end, TTM: true, Source: "yahoo", Revenue: sum4("TotalRevenue"), GrossProfit: sum4("GrossProfit"),
		OpIncome: sum4("OperatingIncome"), NetIncome: sum4("NetIncome"), OCF: sum4("OperatingCashFlow")}
	if y.Revenue == nil || y.OCF == nil {
		return nil, fmt.Errorf("Yahoo 季度数据不连续")
	}
	// Units sanity check against the SEC annual report (catches ADS/thousands mix-ups).
	if fy.Revenue != nil && *fy.Revenue > 0 {
		if r := *y.Revenue / *fy.Revenue; r < 0.5 || r > 2 {
			return nil, fmt.Errorf("Yahoo TTM 收入是年报的 %.2f 倍，口径可疑", r)
		}
	}
	switch capex, fcf := sum4("CapitalExpenditure"), sum4("FreeCashFlow"); {
	case capex != nil:
		y.Capex = model.F(math.Abs(*capex))
	case fcf != nil:
		y.Capex = model.F(math.Max(0, *y.OCF-*fcf))
	default: // impute from the annual capex intensity, as for 10-Q filers without capex tags
		var ratios []float64
		for _, a := range c.Years {
			if a.Capex != nil && !a.CapexImputed && a.Revenue != nil && *a.Revenue > 0 {
				ratios = append(ratios, *a.Capex / *a.Revenue)
			}
		}
		if len(ratios) == 0 {
			return nil, fmt.Errorf("没有资本开支数据")
		}
		slices.Sort(ratios)
		y.Capex, y.CapexImputed = model.F(ratios[len(ratios)/2]**y.Revenue), true
	}
	y.FCF = model.F(*y.OCF - *y.Capex)
	y.Cash = orFY(at("CashCashEquivalentsAndShortTermInvestments"), fy.Cash)
	cur, lt := at("CurrentDebt"), at("LongTermDebt")
	if cur != nil || lt != nil {
		y.Debt = model.F(val(cur) + val(lt))
	} else {
		y.Debt = fy.Debt
	}
	y.Equity = orFY(at("StockholdersEquity"), fy.Equity)
	y.Shares = fy.Shares // Yahoo share counts mix ADS and ordinary shares
	return &y, nil
}

func orFY(v, fallback *float64) *float64 {
	if v != nil {
		return v
	}
	return fallback
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
