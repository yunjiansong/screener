// Package yahoo reads prices, market cap, sector and FX from Yahoo Finance's public JSON endpoints.
// These endpoints are unofficial: no SLA, and they may change or rate-limit without notice.
package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"stockscreener/internal/fetch"
)

type Bar struct {
	Date   string  `json:"date"` // month start, YYYY-MM-DD
	Close  float64 `json:"close"`
	Volume float64 `json:"volume,omitempty"` // shares traded (daily charts)
}

type Chart struct {
	Currency string
	Price    float64
	Bars     []Bar // monthly, split-adjusted, ascending
}

type Profile struct {
	Sector, Industry, QuoteType string
}

type MarketCap struct {
	AsOf  string
	Value float64
}

type Client struct{ c *fetch.Client }

func New(c *fetch.Client) *Client { return &Client{c} }

// MonthlyChart returns ~10 years of monthly closes. symbol is a Yahoo symbol, e.g. "PDD" or "CNY=X".
func (y *Client) MonthlyChart(ctx context.Context, symbol string) (*Chart, error) {
	return y.chart(ctx, symbol, "10y", "1mo", "yahoo/chart/"+symbol+".json", 20*time.Hour)
}

// DailyChart returns ~6 months of daily closes, dated in New York time; used to measure
// the price reaction to news.
func (y *Client) DailyChart(ctx context.Context, symbol string) (*Chart, error) {
	return y.DailyChartFresh(ctx, symbol, 6*time.Hour)
}

// DailyChartFresh is DailyChart with a caller-chosen cache age (alerts want fresh prices).
func (y *Client) DailyChartFresh(ctx context.Context, symbol string, maxAge time.Duration) (*Chart, error) {
	return y.chart(ctx, symbol, "6mo", "1d", "yahoo/daily/"+symbol+".json", maxAge)
}

var newYork, _ = time.LoadLocation("America/New_York")

func (y *Client) chart(ctx context.Context, symbol, rng, interval, key string, maxAge time.Duration) (*Chart, error) {
	u := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?range=%s&interval=%s", url.PathEscape(symbol), rng, interval)
	body, err := y.c.Get(ctx, u, key, maxAge)
	if err != nil {
		return nil, err
	}
	var r struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Currency           string  `json:"currency"`
					RegularMarketPrice float64 `json:"regularMarketPrice"`
				} `json:"meta"`
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Close  []*float64 `json:"close"`
						Volume []*float64 `json:"volume"`
					} `json:"quote"`
				} `json:"indicators"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("chart %s: %w", symbol, err)
	}
	if len(r.Chart.Result) == 0 || len(r.Chart.Result[0].Indicators.Quote) == 0 {
		return nil, fmt.Errorf("chart %s: empty", symbol)
	}
	res := r.Chart.Result[0]
	closes, volumes := res.Indicators.Quote[0].Close, res.Indicators.Quote[0].Volume
	out := &Chart{Currency: res.Meta.Currency, Price: res.Meta.RegularMarketPrice}
	for i, ts := range res.Timestamp {
		if i < len(closes) && closes[i] != nil && *closes[i] > 0 {
			t := time.Unix(ts, 0).UTC()
			if interval == "1d" && newYork != nil {
				t = t.In(newYork)
			}
			b := Bar{Date: t.Format(time.DateOnly), Close: *closes[i]}
			if i < len(volumes) && volumes[i] != nil {
				b.Volume = *volumes[i]
			}
			out.Bars = append(out.Bars, b)
		}
	}
	if out.Price <= 0 && len(out.Bars) > 0 {
		out.Price = out.Bars[len(out.Bars)-1].Close
	}
	return out, nil
}

// LatestMarketCap returns Yahoo's most recent quarter-end market cap in USD. Using it (rather
// than price × SEC share count) keeps ADRs correct without knowing the ADS ratio.
func (y *Client) LatestMarketCap(ctx context.Context, symbol string) (*MarketCap, error) {
	now := time.Now()
	u := fmt.Sprintf("https://query1.finance.yahoo.com/ws/fundamentals-timeseries/v1/finance/timeseries/%s?type=quarterlyMarketCap&period1=%d&period2=%d",
		url.PathEscape(symbol), now.AddDate(-2, 0, 0).Unix(), now.Unix())
	body, err := y.c.Get(ctx, u, "yahoo/mcap/"+symbol+".json", 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	var r struct {
		Timeseries struct {
			Result []struct {
				Values []struct {
					AsOfDate      string `json:"asOfDate"`
					CurrencyCode  string `json:"currencyCode"`
					ReportedValue struct {
						Raw float64 `json:"raw"`
					} `json:"reportedValue"`
				} `json:"quarterlyMarketCap"`
			} `json:"result"`
		} `json:"timeseries"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("market cap %s: %w", symbol, err)
	}
	var best *MarketCap
	for _, res := range r.Timeseries.Result {
		for _, v := range res.Values {
			if v.CurrencyCode == "USD" && v.ReportedValue.Raw > 0 && (best == nil || v.AsOfDate > best.AsOf) {
				best = &MarketCap{v.AsOfDate, v.ReportedValue.Raw}
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("market cap %s: none in USD", symbol)
	}
	return best, nil
}

func (y *Client) Profile(ctx context.Context, symbol string) (*Profile, error) {
	u := "https://query2.finance.yahoo.com/v1/finance/search?quotesCount=3&newsCount=0&q=" + url.QueryEscape(symbol)
	body, err := y.c.Get(ctx, u, "yahoo/profile/"+symbol+".json", 30*24*time.Hour)
	if err != nil {
		return nil, err
	}
	type quote struct {
		Symbol    string `json:"symbol"`
		Sector    string `json:"sectorDisp"`
		Industry  string `json:"industryDisp"`
		QuoteType string `json:"quoteType"`
	}
	var r struct {
		Quotes []quote `json:"quotes"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("profile %s: %w", symbol, err)
	}
	i := slices.IndexFunc(r.Quotes, func(q quote) bool { return q.Symbol == symbol })
	if i < 0 {
		return &Profile{}, nil
	}
	q := r.Quotes[i]
	return &Profile{q.Sector, q.Industry, q.QuoteType}, nil
}

// QuarterPoint is one quarterly value as Yahoo reports it.
type QuarterPoint struct {
	End      string
	Currency string
	Value    float64
}

// Quarterly fetches quarterly fundamentals (Yahoo keeps about the last five quarters).
// Keys are Yahoo types without the "quarterly" prefix, e.g. "TotalRevenue".
func (y *Client) Quarterly(ctx context.Context, symbol string, types []string) (map[string][]QuarterPoint, error) {
	now := time.Now()
	q := make([]string, len(types))
	for i, t := range types {
		q[i] = "quarterly" + t
	}
	u := fmt.Sprintf("https://query1.finance.yahoo.com/ws/fundamentals-timeseries/v1/finance/timeseries/%s?type=%s&period1=%d&period2=%d",
		url.PathEscape(symbol), strings.Join(q, ","), now.AddDate(-2, 0, 0).Unix(), now.Unix())
	body, err := y.c.Get(ctx, u, "yahoo/quarterly/"+symbol+".json", 3*24*time.Hour)
	if err != nil {
		return nil, err
	}
	var r struct {
		Timeseries struct {
			Result []map[string]json.RawMessage `json:"result"`
		} `json:"timeseries"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("quarterly %s: %w", symbol, err)
	}
	out := map[string][]QuarterPoint{}
	for _, res := range r.Timeseries.Result {
		for _, t := range types {
			raw, ok := res["quarterly"+t]
			if !ok {
				continue
			}
			var vals []*struct {
				AsOfDate      string `json:"asOfDate"`
				CurrencyCode  string `json:"currencyCode"`
				ReportedValue struct {
					Raw float64 `json:"raw"`
				} `json:"reportedValue"`
			}
			if json.Unmarshal(raw, &vals) != nil {
				continue
			}
			for _, v := range vals {
				if v != nil && v.AsOfDate != "" {
					out[t] = append(out[t], QuarterPoint{v.AsOfDate, v.CurrencyCode, v.ReportedValue.Raw})
				}
			}
		}
	}
	return out, nil
}
