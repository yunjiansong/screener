package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Attention measures how much the company is being looked at, from sources that allow
// automated access: Wikipedia pageviews (public interest) and trading volume (market).
// Google Trends and the social networks offer no open API; the news feed is capped and
// relevance-ranked, so headline counts are not a usable volume measure either.

type DayValue struct {
	Date string  `json:"date"`
	V    float64 `json:"v"`
}

type AttentionSeries struct {
	Key    string     `json:"key"`   // wiki_en | wiki_zh | volume
	Label  string     `json:"label"` // 维基百科浏览量（英文）…
	Unit   string     `json:"unit"`
	Source string     `json:"source"`
	URL    string     `json:"url,omitempty"`
	Days   []DayValue `json:"days"`   // daily, ascending, last ~120 days
	Recent float64    `json:"recent"` // daily average over the last 30 days
	Base   float64    `json:"base"`   // daily average over the days before that
	Ratio  *float64   `json:"ratio"`  // recent / base
	Note   string     `json:"note,omitempty"`
}

type Attention struct {
	Series []AttentionSeries `json:"series"`
}

// summarize fills Recent/Base/Ratio from Days, splitting at 30 days before the last date.
func (s *AttentionSeries) summarize(end time.Time) {
	cut := end.AddDate(0, 0, -30).Format(time.DateOnly)
	var r, b []float64
	for _, d := range s.Days {
		if d.Date > cut {
			r = append(r, d.V)
		} else {
			b = append(b, d.V)
		}
	}
	avg := func(xs []float64) float64 {
		if len(xs) == 0 {
			return 0
		}
		t := 0.0
		for _, x := range xs {
			t += x
		}
		return t / float64(len(xs))
	}
	s.Recent, s.Base = avg(r), avg(b)
	if s.Base > 0 && len(b) >= 20 {
		ratio := s.Recent / s.Base
		s.Ratio = &ratio
	}
}

func pageviews(ctx context.Context, c Clients, project, title string, cik int) (*AttentionSeries, error) {
	end := time.Now().UTC().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -120)
	u := fmt.Sprintf("https://wikimedia.org/api/rest_v1/metrics/pageviews/per-article/%s/all-access/user/%s/daily/%s/%s",
		project, url.PathEscape(strings.ReplaceAll(title, " ", "_")), start.Format("20060102"), end.Format("20060102"))
	body, err := c.Wiki.Get(ctx, u, fmt.Sprintf("profile/pv-%s-%d.json", strings.SplitN(project, ".", 2)[0], cik), 12*time.Hour)
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []struct {
			Timestamp string  `json:"timestamp"`
			Views     float64 `json:"views"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	lang := map[string]string{"en.wikipedia": "英文", "zh.wikipedia": "中文"}[project]
	s := &AttentionSeries{Key: "wiki_" + project[:2], Label: "维基百科浏览量（" + lang + "）", Unit: "次/天", Source: "Wikimedia",
		URL: fmt.Sprintf("https://%s.org/wiki/%s", project, url.PathEscape(strings.ReplaceAll(title, " ", "_")))}
	for _, it := range r.Items {
		if len(it.Timestamp) >= 8 {
			d := it.Timestamp[:4] + "-" + it.Timestamp[4:6] + "-" + it.Timestamp[6:8]
			s.Days = append(s.Days, DayValue{d, it.Views})
		}
	}
	s.summarize(end)
	return s, nil
}

func volumeSeries(ctx context.Context, c Clients, ticker string) (*AttentionSeries, error) {
	if c.Yahoo == nil {
		return nil, fmt.Errorf("no yahoo client")
	}
	ch, err := c.Yahoo.DailyChart(ctx, ticker)
	if err != nil {
		return nil, err
	}
	s := &AttentionSeries{Key: "volume", Label: "成交量", Unit: "股/天", Source: "Yahoo"}
	for _, b := range ch.Bars {
		if b.Volume > 0 {
			s.Days = append(s.Days, DayValue{b.Date, b.Volume})
		}
	}
	if len(s.Days) == 0 {
		return nil, fmt.Errorf("没有成交量数据")
	}
	end, _ := time.Parse(time.DateOnly, s.Days[len(s.Days)-1].Date)
	s.summarize(end)
	return s, nil
}

func attention(ctx context.Context, c Clients, co Company) (*Attention, []string) {
	a := &Attention{}
	var errs []string
	zh, en, err := wikiTitles(ctx, c.Wiki, co)
	if err != nil {
		errs = append(errs, "维基百科："+err.Error())
	}
	for _, p := range []struct{ project, title string }{{"en.wikipedia", en}, {"zh.wikipedia", zh}} {
		if p.title == "" {
			continue
		}
		if s, err := pageviews(ctx, c, p.project, p.title, co.CIK); err == nil {
			a.Series = append(a.Series, *s)
		} else {
			errs = append(errs, "维基浏览量："+err.Error())
		}
	}
	if s, err := volumeSeries(ctx, c, co.Ticker); err == nil {
		a.Series = append(a.Series, *s)
	} else {
		errs = append(errs, "成交量："+err.Error())
	}
	order := map[string]int{"volume": 0, "wiki_en": 1, "wiki_zh": 2}
	slices.SortFunc(a.Series, func(x, y AttentionSeries) int { return order[x.Key] - order[y.Key] })
	return a, errs
}
