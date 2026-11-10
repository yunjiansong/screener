package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Industry comparison: the company's quality and valuation figures next to the median of its
// industry peers and the industry's largest company. Only companies that passed L1 have market
// data (and so an industry and a valuation), so peers are "same-industry companies that passed
// the quality layer" – the honest reference for "cheap versus similar-quality businesses".

var peerMetrics = []string{"grossMargin", "opMargin", "fcfMargin", "revenueCagr", "roic", "fcfYield", "multiple", "drawdown"}

type peerRow struct {
	CIK       int                 `json:"cik"`
	Ticker    string              `json:"ticker"`
	Name      string              `json:"name"`
	Sector    string              `json:"-"`
	Industry  string              `json:"-"`
	MarketCap float64             `json:"marketCap"`
	Stage     int                 `json:"stage"`
	M         map[string]*float64 `json:"metrics"`
}

type peerCache struct {
	mu    sync.Mutex
	runID int64
	rows  []peerRow
}

func (s *Server) peerRows(runID int64) ([]peerRow, error) {
	pc := &s.peers
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.runID == runID {
		return pc.rows, nil
	}
	var rows []peerRow
	err := s.st.EachDetail(runID, 1, func(raw []byte) {
		var e struct {
			CIK      int    `json:"cik"`
			Ticker   string `json:"ticker"`
			Name     string `json:"name"`
			Sector   string `json:"sector"`
			Industry string `json:"industry"`
			Stage    int    `json:"stage"`
			Quality  *struct {
				GrossMargin, OpMargin, FCFMargin, RevenueCAGR, ROIC *float64
			} `json:"quality"`
			Valuation *struct {
				MarketCapUSD float64  `json:"marketCapUsd"`
				FCFYield     *float64 `json:"fcfYield"`
				Multiple     *float64 `json:"multiple"`
				Drawdown     float64  `json:"drawdown"`
			} `json:"valuation"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Industry == "" || e.Valuation == nil || e.Quality == nil {
			return
		}
		q, v := e.Quality, e.Valuation
		dd := v.Drawdown
		rows = append(rows, peerRow{CIK: e.CIK, Ticker: e.Ticker, Name: e.Name, Sector: e.Sector, Industry: e.Industry,
			MarketCap: v.MarketCapUSD, Stage: e.Stage, M: map[string]*float64{
				"grossMargin": q.GrossMargin, "opMargin": q.OpMargin, "fcfMargin": q.FCFMargin, "revenueCagr": q.RevenueCAGR,
				"roic": q.ROIC, "fcfYield": v.FCFYield, "multiple": v.Multiple, "drawdown": &dd,
			}})
	})
	if err != nil {
		return nil, err
	}
	pc.runID, pc.rows = runID, rows
	return rows, nil
}

func median(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	sort.Float64s(xs)
	m := xs[len(xs)/2]
	if len(xs)%2 == 0 {
		m = (xs[len(xs)/2-1] + m) / 2
	}
	return &m
}

func (s *Server) companyPeers(w http.ResponseWriter, r *http.Request) {
	out, err := s.peerSummary(r.PathValue("ticker"))
	if err != nil {
		fail(w, err)
		return
	}
	if out == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, out)
}

// peerSummary is the peer comparison for ticker; nil when there is no finished run.
func (s *Server) peerSummary(ticker string) (map[string]any, error) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		return nil, err
	}
	rows, err := s.peerRows(run.ID)
	if err != nil {
		return nil, err
	}
	t := strings.ToUpper(ticker)
	var self *peerRow
	for i := range rows {
		if rows[i].Ticker == t {
			self = &rows[i]
		}
	}
	if self == nil {
		return map[string]any{"available": false, "reason": "没有通过 L1，缺少行情与行业数据"}, nil
	}
	// Industry first; when it has fewer than 3 other companies, widen to the sector.
	group, label := self.Industry, "industry"
	pick := func(match func(p *peerRow) bool) []peerRow {
		var out []peerRow
		for i := range rows {
			if rows[i].Ticker != t && match(&rows[i]) {
				out = append(out, rows[i])
			}
		}
		return out
	}
	peers := pick(func(p *peerRow) bool { return p.Industry == self.Industry })
	if len(peers) < 3 && self.Sector != "" {
		group, label = self.Sector, "sector"
		peers = pick(func(p *peerRow) bool { return p.Sector == self.Sector })
	}
	if len(peers) == 0 {
		return map[string]any{"available": false, "reason": "同行业没有其他通过 L1 的公司", "group": group}, nil
	}
	med := map[string]*float64{}
	for _, k := range peerMetrics {
		var xs []float64
		for _, p := range peers {
			if v := p.M[k]; v != nil {
				xs = append(xs, *v)
			}
		}
		med[k] = median(xs)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].MarketCap > peers[j].MarketCap })
	top := peers
	if len(top) > 3 {
		top = top[:3]
	}
	selfRank := 1
	for _, p := range peers {
		if p.MarketCap > self.MarketCap {
			selfRank++
		}
	}
	return map[string]any{"available": true, "group": group, "level": label, "count": len(peers),
		"self": self, "median": med, "leaders": top, "selfRank": selfRank}, nil
}

var peerLabels = map[string]string{"grossMargin": "毛利率_5年中位", "opMargin": "营业利润率_5年中位", "fcfMargin": "FCF利润率_5年中位",
	"revenueCagr": "收入5年复合增速", "roic": "ROIC_5年中位", "fcfYield": "FCF收益率", "multiple": "市值除以正常化FCF", "drawdown": "较5年高点"}

func peerMetricsCN(m map[string]*float64) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if v != nil {
			out[peerLabels[k]] = float64(int64(*v*1000+0.5*sign(*v))) / 1000
		}
	}
	return out
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// peerPrompt is the peer comparison in the shape the AI prompt uses; nil when unavailable.
func (s *Server) peerPrompt(ticker string) map[string]any {
	d, err := s.peerSummary(ticker)
	if err != nil || d == nil || d["available"] != true {
		return nil
	}
	self := d["self"].(*peerRow)
	scope := "行业"
	if d["level"] == "sector" {
		scope = "板块（同行业不足 3 家，放宽）"
	}
	var leaders []map[string]any
	for _, p := range d["leaders"].([]peerRow) {
		leaders = append(leaders, map[string]any{"代码": p.Ticker, "名称": p.Name, "市值USD": float64(int64(p.MarketCap/1e6)) * 1e6,
			"指标": peerMetricsCN(p.M)})
	}
	return map[string]any{
		"范围":      fmt.Sprintf("%s %s，同行 %d 家（都已通过系统 L1 质量筛选，所以是「质量也过关的同行」，不是全行业）", scope, d["group"], d["count"]),
		"本公司市值排名": d["selfRank"],
		"本公司":     peerMetricsCN(self.M),
		"同行中位":    peerMetricsCN(d["median"].(map[string]*float64)),
		"市值最大的同行": leaders,
		"用途":      "判断下跌是行业性的（同行也跌）还是公司特有的（比同行多跌），以及估值和质量相对同行的位置",
	}
}

// reset drops the cached peer rows (one company's evaluation was replaced).
func (pc *peerCache) reset() {
	pc.mu.Lock()
	pc.runID = 0
	pc.mu.Unlock()
}
