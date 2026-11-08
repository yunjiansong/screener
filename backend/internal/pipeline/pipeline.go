// Package pipeline runs one full screen: universe → SEC fundamentals → layer 1 → Yahoo market
// data for survivors → layers 2-3 → peer hints → persisted run.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/fetch"
	"stockscreener/internal/model"
	"stockscreener/internal/screen"
	"stockscreener/internal/sec"
	"stockscreener/internal/store"
	"stockscreener/internal/yahoo"
)

type Funnel struct {
	Universe         int     `json:"universe"`         // operating companies listed on the chosen exchanges
	WithFundamentals int     `json:"withFundamentals"` // annual cash-flow data found in SEC XBRL
	Quality          int     `json:"quality"`          // passed layer 1
	Dislocation      int     `json:"dislocation"`      // passed layers 1-2
	Convexity        int     `json:"convexity"`        // passed layers 1-3
	MarketFetched    int     `json:"marketFetched"`
	Seconds          float64 `json:"seconds"`
}

// Run executes the screen and stores it; report receives human-readable progress.
func Run(ctx context.Context, cfg config.Config, st *store.Store, report func(string)) (*Funnel, error) {
	started := time.Now()
	runID, err := st.StartRun(cfg)
	if err != nil {
		return nil, err
	}
	say := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		st.SetMessage(runID, msg)
		if report != nil {
			report(msg)
		}
	}
	funnel, evals, err := run(ctx, cfg, say)
	if err == nil {
		funnel.Seconds = time.Since(started).Seconds()
		say("保存 %d 条结果", len(evals))
		err = st.SaveEvaluations(runID, evals)
	}
	if ferr := st.FinishRun(runID, err, funnel); err == nil {
		err = ferr
	}
	return funnel, err
}

func run(ctx context.Context, cfg config.Config, say func(string, ...any)) (*Funnel, []*screen.Evaluation, error) {
	cache := filepath.Join(cfg.DataDir, "cache")
	secClient := fetch.New(cfg.SECUserAgent, cache, 8)
	yc := yahoo.New(fetch.New("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36", cache, 5))
	f := &Funnel{}

	say("读取 SEC 上市公司列表")
	listings, err := sec.Listings(ctx, secClient, cfg.Exchanges)
	if err != nil {
		return f, nil, fmt.Errorf("SEC 公司列表: %w", err)
	}
	f.Universe = len(listings)

	zipPath := cfg.FactsZip
	if zipPath == "" {
		say("下载 SEC 全量财务数据包（约 1.4GB，%d 小时内只下载一次）", cfg.FactsMaxAgeH)
		if zipPath, err = sec.DownloadFacts(ctx, secClient, time.Duration(cfg.FactsMaxAgeH)*time.Hour); err != nil {
			return f, nil, fmt.Errorf("SEC 财务数据包: %w", err)
		}
	}

	say("解析 %d 家公司的年度财报", len(listings))
	companies, err := sec.ParseFacts(zipPath, listings, 12)
	if err != nil {
		return f, nil, err
	}

	now := time.Now()
	var evals []*screen.Evaluation
	var selected []*model.Company
	for _, c := range companies {
		if len(c.Years) == 0 {
			continue
		}
		e := screen.Evaluate(c, nil, cfg, now)
		evals = append(evals, e)
		if e.Stage >= 1 || slices.Contains(cfg.AlwaysInclude, c.Ticker) {
			selected = append(selected, c)
		}
	}
	f.WithFundamentals = len(evals)

	fx, err := loadFX(ctx, yc, selected)
	if err != nil {
		return f, nil, err
	}

	say("获取 %d 家公司的行情与市值", len(selected))
	byCIK := map[int]*screen.Evaluation{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan *model.Company)
	done := 0
	for range 6 {
		wg.Go(func() {
			for c := range jobs {
				// Foreign filers: latest TTM from Yahoo quarterly figures when SEC has only annual data.
				if len(c.TTM) == 0 && c.Years[len(c.Years)-1].End < now.AddDate(0, 0, -100).Format(time.DateOnly) {
					if y, err := yahooTTM(ctx, yc, c); err == nil {
						c.TTM = []model.Year{*y}
					}
				}
				e := screen.Evaluate(c, market(ctx, yc, c, fx), cfg, now)
				mu.Lock()
				byCIK[c.CIK] = e
				done++
				if done%50 == 0 {
					say("行情进度 %d/%d", done, len(selected))
				}
				mu.Unlock()
			}
		})
	}
	for _, c := range selected {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return f, nil, err
	}
	f.MarketFetched = len(byCIK)

	var withMarket []*screen.Evaluation
	for i, e := range evals {
		if m, ok := byCIK[e.CIK]; ok {
			evals[i] = m
			withMarket = append(withMarket, m)
		}
	}
	screen.AddPeerHints(withMarket)
	for _, e := range evals {
		if e.Stage >= 1 {
			f.Quality++
		}
		if e.Stage >= 2 {
			f.Dislocation++
		}
		if e.Stage >= 3 {
			f.Convexity++
		}
	}
	return f, evals, nil
}

func market(ctx context.Context, yc *yahoo.Client, c *model.Company, fx map[string]*yahoo.Chart) *screen.Market {
	m := &screen.Market{}
	if ch := fx[c.Currency]; ch != nil {
		m.FX, m.FXSpot = ch.Bars, ch.Price
	}
	var err error
	if m.Chart, err = yc.MonthlyChart(ctx, c.Ticker); err != nil {
		m.Errors = append(m.Errors, "行情: "+err.Error())
	}
	if m.MarketCap, err = yc.LatestMarketCap(ctx, c.Ticker); err != nil {
		m.Errors = append(m.Errors, "市值: "+err.Error())
	}
	if m.Profile, err = yc.Profile(ctx, c.Ticker); err != nil {
		m.Errors = append(m.Errors, "行业: "+err.Error())
	}
	return m
}

// loadFX fetches monthly "units per USD" history and the latest rate for every non-USD
// reporting currency.
func loadFX(ctx context.Context, yc *yahoo.Client, companies []*model.Company) (map[string]*yahoo.Chart, error) {
	out := map[string]*yahoo.Chart{}
	for _, c := range companies {
		if c.Currency == "USD" || out[c.Currency] != nil {
			continue
		}
		ch, err := yc.MonthlyChart(ctx, c.Currency+"=X")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			out[c.Currency] = &yahoo.Chart{} // valuation will report the missing rate
			continue
		}
		out[c.Currency] = ch
	}
	return out, nil
}
