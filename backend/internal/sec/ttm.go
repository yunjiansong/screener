package sec

import (
	"encoding/json"
	"slices"
	"time"

	"stockscreener/internal/model"
)

// Trailing-twelve-month periods from quarterly (10-Q) filings.
//
// 10-Q cash-flow statements are year-to-date, so for a quarter ending E inside fiscal year F+1:
//
//	TTM(E) = FY(F) + YTD(E) − YTD(E − 1 year)
//
// The same identity one and two years back gives three non-overlapping 12-month periods that
// all end in E's month, which is what "the last three years" should mean once a quarter is out.
// Foreign private issuers (20-F / 40-F) file no 10-Q, so they get no TTM.

var quarterlyForms = map[string]bool{"10-Q": true, "10-Q/A": true}

type span struct{ start, end string }

// durations collects every duration fact (any form) by start/end; the latest filing wins.
func durations(cc *concept, unit string) map[span]float64 {
	out := map[span]float64{}
	if cc == nil {
		return out
	}
	filed := map[span]string{}
	for _, f := range cc.Units[unit] {
		if f.Start == "" {
			continue
		}
		k := span{f.Start, f.End}
		if f.Filed > filed[k] {
			out[k], filed[k] = f.Val, f.Filed
		}
	}
	return out
}

func instants(cc *concept, unit string) map[string]float64 {
	out := map[string]float64{}
	if cc == nil {
		return out
	}
	filed := map[string]string{}
	for _, f := range cc.Units[unit] {
		if f.Start != "" {
			continue
		}
		if f.Filed > filed[f.End] {
			out[f.End], filed[f.End] = f.Val, f.Filed
		}
	}
	return out
}

func parseDay(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func daysBetween(a, b string) int { return int(parseDay(b).Sub(parseDay(a)).Hours() / 24) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// lookup finds a duration whose end is within tolEnd days of end and whose start is within
// 7 days of start (52/53-week fiscal calendars drift by a few days).
func lookup(m map[span]float64, start, end string, tolEnd int) (float64, bool) {
	best, found, bestDist := 0.0, false, 1<<30
	for k, v := range m {
		de, ds := abs(daysBetween(k.end, end)), abs(daysBetween(k.start, start))
		if de <= tolEnd && ds <= 7 && de+ds < bestDist {
			best, found, bestDist = v, true, de+ds
		}
	}
	return best, found
}

// fyBefore returns the latest fiscal year end strictly before date (within ~a year).
func fyBefore(fyEnds []string, date string) string {
	for i := len(fyEnds) - 1; i >= 0; i-- {
		if fyEnds[i] < date {
			if daysBetween(fyEnds[i], date) <= 372 {
				return fyEnds[i]
			}
			return ""
		}
	}
	return ""
}

func shiftYears(date string, n int) string {
	return parseDay(date).AddDate(n, 0, 0).Format(time.DateOnly)
}

func dayAfter(date string) string { return parseDay(date).AddDate(0, 0, 1).Format(time.DateOnly) }

// buildTTM returns up to three TTM periods (ascending) ending at the latest 10-Q quarter,
// or nil when the latest filing is the annual report itself.
func buildTTM(facts map[string]json.RawMessage, conceptsFor func(spec) []string, taxonomy, currency string,
	annual map[string]map[string]float64, years []model.Year) []model.Year {
	if len(years) == 0 {
		return nil
	}
	fyEnds := make([]string, 0, len(annual["ocf"]))
	for end := range annual["ocf"] {
		fyEnds = append(fyEnds, end)
	}
	slices.Sort(fyEnds)
	lastFY := fyEnds[len(fyEnds)-1]

	// Latest quarter: the newest 10-Q year-to-date OCF fact that starts right after a fiscal year end.
	latest := ""
	for _, name := range conceptsFor(specs["ocf"]) {
		cc := decodeConcept(facts[name])
		if cc == nil {
			continue
		}
		for _, f := range cc.Units[currency] {
			if quarterlyForms[f.Form] && f.Start != "" && f.End > lastFY && f.End > latest &&
				abs(daysBetween(lastFY, f.Start)-1) <= 7 && daysBetween(f.Start, f.End) < 340 {
				latest = f.End
			}
		}
	}
	if latest == "" {
		return nil
	}

	flows := map[string]map[span]float64{}
	points := map[string]map[string]float64{}
	for field, s := range specs {
		unit := currency
		if s.shares {
			unit = "shares"
		}
		if s.instant {
			merged := map[string]float64{}
			for _, name := range conceptsFor(s) {
				for end, v := range instants(decodeConcept(facts[name]), unit) {
					if old, ok := merged[end]; !ok || (s.max && v > old) {
						merged[end] = v
					}
				}
			}
			points[field] = merged
			continue
		}
		merged := map[span]float64{}
		for _, name := range conceptsFor(s) {
			for k, v := range durations(decodeConcept(facts[name]), unit) {
				old, ok := merged[k]
				switch {
				case !ok:
					merged[k] = v
				case s.sumGAAP && taxonomy == "us-gaap":
					merged[k] = old + v
				case s.max && v > old:
					merged[k] = v
				}
			}
		}
		flows[field] = merged
	}

	// ttmFlow computes FY(F) + YTD(E) − YTD(E−1y) for one field; false if any piece is missing.
	ttmFlow := func(field, end string) (float64, bool) {
		fy := fyBefore(fyEnds, end)
		fyPrev := fyBefore(fyEnds, shiftYears(end, -1))
		if fy == "" || fyPrev == "" {
			return 0, false
		}
		full, ok := annual[field][fy]
		if !ok {
			return 0, false
		}
		cur, ok1 := lookup(flows[field], dayAfter(fy), end, 3)
		prev, ok2 := lookup(flows[field], dayAfter(fyPrev), shiftYears(end, -1), 10)
		if !ok1 || !ok2 {
			return 0, false
		}
		return full + cur - prev, true
	}
	opt := func(field, end string) *float64 {
		if v, ok := ttmFlow(field, end); ok {
			return model.F(v)
		}
		return nil
	}
	point := func(field, end string) *float64 {
		for d := 0; d <= 10; d++ { // instants of the comparable quarter may be dated a few days apart
			for _, e := range []string{parseDay(end).AddDate(0, 0, -d).Format(time.DateOnly), parseDay(end).AddDate(0, 0, d).Format(time.DateOnly)} {
				if v, ok := points[field][e]; ok {
					return model.F(v)
				}
			}
		}
		return nil
	}
	// Capex intensity from annual data, for quarters where capex is untagged.
	var ratios []float64
	for _, y := range years {
		if y.Capex != nil && !y.CapexImputed && y.Revenue != nil && *y.Revenue > 0 {
			ratios = append(ratios, *y.Capex / *y.Revenue)
		}
	}
	slices.Sort(ratios)

	var out []model.Year
	for k := 2; k >= 0; k-- {
		end := shiftYears(latest, -k)
		ocf, ok := ttmFlow("ocf", end)
		if !ok {
			continue
		}
		y := model.Year{End: end, TTM: true, OCF: model.F(ocf), Revenue: opt("revenue", end), OpIncome: opt("op_income", end),
			NetIncome: opt("net_income", end), GrossProfit: opt("gross", end)}
		if cogs := opt("cogs", end); y.GrossProfit == nil && cogs != nil && y.Revenue != nil {
			y.GrossProfit = model.F(*y.Revenue - *cogs)
		}
		y.Capex = maxOf(opt("capex", end), opt("capex_agg", end))
		capex := orZero(y.Capex)
		if y.Capex == nil && len(ratios) > 0 && y.Revenue != nil {
			capex, y.CapexImputed = ratios[len(ratios)/2]**y.Revenue, true
		}
		y.FCF = model.F(ocf - capex)
		y.Cash = model.F(orZero(point("cash", end)) + orZero(point("st_invest", end)))
		y.Equity = point("equity", end)
		pget := func(field, e string) *float64 { return point(field, e) }
		y.Debt = model.F(debt(pget, end))
		setExtras(&y, func(f string) *float64 {
			if specs[f].instant {
				return point(f, end)
			}
			return opt(f, end)
		})
		// Weighted shares are duration facts; take whichever period ends at the quarter.
		for kk, v := range flows["shares"] {
			if abs(daysBetween(kk.end, end)) <= 3 {
				y.Shares = model.F(v)
				break
			}
		}
		out = append(out, y)
	}
	// Only the latest period is required; earlier ones are dropped if a gap breaks the series.
	if len(out) == 0 || out[len(out)-1].End != latest {
		return nil
	}
	return out
}
