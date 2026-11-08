// Package api serves the JSON API and the built frontend.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"stockscreener/internal/alerts"
	"stockscreener/internal/calendar"
	"stockscreener/internal/config"
	"stockscreener/internal/diagnose"
	"stockscreener/internal/fetch"
	"stockscreener/internal/pipeline"
	"stockscreener/internal/profile"
	"stockscreener/internal/screen"
	"stockscreener/internal/store"
	"stockscreener/internal/yahoo"
)

type Server struct {
	cfg     config.Config
	st      *store.Store
	mu      sync.Mutex
	running bool
	ctx     context.Context
	pc      profile.Clients
	Alerts  *alerts.Scanner
	AI      *diagnose.Runner
	cal     *calendar.Service
	peers   peerCache
}

func New(ctx context.Context, cfg config.Config, st *store.Store) *Server {
	cache := filepath.Join(cfg.DataDir, "cache")
	pc := profile.NewClients(cfg.SECUserAgent, cache)
	yc := yahoo.New(fetch.New("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36", cache, 4))
	browser := fetch.New("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130 Safari/537.36", cache, 4)
	srv := &Server{cfg: cfg, st: st, ctx: ctx, pc: pc, Alerts: alerts.New(cfg, st, yc, pc), AI: diagnose.New(cfg, st, yc, pc),
		cal: calendar.New(browser, pc.SEC)}
	srv.Alerts.OnFired = srv.AI.AlertFired
	srv.AI.Peers = srv.peerPrompt
	return srv
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.cfg) })
	mux.HandleFunc("GET /api/companies", s.companies)
	mux.HandleFunc("GET /api/companies/{ticker}", s.company)
	mux.HandleFunc("GET /api/companies/{ticker}/peers", s.companyPeers)
	mux.HandleFunc("GET /api/companies/{ticker}/value-params", s.getValueParams)
	mux.HandleFunc("PUT /api/companies/{ticker}/value-params", s.putValueParams)
	mux.HandleFunc("PUT /api/value-params/default", s.putValueParams)
	mux.HandleFunc("GET /api/home", s.home)
	mux.HandleFunc("GET /api/earnings", s.earnings)
	mux.HandleFunc("GET /api/falsify/metrics", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, screen.FalsifyMetricDefs) })
	mux.HandleFunc("GET /api/companies/{ticker}/profile", s.profile)
	mux.HandleFunc("PUT /api/judgements/{cik}", s.saveJudgement)
	mux.HandleFunc("DELETE /api/judgements/{cik}", s.deleteJudgement)
	mux.HandleFunc("GET /api/watchlist", s.watchlist)
	mux.HandleFunc("PUT /api/watchlist/{cik}", s.putWatch)
	mux.HandleFunc("DELETE /api/watchlist/{cik}", s.deleteWatch)
	mux.HandleFunc("GET /api/alerts/settings", s.getAlertSettings)
	mux.HandleFunc("PUT /api/alerts/settings", s.putAlertSettings)
	mux.HandleFunc("POST /api/alerts/scan", s.scanAlerts)
	mux.HandleFunc("POST /api/alerts/test", s.testAlert)
	mux.HandleFunc("GET /api/alerts/log", s.alertLog)
	mux.HandleFunc("GET /api/companies/{ticker}/diagnoses", s.diagnoses)
	mux.HandleFunc("POST /api/companies/{ticker}/diagnoses", s.requestDiagnosis)
	mux.HandleFunc("GET /api/companies/{ticker}/diagnoses/prompt", s.diagnosisPrompt)
	mux.HandleFunc("DELETE /api/diagnoses/{id}", s.deleteDiagnosis)
	mux.HandleFunc("GET /api/skills", s.listSkills)
	mux.HandleFunc("POST /api/skills", s.saveSkill)
	mux.HandleFunc("PUT /api/skills/{id}", s.saveSkill)
	mux.HandleFunc("DELETE /api/skills/{id}", s.deleteSkill)
	mux.HandleFunc("GET /api/companies/{ticker}/skill-runs", s.skillRuns)
	mux.HandleFunc("POST /api/companies/{ticker}/skills/{id}/run", s.runSkill)
	mux.HandleFunc("GET /api/companies/{ticker}/skill-prompt", s.skillPrompt)
	mux.HandleFunc("DELETE /api/skill-runs/{id}", s.deleteSkillRun)
	mux.HandleFunc("GET /api/ai/status", s.aiStatus)
	mux.HandleFunc("PUT /api/ai/settings", s.putAISettings)
	mux.HandleFunc("POST /api/ai/batch", s.startAIBatch)
	mux.Handle("/", spa(s.cfg.FrontendDir))
	return mux
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	latest, err := s.st.LatestRun()
	if err != nil {
		fail(w, err)
		return
	}
	done, err := s.st.LatestDoneRun()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"latest": latest, "lastDone": done})
}

// startRun launches a screen in the background; only one runs at a time.
func (s *Server) startRun(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		http.Error(w, "已有筛选在运行", http.StatusConflict)
		return
	}
	s.running = true
	go func() {
		defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
		if _, err := pipeline.Run(s.ctx, s.cfg, s.st, func(m string) { log.Println(m) }); err != nil {
			log.Println("筛选失败:", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) companies(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		writeJSON(w, map[string]any{"run": nil, "items": []any{}})
		return
	}
	minStage, _ := strconv.Atoi(r.URL.Query().Get("minStage"))
	items, err := s.st.Summaries(run.ID, minStage)
	if err != nil {
		fail(w, err)
		return
	}
	if briefs, err := s.st.AIBriefs(); err == nil {
		for i := range items {
			items[i].AI = briefs[items[i].CIK]
		}
	}
	zh := s.st.ZhNames()
	for i := range items {
		items[i].NameZh = zh[items[i].CIK]
	}
	writeJSON(w, map[string]any{"run": run, "items": items})
}

func (s *Server) company(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		http.NotFound(w, r)
		return
	}
	detail, err := s.st.Detail(run.ID, strings.ToUpper(r.PathValue("ticker")))
	if err != nil {
		fail(w, err)
		return
	}
	if detail == nil {
		http.Error(w, "该代码不在本次筛选范围内（非 NYSE/Nasdaq 运营公司，或 SEC 无年度现金流数据）", http.StatusNotFound)
		return
	}
	var head struct {
		CIK int `json:"cik"`
	}
	_ = json.Unmarshal(detail, &head)
	j, err := s.st.Judgement(head.CIK)
	if err != nil {
		fail(w, err)
		return
	}
	watch, err := s.st.WatchItem(head.CIK)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"evaluation": detail, "judgement": j, "run": run, "watching": watch != nil,
		"nameZh": s.st.ZhNames()[head.CIK]})
}

// profile returns the business overview, revenue breakdown and news; slow on first view
// (several SEC/Wikipedia/news requests), cached afterwards.
func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		http.NotFound(w, r)
		return
	}
	detail, err := s.st.Detail(run.ID, strings.ToUpper(r.PathValue("ticker")))
	if err != nil || detail == nil {
		http.NotFound(w, r)
		return
	}
	var head struct {
		CIK    int    `json:"cik"`
		Ticker string `json:"ticker"`
		Name   string `json:"name"`
	}
	_ = json.Unmarshal(detail, &head)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	writeJSON(w, profile.Build(ctx, s.pc, profile.Company{CIK: head.CIK, Ticker: head.Ticker, Name: head.Name}))
}

var verdicts = map[string]bool{"": true, "external": true, "internal": true, "unsure": true, "watch": true, "reject": true}

var reviewDate = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})?$`)

func (s *Server) saveJudgement(w http.ResponseWriter, r *http.Request) {
	cik, err := strconv.Atoi(r.PathValue("cik"))
	if err != nil {
		http.Error(w, "bad cik", http.StatusBadRequest)
		return
	}
	var j store.Judgement
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&j); err != nil || !verdicts[j.Verdict] || j.Ticker == "" || !reviewDate.MatchString(j.ReviewOn) {
		http.Error(w, "需要 ticker；verdict 为 external/internal/unsure/watch/reject 或留空；reviewOn 为 YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	if len(j.FalsifyRules) > 10 {
		http.Error(w, "证伪条件最多 10 条", http.StatusBadRequest)
		return
	}
	for _, fr := range j.FalsifyRules {
		if !screen.ValidFalsifyRule(fr) {
			http.Error(w, "证伪条件格式不对", http.StatusBadRequest)
			return
		}
	}
	if j.Verdict == "" && strings.TrimSpace(j.Note+j.Worry+j.Why+j.Falsify) == "" && len(j.FalsifyRules) == 0 {
		http.Error(w, "结论和笔记都为空：要清除请用 DELETE", http.StatusBadRequest)
		return
	}
	j.CIK = cik
	j.Snapshot = s.judgementSnapshot(cik, j)
	saved, err := s.st.SaveJudgement(j)
	if err != nil {
		fail(w, err)
		return
	}
	if j.Verdict == "watch" { // "加入观察" puts the company on the alert watchlist
		if err := s.addWatch(cik, j.Ticker, nil); err != nil {
			fail(w, err)
			return
		}
	}
	writeJSON(w, saved)
}

func (s *Server) deleteJudgement(w http.ResponseWriter, r *http.Request) {
	cik, err := strconv.Atoi(r.PathValue("cik"))
	if err != nil {
		http.Error(w, "bad cik", http.StatusBadRequest)
		return
	}
	if err := s.st.DeleteJudgement(cik); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// spa serves dir and falls back to index.html for client-side routes.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean(r.URL.Path))); err != nil {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	log.Println("api error:", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// ---------- watchlist & alerts ----------

func (s *Server) addWatch(cik int, ticker string, rules json.RawMessage) error {
	name, stage := "", 0
	if run, _ := s.st.LatestDoneRun(); run != nil {
		if raw, _ := s.st.DetailByCIK(run.ID, cik); raw != nil {
			var head struct {
				Name  string `json:"name"`
				Stage int    `json:"stage"`
			}
			_ = json.Unmarshal(raw, &head)
			name, stage = head.Name, head.Stage
		}
	}
	return s.st.AddWatch(cik, strings.ToUpper(ticker), name, stage, rules)
}

func (s *Server) watchlist(w http.ResponseWriter, _ *http.Request) {
	items, err := s.st.Watchlist()
	if err != nil {
		fail(w, err)
		return
	}
	raw, err := s.st.AlertSettings()
	if err != nil {
		fail(w, err)
		return
	}
	set := alerts.LoadSettings(raw)
	type row struct {
		store.WatchItem
		Effective alerts.Rules     `json:"effective"`
		Judgement *store.Judgement `json:"judgement"`
		AI        *store.Diagnosis `json:"ai"` // latest successful diagnosis
	}
	out := []row{}
	for _, it := range items {
		var over alerts.Rules
		_ = json.Unmarshal(it.Rules, &over)
		j, _ := s.st.Judgement(it.CIK)
		var ai *store.Diagnosis
		if ds, _ := s.st.Diagnoses(it.CIK, "diagnosis", 10); len(ds) > 0 {
			for i := range ds {
				if ds[i].Error == "" {
					ai = &ds[i]
					break
				}
			}
		}
		out = append(out, row{it, set.Defaults.Merge(over), j, ai})
	}
	writeJSON(w, map[string]any{"items": out, "defaults": set.Defaults})
}

func (s *Server) putWatch(w http.ResponseWriter, r *http.Request) {
	cik, err := strconv.Atoi(r.PathValue("cik"))
	if err != nil {
		http.Error(w, "bad cik", http.StatusBadRequest)
		return
	}
	var body struct {
		Ticker string          `json:"ticker"`
		Rules  json.RawMessage `json:"rules"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || body.Ticker == "" {
		http.Error(w, "需要 ticker", http.StatusBadRequest)
		return
	}
	if body.Rules != nil {
		var probe alerts.Rules
		if err := json.Unmarshal(body.Rules, &probe); err != nil {
			http.Error(w, "rules 格式不对："+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.addWatch(cik, body.Ticker, body.Rules); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteWatch(w http.ResponseWriter, r *http.Request) {
	cik, err := strconv.Atoi(r.PathValue("cik"))
	if err != nil {
		http.Error(w, "bad cik", http.StatusBadRequest)
		return
	}
	if err := s.st.RemoveWatch(cik); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getAlertSettings(w http.ResponseWriter, _ *http.Request) {
	raw, err := s.st.AlertSettings()
	if err != nil {
		fail(w, err)
		return
	}
	set := alerts.LoadSettings(raw)
	hasPassword := set.SMTP.Password != ""
	set.SMTP.Password = "" // never sent back to the browser
	writeJSON(w, map[string]any{"settings": set, "hasPassword": hasPassword, "passwordFromEnv": os.Getenv("SMTP_PASSWORD") != ""})
}

func (s *Server) putAlertSettings(w http.ResponseWriter, r *http.Request) {
	var set alerts.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&set); err != nil {
		http.Error(w, "设置格式不对："+err.Error(), http.StatusBadRequest)
		return
	}
	if set.SMTP.Password == "" { // blank means "keep the stored one"
		raw, _ := s.st.AlertSettings()
		var old alerts.Settings
		if raw != nil && json.Unmarshal(raw, &old) == nil {
			set.SMTP.Password = old.SMTP.Password
		}
	}
	if err := s.st.SaveAlertSettings(set); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scanAlerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := s.Alerts.Scan(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, res)
}

func (s *Server) testAlert(w http.ResponseWriter, _ *http.Request) {
	delivery, err := s.Alerts.SendTest()
	if err != nil {
		http.Error(w, "发送失败："+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"delivery": delivery})
}

func (s *Server) alertLog(w http.ResponseWriter, _ *http.Request) {
	logs, err := s.st.AlertLogs(200)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, logs)
}

// ---------- AI diagnosis ----------

// cikOf resolves a ticker to its CIK through the latest screen.
func (s *Server) cikOf(ticker string) (int, string, bool) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		return 0, "", false
	}
	detail, err := s.st.Detail(run.ID, strings.ToUpper(ticker))
	if err != nil || detail == nil {
		return 0, "", false
	}
	var head struct {
		CIK    int    `json:"cik"`
		Ticker string `json:"ticker"`
	}
	_ = json.Unmarshal(detail, &head)
	return head.CIK, head.Ticker, true
}

func (s *Server) diagnoses(w http.ResponseWriter, r *http.Request) {
	cik, _, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	items, err := s.st.Diagnoses(cik, "", 100)
	if err != nil {
		fail(w, err)
		return
	}
	set := s.AI.Settings()
	pending := []diagnose.PendingJob{}
	for _, p := range s.AI.Pending(cik) {
		if p.Kind != "skill" { // skill jobs are listed with the skill results
			pending = append(pending, p)
		}
	}
	writeJSON(w, map[string]any{"items": items, "pending": pending, "engine": set.Engine})
}

func (s *Server) requestDiagnosis(w http.ResponseWriter, r *http.Request) {
	cik, ticker, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "diagnosis"
	}
	if kind != "diagnosis" && kind != "rollup" && kind != "xpulse" || (r.URL.Query().Get("engine") != "" && !diagnose.ValidEngine(r.URL.Query().Get("engine"))) {
		http.Error(w, "kind 为 diagnosis、rollup 或 xpulse；engine 为 codex、claude、agy 或 grok", http.StatusBadRequest)
		return
	}
	if !s.AI.Enqueue(cik, ticker, kind, "manual", r.URL.Query().Get("engine")) {
		http.Error(w, "已经在排队或进行中", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) diagnosisPrompt(w http.ResponseWriter, r *http.Request) {
	cik, _, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	prompt, _, _, err := s.AI.Prompt(ctx, cik)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(prompt + "\n请按以下结构用中文回答：结论（错杀 / 定价合理 / 价值陷阱 / 看不清）与信心 1–5、一句话结论、市场在担心什么、" +
		"这是暂时/外部的还是内在恶化、看多理由、看空理由、证伪条件、接下来要盯什么、数据疑点。\n"))
}

func (s *Server) deleteDiagnosis(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.st.DeleteDiagnosis(id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.AI.Status(r.Context(), r.URL.Query().Get("light") != "1"))
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]{0,64}$`)

func (s *Server) putAISettings(w http.ResponseWriter, r *http.Request) {
	var set diagnose.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&set); err != nil || !modelName.MatchString(set.Model) ||
		!diagnose.ValidEngine(set.Engine) || !modelName.MatchString(set.XPulseModel) {
		http.Error(w, "设置格式不对：engine 为 codex、claude、agy 或 grok，model 只能含字母数字和 . _ - :", http.StatusBadRequest)
		return
	}
	if err := s.st.SaveAISettings(set); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startAIBatch diagnoses the top-N candidates (or the given tickers) with several engines.
func (s *Server) startAIBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Top     int      `json:"top"`
		Tickers []string `json:"tickers"`
		Engines []string `json:"engines"`
		XPulse  bool     `json:"xpulse"`
		Rollup  bool     `json:"rollup"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil || len(body.Engines) == 0 ||
		body.Top < 0 || body.Top > 50 || len(body.Tickers) > 50 {
		http.Error(w, "需要 engines；top 为 1–50，或给 tickers", http.StatusBadRequest)
		return
	}
	for _, e := range body.Engines {
		if !diagnose.ValidEngine(e) {
			http.Error(w, "未知引擎 "+e, http.StatusBadRequest)
			return
		}
	}
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		http.Error(w, "还没有完成的筛选", http.StatusConflict)
		return
	}
	var targets []diagnose.BatchTarget
	if len(body.Tickers) > 0 {
		for _, t := range body.Tickers {
			if cik, ticker, ok := s.cikOf(t); ok {
				targets = append(targets, diagnose.BatchTarget{CIK: cik, Ticker: ticker})
			}
		}
	} else {
		items, err := s.st.Summaries(run.ID, 3) // candidates, ordered by score
		if err != nil {
			fail(w, err)
			return
		}
		for _, it := range items[:min(body.Top, len(items))] {
			targets = append(targets, diagnose.BatchTarget{CIK: it.CIK, Ticker: it.Ticker})
		}
	}
	if len(targets) == 0 {
		http.Error(w, "没有可诊断的公司", http.StatusBadRequest)
		return
	}
	if err := s.AI.StartBatch(s.ctx, targets, body.Engines, body.XPulse, body.Rollup); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ---------- home workbench ----------

type homeRow struct {
	IsNew     bool                `json:"isNew,omitempty"`
	Falsified []screen.FalsifyHit `json:"falsified,omitempty"`
	ReviewDue string              `json:"reviewDue,omitempty"` // the review date, when it is today or past
}

// home returns what the list page needs beyond the summaries: today's to-dos and per-row flags.
func (s *Server) home(w http.ResponseWriter, _ *http.Request) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		writeJSON(w, map[string]any{"rows": map[int]homeRow{}})
		return
	}
	rows := map[int]*homeRow{}
	row := func(cik int) *homeRow {
		if rows[cik] == nil {
			rows[cik] = &homeRow{}
		}
		return rows[cik]
	}
	type item struct {
		CIK    int    `json:"cik"`
		Ticker string `json:"ticker"`
		Text   string `json:"text"`
	}
	var falsified, review []item

	// New / removed candidates against the latest earlier run with a different candidate set
	// (re-running on unchanged data would otherwise always show zero).
	cur, err := s.st.Candidates(run.ID)
	if err != nil {
		fail(w, err)
		return
	}
	var newIn, removed []item
	var baseline *store.Run
	if prev, err := s.st.DoneRunsBefore(run.ID); err == nil {
		for i := range prev {
			old, err := s.st.Candidates(prev[i].ID)
			if err != nil {
				break
			}
			var n, r []item
			for cik, t := range cur {
				if _, ok := old[cik]; !ok {
					n = append(n, item{cik, t, ""})
				}
			}
			for cik, t := range old {
				if _, ok := cur[cik]; !ok {
					r = append(r, item{cik, t, ""})
				}
			}
			if len(n)+len(r) > 0 {
				newIn, removed, baseline = n, r, &prev[i]
				break
			}
		}
	}
	for _, it := range newIn {
		row(it.CIK).IsNew = true
	}

	// Falsification conditions and review dates.
	today := time.Now().Format(time.DateOnly)
	js, err := s.st.JudgementsWithWork()
	if err != nil {
		fail(w, err)
		return
	}
	for _, j := range js {
		if j.ReviewOn != "" && j.ReviewOn <= today {
			row(j.CIK).ReviewDue = j.ReviewOn
			text := "复查日 " + j.ReviewOn
			var snap struct {
				Metrics map[string]float64 `json:"metrics"`
			}
			if json.Unmarshal(j.Snapshot, &snap) == nil && snap.Metrics["price"] > 0 {
				if raw, _ := s.st.DetailByCIK(run.ID, j.CIK); raw != nil {
					var e screen.Evaluation
					if json.Unmarshal(raw, &e) == nil {
						if now := screen.FalsifyMetrics(&e, 0)["price"]; now > 0 {
							text += fmt.Sprintf(" · 判断时 $%.2f → $%.2f", snap.Metrics["price"], now)
						}
					}
				}
			}
			review = append(review, item{j.CIK, j.Ticker, text})
		}
		if len(j.FalsifyRules) == 0 {
			continue
		}
		raw, _ := s.st.DetailByCIK(run.ID, j.CIK)
		if raw == nil {
			continue
		}
		var e screen.Evaluation
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		live := 0.0 // the watchlist scan keeps a fresher price
		if wi, _ := s.st.WatchItem(j.CIK); wi != nil {
			var snap struct {
				Price float64 `json:"price"`
			}
			_ = json.Unmarshal(wi.Snapshot, &snap)
			live = snap.Price
		}
		if hits := screen.EvalFalsify(j.FalsifyRules, screen.FalsifyMetrics(&e, live)); len(hits) > 0 {
			row(j.CIK).Falsified = hits
			falsified = append(falsified, item{j.CIK, j.Ticker, hits[0].Text})
		}
	}
	out := map[int]homeRow{}
	for k, v := range rows {
		out[k] = *v
	}
	sortItems := func(xs []item) {
		sort.Slice(xs, func(a, b int) bool { return xs[a].Ticker < xs[b].Ticker })
	}
	sortItems(newIn)
	sortItems(removed)
	sortItems(falsified)
	sortItems(review)
	writeJSON(w, map[string]any{"rows": out, "falsified": orEmpty(falsified), "review": orEmpty(review),
		"newIn": orEmpty(newIn), "removed": orEmpty(removed), "baseline": baseline})
}

func orEmpty[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// earnings returns next earnings dates for the current candidates (slow on first call).
func (s *Server) earnings(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		writeJSON(w, map[string]any{})
		return
	}
	cur, err := s.st.Candidates(run.ID)
	if err != nil {
		fail(w, err)
		return
	}
	var ts []calendar.Target
	for cik, t := range cur {
		ts = append(ts, calendar.Target{CIK: cik, Ticker: t})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	writeJSON(w, s.cal.Many(ctx, ts))
}

// judgementSnapshot freezes the numbers a judgement was made on, so a review can compare.
func (s *Server) judgementSnapshot(cik int, j store.Judgement) json.RawMessage {
	snap := map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "verdict": j.Verdict, "falsifyRules": j.FalsifyRules}
	if run, _ := s.st.LatestDoneRun(); run != nil {
		if raw, _ := s.st.DetailByCIK(run.ID, cik); raw != nil {
			var e screen.Evaluation
			if json.Unmarshal(raw, &e) == nil {
				live := 0.0
				if wi, _ := s.st.WatchItem(cik); wi != nil {
					var sp struct {
						Price float64 `json:"price"`
					}
					_ = json.Unmarshal(wi.Snapshot, &sp)
					live = sp.Price
				}
				m := screen.FalsifyMetrics(&e, live)
				snap["metrics"] = m
				if v := e.Valuation; v != nil {
					snap["multiple"], snap["basis"] = v.Multiple, v.Basis
				}
			}
		}
	}
	if briefs, err := s.st.AIBriefs(); err == nil && briefs[cik] != nil {
		b := briefs[cik]
		if b.Rollup != nil {
			snap["ai"] = b.Rollup.Stance
		} else {
			for _, e := range []string{"codex", "agy", "claude"} {
				if v, ok := b.Engines[e]; ok {
					snap["ai"] = v.Stance
					break
				}
			}
		}
	}
	out, _ := json.Marshal(snap)
	return out
}

func (s *Server) getValueParams(w http.ResponseWriter, r *http.Request) {
	cik, _, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	own, err := s.st.ValueParams(cik)
	if err != nil {
		fail(w, err)
		return
	}
	def, err := s.st.ValueParams(0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"company": own, "default": def})
}

// putValueParams saves a JSON object of assumptions; the body "null" clears them.
// Without {ticker} in the path it sets the defaults.
func (s *Server) putValueParams(w http.ResponseWriter, r *http.Request) {
	cik := 0
	if t := r.PathValue("ticker"); t != "" {
		c, _, ok := s.cikOf(t)
		if !ok {
			http.NotFound(w, r)
			return
		}
		cik = c
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "需要 JSON 对象（或 null 清除）", http.StatusBadRequest)
		return
	}
	var raw json.RawMessage
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	if err := s.st.SaveValueParams(cik, raw); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
