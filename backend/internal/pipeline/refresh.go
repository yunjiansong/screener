package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/fetch"
	"stockscreener/internal/model"
	"stockscreener/internal/screen"
	"stockscreener/internal/yahoo"
)

// ErrHasQuarterly: the company files 10-Q, so SEC already has its latest quarters.
var ErrHasQuarterly = errors.New("该公司提交 10-Q，季报已在 SEC 数据里")

// ErrNoValuation: the screen didn't fetch market data for the company (it failed L1).
var ErrNoValuation = errors.New("这家公司未通过 L1，筛选不拉取它的行情")

// ErrNoQuarterly: Yahoo has no usable four quarters for the company.
var ErrNoQuarterly = errors.New("无法从 Yahoo 得到最新 12 个月")

// RefreshQuarterly re-evaluates one foreign filer with its latest TTM summed from Yahoo
// quarterly figures, as the screen does for companies that pass L1. prev is the company's
// evaluation in the latest run; its peer hints (computed across all companies) carry over.
func RefreshQuarterly(ctx context.Context, cfg config.Config, prev *screen.Evaluation) (*screen.Evaluation, error) {
	c := prev.Company
	if len(c.TTM) > 0 && c.TTM[len(c.TTM)-1].Source != "yahoo" {
		return nil, ErrHasQuarterly
	}
	if len(c.Years) == 0 {
		return nil, ErrNoQuarterly
	}
	yc := yahooClient(cfg)
	c.TTM = nil
	y, err := yahooTTM(ctx, yc, &c)
	if err != nil {
		return nil, fmt.Errorf("%w：%w", ErrNoQuarterly, err)
	}
	if y.End <= c.Years[len(c.Years)-1].End {
		return nil, ErrNoQuarterly
	}
	c.TTM = []model.Year{*y}
	fx, err := loadFX(ctx, yc, []*model.Company{&c}, chartAge)
	if err != nil {
		return nil, err
	}
	e := screen.Evaluate(&c, market(ctx, yc, &c, fx, chartAge), cfg, time.Now())
	for _, h := range prev.Hints {
		if strings.HasPrefix(h.Text, "同行业(") {
			e.Hints = append(e.Hints, h)
		}
	}
	return e, nil
}

func yahooClient(cfg config.Config) *yahoo.Client {
	return yahoo.New(fetch.New("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36", filepath.Join(cfg.DataDir, "cache"), 2))
}

// priceAge: a manual price update refetches a chart older than this (Yahoo's quote is
// itself delayed, so refetching more often gains nothing).
const priceAge = time.Minute

// RefreshPrice re-evaluates one company on the latest Yahoo price (and FX rate), keeping its
// financials from the latest run, so the valuation, upside and stage follow today's price.
func RefreshPrice(ctx context.Context, cfg config.Config, prev *screen.Evaluation) (*screen.Evaluation, error) {
	if prev.Valuation == nil {
		return nil, ErrNoValuation
	}
	c := prev.Company
	yc := yahooClient(cfg)
	fx, err := loadFX(ctx, yc, []*model.Company{&c}, priceAge)
	if err != nil {
		return nil, err
	}
	m := market(ctx, yc, &c, fx, priceAge)
	if m.Chart == nil {
		return nil, fmt.Errorf("Yahoo 没有返回行情：%s", strings.Join(m.Errors, "；"))
	}
	e := screen.Evaluate(&c, m, cfg, time.Now())
	for _, h := range prev.Hints {
		if strings.HasPrefix(h.Text, "同行业(") {
			e.Hints = append(e.Hints, h)
		}
	}
	return e, nil
}
