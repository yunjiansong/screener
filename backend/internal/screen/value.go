package screen

import "stockscreener/internal/model"

// ValueInputs are the per-share figures the intrinsic-value formulas need (Graham, DCF, EPV,
// historical multiple), in US dollars per *traded* share: totals are divided by the share count
// implied by market cap ÷ price, so ADS ratios and reporting currencies drop out. The formulas
// themselves run in the browser, so the person can change assumptions without a re-run.
type ValueInputs struct {
	Price         float64  `json:"price"`
	FCF           float64  `json:"fcf"`           // normalized FCF per share (same basis as the FCF yield)
	LatestFCF     *float64 `json:"latestFcf"`     // latest 12-month FCF per share
	EPS           *float64 `json:"eps"`           // average net income per share over the same periods
	LatestEPS     *float64 `json:"latestEps"`     // latest 12-month net income per share
	BookValue     *float64 `json:"bookValue"`     // shareholders' equity per share, latest
	NetCash       float64  `json:"netCash"`       // (cash + short-term investments − debt) per share
	HistFCFGrowth *float64 `json:"histFcfGrowth"` // 5-year FCF CAGR (both ends positive)
	HistRevGrowth *float64 `json:"histRevGrowth"` // 5-year revenue CAGR
	Median        *float64 `json:"medianMultiple"`
	P10           *float64 `json:"p10Multiple"`
}

func valueInputs(c *model.Company, v *Valuation, fx float64, dcf *ReverseDCF) *ValueInputs {
	if v.Price <= 0 || v.MarketCapUSD <= 0 || fx <= 0 {
		return nil
	}
	shares := v.MarketCapUSD / v.Price
	per := func(local float64) float64 { return local / fx / shares }
	latest := c.Latest()
	out := &ValueInputs{Price: v.Price, FCF: v.NormFCFUSD / shares, Median: v.Median, P10: v.P10,
		NetCash: per(orZero(latest.Cash) - orZero(latest.Debt))}
	if latest.FCF != nil {
		out.LatestFCF = model.F(per(*latest.FCF))
	}
	if latest.NetIncome != nil {
		out.LatestEPS = model.F(per(*latest.NetIncome))
	}
	if latest.Equity != nil {
		out.BookValue = model.F(per(*latest.Equity))
	}
	var sum float64
	var n int
	for _, y := range c.Recent() {
		if y.NetIncome != nil {
			sum += *y.NetIncome
			n++
		}
	}
	if n > 0 {
		out.EPS = model.F(per(sum / float64(n)))
	}
	if dcf != nil {
		out.HistFCFGrowth, out.HistRevGrowth = dcf.HistFCF, dcf.HistRevenue
	}
	return out
}
