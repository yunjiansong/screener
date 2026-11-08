// Package config holds every tunable threshold of the screen in one place.
package config

import (
	"encoding/json"
	"errors"
	"os"
)

type Quality struct {
	MinYears        int     `json:"minYears"`        // annual reports needed
	MaxStaleMonths  int     `json:"maxStaleMonths"`  // latest fiscal year end must be this recent
	MinFCFPositive5 int     `json:"minFcfPositive5"` // of the last 5 years
	MinFCFMargin    float64 `json:"minFcfMargin"`    // median FCF / revenue
	MinOpMargin     float64 `json:"minOpMargin"`     // median operating margin
	MinRevenueCAGR  float64 `json:"minRevenueCagr"`  // over the last 5 years
	MaxNetDebtToFCF float64 `json:"maxNetDebtToFcf"` // net debt / 3y average FCF
	MinScore        float64 `json:"minScore"`        // 0-100 quality score
}

type Dislocation struct {
	ExcludeSectors   []string `json:"excludeSectors"` // generic FCF metrics do not fit banks/insurers/REITs
	MinMarketCapUSD  float64  `json:"minMarketCapUsd"`
	MaxDrawdown      float64  `json:"maxDrawdown"`      // price vs 5y high, e.g. -0.30
	MaxPercentile    float64  `json:"maxPercentile"`    // current P/FCF vs own history
	MinFCFYield      float64  `json:"minFcfYield"`      // normalized FCF / market cap
	MinFCFRetention  float64  `json:"minFcfRetention"`  // last-3y avg FCF vs prior years
	MinRevenueRetain float64  `json:"minRevenueRetain"` // latest revenue vs best of last 3 years
	MinLatestFCF     float64  `json:"minLatestFcf"`     // latest-year FCF vs 3y average: averages hide a fresh collapse
}

type Convexity struct {
	MinRatio          float64 `json:"minRatio"`          // upside / downside
	MinUpside         float64 `json:"minUpside"`         // re-rating to the target multiple
	MaxDownside       float64 `json:"maxDownside"`       // downside case may lose at most this much (e.g. -0.6)
	MaxTargetMultiple float64 `json:"maxTargetMultiple"` // cap on the target P/FCF: ex-growth stocks rarely regain old peaks
	FCFShock          float64 `json:"fcfShock"`          // downside case also assumes FCF falls this much
}

type Config struct {
	SECUserAgent  string      `json:"secUserAgent"`
	DataDir       string      `json:"dataDir"`
	FrontendDir   string      `json:"frontendDir"`
	Listen        string      `json:"listen"`
	Exchanges     []string    `json:"exchanges"`
	AlwaysInclude []string    `json:"alwaysInclude"` // tickers that always get market data, to see where they stop
	FactsMaxAgeH  int         `json:"factsMaxAgeHours"`
	FactsZip      string      `json:"factsZip"` // use this companyfacts.zip instead of downloading (testing)
	Quality       Quality     `json:"quality"`
	Dislocation   Dislocation `json:"dislocation"`
	Convexity     Convexity   `json:"convexity"`
}

func Default() Config {
	return Config{
		SECUserAgent:  "StockScreener your-email@example.com", // SEC wants real contact info: set it in config.json or SEC_USER_AGENT,
		DataDir:       "../data",
		FrontendDir:   "../frontend/dist",
		Listen:        "127.0.0.1:8090",
		Exchanges:     []string{"NYSE", "Nasdaq"},
		AlwaysInclude: []string{"JD", "PDD", "LULU", "NVO", "PFE"},
		FactsMaxAgeH:  24 * 7,
		Quality: Quality{MinYears: 5, MaxStaleMonths: 18, MinFCFPositive5: 4, MinFCFMargin: 0.05,
			MinOpMargin: 0.08, MinRevenueCAGR: -0.03, MaxNetDebtToFCF: 4, MinScore: 50},
		Dislocation: Dislocation{ExcludeSectors: []string{"Financial Services", "Real Estate"},
			MinMarketCapUSD: 1e9, MaxDrawdown: -0.30, MaxPercentile: 0.30, MinFCFYield: 0.04,
			MinFCFRetention: 0.8, MinRevenueRetain: 0.9, MinLatestFCF: 0.5},
		Convexity: Convexity{MinRatio: 2, MinUpside: 0.4, MaxDownside: -0.6, MaxTargetMultiple: 30, FCFShock: 0.2},
	}
}

// Load overlays the JSON file at path (if present) on the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, err
		}
	}
	if ua := os.Getenv("SEC_USER_AGENT"); ua != "" {
		cfg.SECUserAgent = ua
	}
	return cfg, nil
}
