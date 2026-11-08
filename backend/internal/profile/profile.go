// Package profile builds the "what does this company do" card for the detail page:
// a Chinese business overview, revenue breakdowns from the latest annual XBRL filing,
// and recent news. Everything is fetched on demand, cached, and each part fails alone.
//
// Sources (all free):
//   - SEC submissions + latest 10-K/20-F/40-F: business section text and XBRL revenue by segment/product/region
//   - Wikidata → Chinese Wikipedia intro, when the company has a zh article
//   - Google News RSS (zh-CN, falling back to English) for headlines
//   - translate.googleapis.com (unofficial) to render English text in Chinese
package profile

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"stockscreener/internal/fetch"
	"stockscreener/internal/yahoo"
)

type Profile struct {
	Overview   *Overview  `json:"overview"`
	Wiki       *Wiki      `json:"wiki"`
	Revenue    *Revenue   `json:"revenue"`
	Attention  *Attention `json:"attention"`
	News       []NewsItem `json:"news"`
	NewsLang   string     `json:"newsLang"`   // zh | en
	NewsHidden int        `json:"newsHidden"` // low-quality or duplicate items dropped
	Summary    string     `json:"summary"`    // one-sentence description
	Errors     []string   `json:"errors"`
	FetchedAt  string     `json:"fetchedAt"`
}

type Overview struct {
	Text       string `json:"text"`       // Chinese (machine translated)
	Original   string `json:"original"`   // English excerpt it was translated from
	Translated bool   `json:"translated"` // false when translation failed and Text is English
	Form       string `json:"form"`
	FiledAt    string `json:"filedAt"`
	URL        string `json:"url"`
}

type Wiki struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	URL   string `json:"url"`
}

type Clients struct {
	SEC   *fetch.Client // SEC asks for a contact User-Agent
	Web   *fetch.Client // everything else
	Wiki  *fetch.Client // Wikimedia also asks for a descriptive User-Agent
	Yahoo *yahoo.Client // daily closes, for the price reaction to news
}

type Company struct {
	CIK    int
	Ticker string
	Name   string
}

// Build fetches all parts in parallel.
func Build(ctx context.Context, c Clients, co Company) *Profile {
	p := &Profile{FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	var mu sync.Mutex
	fail := func(part string, err error) {
		mu.Lock()
		p.Errors = append(p.Errors, part+"："+err.Error())
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		f, err := latestAnnual(ctx, c.SEC, co.CIK)
		if err != nil {
			fail("年报", err)
			return
		}
		var inner sync.WaitGroup
		inner.Go(func() {
			ov, err := overview(ctx, c, f)
			if err != nil {
				fail("业务介绍", err)
				return
			}
			p.Overview = ov
		})
		inner.Go(func() {
			rv, err := revenue(ctx, c, f)
			if err != nil {
				fail("收入构成", err)
				return
			}
			p.Revenue = rv
		})
		inner.Wait()
	})
	wg.Go(func() {
		w, err := wiki(ctx, c.Wiki, co)
		if err != nil {
			fail("维基百科", err)
		}
		p.Wiki = w
		zhName := ""
		if w != nil {
			zhName = w.Title
		}
		items, lang, hidden, err := news(ctx, c, co, zhName)
		if err != nil {
			fail("新闻", err)
			return
		}
		p.News, p.NewsLang, p.NewsHidden = items, lang, hidden
	})
	wg.Go(func() {
		a, errs := attention(ctx, c, co)
		mu.Lock()
		p.Attention = a
		p.Errors = append(p.Errors, errs...)
		mu.Unlock()
	})
	wg.Wait()
	p.Summary = summarize(p)
	return p
}

// ---------- SEC filing lookup ----------

type filing struct {
	CIK       int
	Form      string
	Accession string // with dashes
	Primary   string
	FiledAt   string
	ReportAt  string
}

func (f filing) dir() string {
	return fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%d/%s/", f.CIK, strings.ReplaceAll(f.Accession, "-", ""))
}

func latestAnnual(ctx context.Context, sec *fetch.Client, cik int) (*filing, error) {
	body, err := sec.Get(ctx, fmt.Sprintf("https://data.sec.gov/submissions/CIK%010d.json", cik), fmt.Sprintf("profile/submissions-%d.json", cik), 24*time.Hour)
	if err != nil {
		return nil, err
	}
	var s struct {
		Filings struct {
			Recent struct {
				Form       []string `json:"form"`
				Accession  []string `json:"accessionNumber"`
				Primary    []string `json:"primaryDocument"`
				FilingDate []string `json:"filingDate"`
				ReportDate []string `json:"reportDate"`
			} `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	r := s.Filings.Recent
	for i, form := range r.Form {
		switch form {
		case "10-K", "20-F", "40-F", "10-KT":
			return &filing{cik, form, r.Accession[i], r.Primary[i], r.FilingDate[i], r.ReportDate[i]}, nil
		}
	}
	return nil, fmt.Errorf("近期没有 10-K / 20-F / 40-F")
}

// ---------- translation ----------

// translate renders English text in Simplified Chinese via the free gtx endpoint,
// chunked by line so each request stays small. Results are cached by content hash.
func translate(ctx context.Context, web *fetch.Client, text string) (string, error) {
	var out strings.Builder
	var chunk strings.Builder
	flush := func() error {
		if chunk.Len() == 0 {
			return nil
		}
		s, err := translateChunk(ctx, web, chunk.String())
		if err != nil {
			return err
		}
		out.WriteString(s)
		chunk.Reset()
		return nil
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if chunk.Len()+len(line) > 1800 {
			if err := flush(); err != nil {
				return "", err
			}
		}
		chunk.WriteString(line)
	}
	if err := flush(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func translateChunk(ctx context.Context, web *fetch.Client, text string) (string, error) {
	h := sha1.Sum([]byte(text))
	u := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=zh-CN&dt=t&q=" + url.QueryEscape(text)
	body, err := web.Get(ctx, u, "profile/tr-"+hex.EncodeToString(h[:8])+".json", 365*24*time.Hour)
	if err != nil {
		return "", err
	}
	// Shape: [[["译文","source",...], ...], ...]
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || len(raw) == 0 {
		return "", fmt.Errorf("翻译结果无法解析")
	}
	var parts [][]any
	if err := json.Unmarshal(raw[0], &parts); err != nil {
		return "", fmt.Errorf("翻译结果无法解析")
	}
	var b strings.Builder
	for _, p := range parts {
		if len(p) > 0 {
			if s, ok := p[0].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String(), nil
}

// NewClients builds the HTTP clients a profile needs. secUserAgent must carry contact
// details (SEC policy); the other sources get generic agents.
func NewClients(secUserAgent, cacheDir string) Clients {
	return Clients{
		SEC:   fetch.New(secUserAgent, cacheDir, 4),
		Web:   fetch.New("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36", cacheDir, 4).HTTP1(),
		Wiki:  fetch.New("StockScreener/0.1 (personal research tool)", cacheDir, 2),
		Yahoo: yahoo.New(fetch.New("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36", cacheDir, 4)),
	}
}

// summarize picks one sentence: the Chinese Wikipedia intro when it says more than
// "X is a company", otherwise the first sentence of the translated business section.
func summarize(p *Profile) string {
	first := func(text string) string {
		text = strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
		for _, sep := range []string{"。", ". "} {
			if i := strings.Index(text, sep); i > 0 {
				return text[:i+len(sep)]
			}
		}
		return text
	}
	var wiki, ov string
	if p.Wiki != nil {
		wiki = first(p.Wiki.Text)
	}
	if p.Overview != nil && p.Overview.Translated {
		ov = first(p.Overview.Text)
	}
	switch {
	case ov != "" && len([]rune(wiki)) < 60:
		return ov
	case wiki != "":
		return wiki
	}
	return ov
}

// NewsOnly fetches just the headlines (and the Chinese name used to search them), for the
// alert scanner, which must not pull filings on every scan.
func NewsOnly(ctx context.Context, c Clients, co Company) ([]NewsItem, error) {
	w, _ := wiki(ctx, c.Wiki, co)
	zh := ""
	if w != nil {
		zh = w.Title
	}
	items, _, _, err := news(ctx, c, co, zh)
	return items, err
}
