// Package calendar estimates each company's next earnings date from free sources:
// Nasdaq's public earnings-date endpoint (Zacks estimate or company-confirmed date), and as
// a fallback the SEC filing history (8-K item 2.02 "Results of Operations" a year earlier).
package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"stockscreener/internal/fetch"
)

type Earnings struct {
	Date      string `json:"date"`      // YYYY-MM-DD
	Estimated bool   `json:"estimated"` // not yet confirmed by the company
	Source    string `json:"source"`    // nasdaq | sec
	Note      string `json:"note,omitempty"`
}

type Service struct {
	web, sec *fetch.Client
	mu       sync.Mutex
	memo     map[string]memoEntry
}

type memoEntry struct {
	e  *Earnings
	at time.Time
}

func New(web, sec *fetch.Client) *Service {
	return &Service{web: web, sec: sec, memo: map[string]memoEntry{}}
}

var reNasdaqDate = regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})`)

func (s *Service) nasdaq(ctx context.Context, ticker string) (*Earnings, error) {
	u := "https://api.nasdaq.com/api/analyst/" + strings.ReplaceAll(ticker, ".", "-") + "/earnings-date"
	body, err := s.web.Get(ctx, u, "calendar/nasdaq-"+ticker+".json", 12*time.Hour)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data *struct {
			ReportText string `json:"reportText"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Data == nil {
		return nil, fmt.Errorf("nasdaq: no data")
	}
	m := reNasdaqDate.FindStringSubmatch(r.Data.ReportText)
	if m == nil {
		return nil, fmt.Errorf("nasdaq: no date")
	}
	d, err := time.Parse("1/2/2006", m[1]+"/"+m[2]+"/"+m[3])
	if err != nil {
		return nil, err
	}
	txt := strings.ToLower(r.Data.ReportText)
	return &Earnings{Date: d.Format(time.DateOnly), Estimated: strings.Contains(txt, "estimated"), Source: "nasdaq"}, nil
}

// sec projects last year's results 8-Ks (item 2.02) forward by 52 weeks.
func (s *Service) secEstimate(ctx context.Context, cik int, now time.Time) (*Earnings, error) {
	body, err := s.sec.Get(ctx, fmt.Sprintf("https://data.sec.gov/submissions/CIK%010d.json", cik), fmt.Sprintf("profile/submissions-%d.json", cik), 24*time.Hour)
	if err != nil {
		return nil, err
	}
	var r struct {
		Filings struct {
			Recent struct {
				Form       []string `json:"form"`
				FilingDate []string `json:"filingDate"`
				Items      []string `json:"items"`
			} `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	rec := r.Filings.Recent
	best := ""
	today := now.Format(time.DateOnly)
	for i, f := range rec.Form {
		if f != "8-K" || i >= len(rec.Items) || !strings.Contains(rec.Items[i], "2.02") {
			continue
		}
		d, err := time.Parse(time.DateOnly, rec.FilingDate[i])
		if err != nil || now.Sub(d) > 400*24*time.Hour {
			continue
		}
		for _, next := range []time.Time{d.AddDate(0, 0, 91), d.AddDate(0, 0, 364)} { // a quarter on, a year on
			n := next.Format(time.DateOnly)
			if n >= today && (best == "" || n < best) {
				best = n
			}
		}
	}
	if best == "" {
		return nil, fmt.Errorf("SEC 没有可用的业绩公告记录")
	}
	return &Earnings{Date: best, Estimated: true, Source: "sec", Note: "按过去的业绩公告（8-K 2.02）日期推算"}, nil
}

// Next returns the next earnings date, or nil when neither source knows.
func (s *Service) Next(ctx context.Context, cik int, ticker string) *Earnings {
	s.mu.Lock()
	if m, ok := s.memo[ticker]; ok && time.Since(m.at) < 6*time.Hour {
		s.mu.Unlock()
		return m.e
	}
	s.mu.Unlock()
	now := time.Now()
	e, err := s.nasdaq(ctx, ticker)
	if err != nil || e.Date < now.AddDate(0, 0, -1).Format(time.DateOnly) { // stale: already reported
		if alt, err2 := s.secEstimate(ctx, cik, now); err2 == nil {
			e = alt
		} else if err != nil {
			e = nil
		}
	}
	if ctx.Err() == nil {
		s.mu.Lock()
		s.memo[ticker] = memoEntry{e, time.Now()}
		s.mu.Unlock()
	}
	return e
}

type Target struct {
	CIK    int
	Ticker string
}

// Many looks up several companies with a few requests in flight.
func (s *Service) Many(ctx context.Context, ts []Target) map[string]*Earnings {
	out := map[string]*Earnings{}
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		sem <- struct{}{}
		go func(t Target) {
			defer wg.Done()
			defer func() { <-sem }()
			if e := s.Next(ctx, t.CIK, t.Ticker); e != nil {
				mu.Lock()
				out[t.Ticker] = e
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	return out
}
