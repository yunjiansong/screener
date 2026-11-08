// Package model holds the data shared between ingestion, screening, storage and the API.
package model

import "time"

// Year is one fiscal year of reported fundamentals in the company's reporting currency.
// Nil means the company did not report the item (or we could not map it).
type Year struct {
	End string `json:"end"`           // fiscal year end date, YYYY-MM-DD (quarter end for TTM)
	TTM bool   `json:"ttm,omitempty"` // trailing twelve months built from 10-Q year-to-date figures
	// Source is "" for SEC XBRL, "yahoo" for a TTM summed from Yahoo quarterly figures
	// (foreign filers, whose quarterly results are not in XBRL).
	Source      string   `json:"source,omitempty"`
	Revenue     *float64 `json:"revenue,omitempty"`
	GrossProfit *float64 `json:"grossProfit,omitempty"`
	OpIncome    *float64 `json:"opIncome,omitempty"`
	NetIncome   *float64 `json:"netIncome,omitempty"`
	OCF         *float64 `json:"ocf,omitempty"`
	Capex       *float64 `json:"capex,omitempty"`
	// CapexImputed marks years where capex was untagged and estimated from the company's median
	// capex/revenue; FCF then uses the estimate.
	CapexImputed bool     `json:"capexImputed,omitempty"`
	FCF          *float64 `json:"fcf,omitempty"`
	Shares       *float64 `json:"shares,omitempty"`
	Cash         *float64 `json:"cash,omitempty"` // cash + short-term investments
	Debt         *float64 `json:"debt,omitempty"` // interest-bearing debt excluding leases
	Equity       *float64 `json:"equity,omitempty"`

	// Items used only by the professional metrics (screen.Analysis).
	SBC         *float64 `json:"sbc,omitempty"`       // share-based compensation
	DA          *float64 `json:"da,omitempty"`        // depreciation & amortization
	Interest    *float64 `json:"interest,omitempty"`  // interest expense
	RD          *float64 `json:"rd,omitempty"`        // R&D expense
	SGA         *float64 `json:"sga,omitempty"`       // SG&A expense
	Buyback     *float64 `json:"buyback,omitempty"`   // cash spent repurchasing shares
	Dividends   *float64 `json:"dividends,omitempty"` // cash dividends paid
	Tax         *float64 `json:"tax,omitempty"`
	Pretax      *float64 `json:"pretax,omitempty"`
	Assets      *float64 `json:"assets,omitempty"`
	CurAssets   *float64 `json:"curAssets,omitempty"`
	CurLiab     *float64 `json:"curLiab,omitempty"`
	Liabilities *float64 `json:"liabilities,omitempty"`
	Retained    *float64 `json:"retained,omitempty"`
	Inventory   *float64 `json:"inventory,omitempty"`
	Receivables *float64 `json:"receivables,omitempty"`
	Goodwill    *float64 `json:"goodwill,omitempty"`
	Intangibles *float64 `json:"intangibles,omitempty"`
}

type Company struct {
	CIK      int    `json:"cik"`
	Ticker   string `json:"ticker"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Currency string `json:"currency"` // reporting currency
	Taxonomy string `json:"taxonomy"` // us-gaap or ifrs-full
	Years    []Year `json:"years"`    // fiscal years, ascending by End
	// TTM holds up to three trailing-twelve-month periods ending at the latest 10-Q quarter
	// (one year apart, ascending). Empty when the annual report is the latest filing, and
	// always for foreign filers, which file no 10-Q.
	TTM []Year `json:"ttm,omitempty"`
}

// Recent returns the three most recent non-overlapping 12-month periods: TTM when
// available, otherwise fiscal years.
//
// With fewer than three TTM periods (Yahoo-derived TTM keeps only the latest), the earlier
// periods are the fiscal years that end at least a year before the first TTM period, so
// the periods never overlap (a few months between them may go uncovered).
func (c *Company) Recent() []Year {
	if len(c.TTM) >= 3 {
		return c.TTM
	}
	if len(c.TTM) == 0 {
		return c.Years[max(0, len(c.Years)-3):]
	}
	cut := c.TTM[0].End
	var before []Year
	for _, y := range c.Years {
		if dayDiff(y.End, cut) >= 355 {
			before = append(before, y)
		}
	}
	return append(before[max(0, len(before)-(3-len(c.TTM))):], c.TTM...)
}

func dayDiff(a, b string) int {
	ta, err1 := time.Parse(time.DateOnly, a)
	tb, err2 := time.Parse(time.DateOnly, b)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(tb.Sub(ta).Hours() / 24)
}

// Latest is the most recent 12-month period.
func (c *Company) Latest() Year {
	r := c.Recent()
	return r[len(r)-1]
}

func F(v float64) *float64 { return &v }
