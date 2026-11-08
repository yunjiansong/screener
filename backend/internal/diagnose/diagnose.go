// Package diagnose asks an AI agent CLI for a second opinion on a company and keeps every
// answer, so diagnoses made at different times (manual, scheduled, after an alert) can be
// compared and rolled up.
package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/profile"
	"stockscreener/internal/screen"
	"stockscreener/internal/store"
	"stockscreener/internal/yahoo"
)

type Settings struct {
	Engine         string `json:"engine"`         // default engine for diagnoses: codex | claude | agy | grok
	Model          string `json:"model"`          // blank = the CLI's default
	Enabled        bool   `json:"enabled"`        // scheduled diagnoses of the watchlist
	IntervalDays   int    `json:"intervalDays"`   // re-diagnose a watched company after this many days
	OnAlert        bool   `json:"onAlert"`        // diagnose a company right after one of its alerts fires
	AutoRollup     bool   `json:"autoRollup"`     // refresh the rollup after each new diagnosis (≥ 2 on record)
	MaxPerDay      int    `json:"maxPerDay"`      // cap on automatic runs per day, to spare the subscription quota
	TimeoutMinutes int    `json:"timeoutMinutes"` // per run
	// X pulse (Grok with X / web search): scheduled alongside diagnoses when enabled.
	XPulseEnabled bool   `json:"xpulseEnabled"`
	XPulseModel   string `json:"xpulseModel"`
}

func DefaultSettings() Settings {
	return Settings{Engine: "codex", Enabled: true, IntervalDays: 7, OnAlert: true, MaxPerDay: 20, TimeoutMinutes: 8}
}

func LoadSettings(raw json.RawMessage) Settings {
	s := DefaultSettings()
	if raw != nil {
		_ = json.Unmarshal(raw, &s)
	}
	if !validEngine(s.Engine) {
		s.Engine = "codex"
	}
	if s.IntervalDays <= 0 {
		s.IntervalDays = 7
	}
	if s.MaxPerDay <= 0 {
		s.MaxPerDay = 20
	}
	if s.TimeoutMinutes <= 0 {
		s.TimeoutMinutes = 8
	}
	return s
}

type job struct {
	CIK     int    `json:"cik"`
	Ticker  string `json:"ticker"`
	Kind    string `json:"kind"`              // diagnosis | rollup | xpulse
	Trigger string `json:"trigger"`           // manual | schedule | alert:… | auto | batch
	Engine  string `json:"engine"`            // resolved when queued (X pulse is always Grok)
	SkillID int64  `json:"skillId,omitempty"` // kind "skill"
	auto    bool   // counts against MaxPerDay
	done    chan error
}

// Each engine has its own queue and worker, so different CLIs run side by side while each
// one only ever runs a single job at a time.
type Runner struct {
	st     *store.Store
	cfg    config.Config
	yahoo  *yahoo.Client
	pc     profile.Clients
	work   string
	queues map[string]chan job
	mu     sync.Mutex
	queued map[string]job  // key cik/kind/engine → job waiting or running
	active map[string]*job // engine → running job
	batch  *BatchStatus
	// settings overrides the stored settings (tests).
	settings func() Settings
	// Peers returns the peer comparison for a ticker in prompt form (set by the API server).
	Peers func(ticker string) map[string]any
}

func New(cfg config.Config, st *store.Store, yc *yahoo.Client, pc profile.Clients) *Runner {
	work, err := filepath.Abs(filepath.Join(cfg.DataDir, "ai")) // the CLIs get it as their working root
	if err != nil {
		work = filepath.Join(cfg.DataDir, "ai")
	}
	r := &Runner{st: st, cfg: cfg, yahoo: yc, pc: pc, work: work, queues: map[string]chan job{},
		queued: map[string]job{}, active: map[string]*job{}}
	for _, e := range Engines {
		r.queues[e] = make(chan job, 300)
	}
	return r
}

func (r *Runner) Settings() Settings {
	if r.settings != nil {
		return r.settings()
	}
	raw, _ := r.st.AISettings()
	return LoadSettings(raw)
}

func key(cik int, kind, engine string) string { return fmt.Sprintf("%d/%s/%s", cik, kind, engine) }

// keyOf: the de-duplication key; each skill counts as its own kind.
func keyOf(j job) string {
	if j.Kind == "skill" {
		return key(j.CIK, fmt.Sprintf("skill:%d", j.SkillID), j.Engine)
	}
	return key(j.CIK, j.Kind, j.Engine)
}

// Kinds of job, in display order.
var Kinds = []string{"diagnosis", "rollup", "xpulse"}

// Enqueue adds a job unless the same company/kind/engine is already waiting. It reports whether it was added.
// engine "" uses the configured default (X pulse always uses Grok).
func (r *Runner) Enqueue(cik int, ticker, kind, trigger, engine string) bool {
	return r.enqueue(job{CIK: cik, Ticker: strings.ToUpper(ticker), Kind: kind, Trigger: trigger, Engine: engine,
		auto: trigger != "manual" && trigger != "batch"})
}

func ValidEngine(e string) bool { return validEngine(e) }

// manualOnly engines run only when the person asks for them (limited quota): never in
// batches, schedules, alert-triggered or automatic runs.
var manualOnly = map[string]bool{"claude": true}

func ManualOnly(e string) bool { return manualOnly[e] }

// autoEngine is the engine for runs the person did not start: the default, unless that
// one is manual-only, in which case Codex.
func (r *Runner) autoEngine() string {
	if e := r.Settings().Engine; !manualOnly[e] {
		return e
	}
	return "codex"
}

func (r *Runner) enqueue(j job) bool {
	if j.Kind == "xpulse" {
		j.Engine = "grok"
	} else if !validEngine(j.Engine) {
		if j.Trigger == "manual" {
			j.Engine = r.Settings().Engine
		} else {
			j.Engine = r.autoEngine()
		}
	}
	if manualOnly[j.Engine] && j.Trigger != "manual" {
		log.Printf("diagnose: %s %s: %s 只在手动触发时使用，改用 %s", j.Ticker, j.Kind, j.Engine, r.autoEngine())
		j.Engine = r.autoEngine()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := keyOf(j)
	if _, dup := r.queued[k]; dup {
		return false
	}
	select {
	case r.queues[j.Engine] <- j:
		r.queued[k] = j
		return true
	default:
		return false
	}
}

// run queues a job and waits for it to finish (used by batches).
func (r *Runner) runAndWait(ctx context.Context, j job) error {
	j.done = make(chan error, 1)
	if !r.enqueue(j) {
		return fmt.Errorf("已在队列中")
	}
	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type PendingJob struct {
	Kind    string `json:"kind"`
	Engine  string `json:"engine"`
	Active  bool   `json:"active"`
	SkillID int64  `json:"skillId,omitempty"`
}

// Pending lists the company's jobs waiting or running.
func (r *Runner) Pending(cik int) []PendingJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []PendingJob{}
	for _, j := range r.queued {
		if j.CIK != cik {
			continue
		}
		a := r.active[j.Engine]
		out = append(out, PendingJob{j.Kind, j.Engine, a != nil && keyOf(*a) == keyOf(j), j.SkillID})
	}
	order := func(k string) int { return slices.Index(slices.Concat(Kinds, []string{"skill"}), k) }
	slices.SortFunc(out, func(a, b PendingJob) int {
		if d := order(a.Kind) - order(b.Kind); d != 0 {
			return d
		}
		if d := slices.Index(Engines, a.Engine) - slices.Index(Engines, b.Engine); d != 0 {
			return d
		}
		return int(a.SkillID - b.SkillID)
	})
	return out
}

type Status struct {
	Settings Settings        `json:"settings"`
	Engines  []EngineStatus  `json:"engines"`
	Queue    int             `json:"queue"`
	Active   map[string]*job `json:"active"`
	Today    int             `json:"today"` // runs started today (all triggers)
	Batch    *BatchStatus    `json:"batch"`
}

// Status reports settings, queues and the batch; withEngines also probes each CLI (slow).
func (r *Runner) Status(ctx context.Context, withEngines bool) Status {
	r.mu.Lock()
	act := map[string]*job{}
	for e, j := range r.active {
		if j != nil {
			act[e] = j
		}
	}
	var b *BatchStatus
	if r.batch != nil {
		cp := *r.batch
		cp.Companies = append([]BatchCompany(nil), r.batch.Companies...)
		b = &cp
	}
	st := Status{Settings: r.Settings(), Queue: len(r.queued), Active: act, Batch: b}
	r.mu.Unlock()
	for _, e := range Engines {
		if withEngines {
			st.Engines = append(st.Engines, engineStatus(ctx, e))
		}
	}
	st.Today, _ = r.st.DiagnosesSince(dayStart())
	return st
}

func dayStart() string {
	t := time.Now()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).UTC().Format(time.RFC3339)
}

// Loop starts one worker per engine and, every 15 minutes, queues scheduled diagnoses.
func (r *Runner) Loop(ctx context.Context) {
	for _, e := range Engines {
		go r.worker(ctx, e)
	}
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	first := time.After(3 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-tick.C:
		}
		r.schedule()
	}
}

func (r *Runner) schedule() {
	set := r.Settings()
	if !set.Enabled {
		return
	}
	items, err := r.st.Watchlist()
	if err != nil {
		log.Println("diagnose: watchlist:", err)
		return
	}
	due := time.Now().AddDate(0, 0, -set.IntervalDays).UTC().Format(time.RFC3339)
	for _, w := range items {
		// The X pulse goes first so the diagnosis that follows can use it.
		if set.XPulseEnabled {
			if last, err := r.st.LastDiagnosisAt(w.CIK, "xpulse"); err == nil && (last == "" || last < due) {
				r.Enqueue(w.CIK, w.Ticker, "xpulse", "schedule", "grok")
			}
		}
		if last, err := r.st.LastDiagnosisAt(w.CIK, "diagnosis"); err == nil && (last == "" || last < due) {
			r.Enqueue(w.CIK, w.Ticker, "diagnosis", "schedule", "")
		}
	}
}

// AlertFired is the hook the alert scanner calls for each company with newly fired rules.
func (r *Runner) AlertFired(cik int, ticker string, rules []string) {
	if set := r.Settings(); set.OnAlert {
		r.Enqueue(cik, ticker, "diagnosis", "alert:"+strings.Join(rules, "、"), "")
	}
}

func (r *Runner) worker(ctx context.Context, engine string) {
	q := r.queues[engine]
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-q:
			r.mu.Lock()
			r.active[engine] = &j
			r.mu.Unlock()
			err := r.runJob(ctx, j)
			r.mu.Lock()
			r.active[engine] = nil
			delete(r.queued, keyOf(j))
			r.mu.Unlock()
			if j.done != nil {
				j.done <- err
			}
			if err != nil {
				log.Printf("diagnose: %s %s %s (%s): %v", j.Ticker, j.Kind, j.Engine, j.Trigger, err)
			}
		}
	}
}

func (r *Runner) runJob(ctx context.Context, j job) error {
	set := r.Settings()
	if j.auto {
		if n, _ := r.st.DiagnosesSince(dayStart()); n >= set.MaxPerDay {
			return fmt.Errorf("今天的自动诊断已达上限 %d 次，跳过", set.MaxPerDay)
		}
	}
	o := runOpts{Engine: set.Engine, Model: set.Model, Timeout: time.Duration(set.TimeoutMinutes) * time.Minute}
	if j.Engine != "" && j.Engine != o.Engine {
		o.Engine, o.Model = j.Engine, "" // not the default engine: use that CLI's own default model
	}
	if j.Kind == "xpulse" {
		o.Engine, o.Model, o.Search = "grok", set.XPulseModel, true
		o.Timeout = max(o.Timeout, 12*time.Minute) // searching takes longer
	}
	if j.Kind == "skill" {
		return r.runSkill(ctx, j, o)
	}
	d := store.Diagnosis{CIK: j.CIK, Ticker: j.Ticker, Kind: j.Kind, Trigger: j.Trigger, Engine: o.Engine, Model: o.Model}
	start := time.Now()
	var res json.RawMessage
	var err error
	switch j.Kind {
	case "rollup":
		res, d.Model, err = r.rollup(ctx, o, &d)
	case "xpulse":
		res, d.Model, err = r.xpulse(ctx, o, &d)
	default:
		res, d.Model, err = r.diagnose(ctx, o, &d)
	}
	d.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		d.Error = err.Error()
	}
	d.Result = res
	if _, serr := r.st.SaveDiagnosis(d); serr != nil {
		return serr
	}
	if err == nil && j.Kind == "diagnosis" && set.AutoRollup {
		if hist, _ := r.st.Diagnoses(j.CIK, "diagnosis", 20); countOK(hist) >= 2 {
			r.enqueue(job{CIK: j.CIK, Ticker: j.Ticker, Kind: "rollup", Trigger: "auto", auto: true}) // auto engine, never manual-only
		}
	}
	return err
}

func countOK(ds []store.Diagnosis) int {
	n := 0
	for _, d := range ds {
		if d.Error == "" {
			n++
		}
	}
	return n
}

// inputs: everything a prompt about one company is built from.
type inputs struct {
	e     *screen.Evaluation
	p     *profile.Profile
	j     *store.Judgement
	last  *store.Diagnosis
	pulse *store.Diagnosis
	live  *liveQuote
	peers map[string]any
}

// Prompt builds the diagnosis prompt for a company (also offered for copy-paste into a chat).
func (r *Runner) Prompt(ctx context.Context, cik int) (string, *screen.Evaluation, *liveQuote, error) {
	in, err := r.gather(ctx, cik)
	if err != nil {
		return "", nil, nil, err
	}
	return buildPrompt(r.cfg.Convexity, in.e, in.p, in.j, in.last, in.pulse, in.live, in.peers, time.Now()), in.e, in.live, nil
}

func (r *Runner) gather(ctx context.Context, cik int) (*inputs, error) {
	run, err := r.st.LatestDoneRun()
	if err != nil || run == nil {
		return nil, fmt.Errorf("还没有完成的筛选")
	}
	raw, err := r.st.DetailByCIK(run.ID, cik)
	if err != nil || raw == nil {
		return nil, fmt.Errorf("该公司不在最近一次筛选结果中")
	}
	var e screen.Evaluation
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	p := profile.Build(pctx, r.pc, profile.Company{CIK: e.CIK, Ticker: e.Ticker, Name: e.Name})
	cancel()
	j, _ := r.st.Judgement(cik)
	var last *store.Diagnosis
	if hist, _ := r.st.Diagnoses(cik, "diagnosis", 10); len(hist) > 0 {
		for i := range hist {
			if hist[i].Error == "" {
				last = &hist[i]
				break
			}
		}
	}
	var live *liveQuote
	if r.yahoo != nil {
		if ch, err := r.yahoo.DailyChartFresh(ctx, e.Ticker, 30*time.Minute); err == nil && len(ch.Bars) > 0 {
			live = &liveQuote{Price: ch.Price, AsOf: ch.Bars[len(ch.Bars)-1].Date}
			if v := e.Valuation; v != nil && v.High5y > 0 {
				live.Drawdown = ch.Price/math.Max(v.High5y, ch.Price) - 1
			}
		}
	}
	var pulse *store.Diagnosis // a recent X pulse is part of the evidence
	if xs, _ := r.st.Diagnoses(cik, "xpulse", 5); len(xs) > 0 {
		for i := range xs {
			if xs[i].Error == "" && xs[i].At >= time.Now().AddDate(0, 0, -10).UTC().Format(time.RFC3339) {
				pulse = &xs[i]
				break
			}
		}
	}
	var peers map[string]any
	if r.Peers != nil {
		peers = r.Peers(e.Ticker)
	}
	return &inputs{e: &e, p: p, j: j, last: last, pulse: pulse, live: live, peers: peers}, nil
}

func (r *Runner) diagnose(ctx context.Context, o runOpts, d *store.Diagnosis) (json.RawMessage, string, error) {
	prompt, e, live, err := r.Prompt(ctx, d.CIK)
	if err != nil {
		return nil, o.Model, err
	}
	if v := e.Valuation; v != nil {
		d.Basis, d.Price = v.Basis, v.Price
	}
	if live != nil {
		d.Price = live.Price
	}
	out, err := run(ctx, o, r.work, prompt, diagnosisSchema)
	if err != nil {
		return nil, o.Model, err
	}
	if m, err := json.Marshal(out.Meta); err == nil {
		d.Meta = m
	}
	var res Result
	if err := json.Unmarshal(out.JSON, &res); err != nil || res.Summary == "" {
		return out.JSON, out.Model, fmt.Errorf("返回结果缺少必要字段")
	}
	return normalizeFalsifiers(out.JSON), out.Model, nil
}

func (r *Runner) rollup(ctx context.Context, o runOpts, d *store.Diagnosis) (json.RawMessage, string, error) {
	hist, err := r.st.Diagnoses(d.CIK, "diagnosis", 12)
	if err != nil {
		return nil, o.Model, err
	}
	ok := hist[:0:0]
	for _, h := range hist {
		if h.Error == "" {
			ok = append(ok, h)
		}
	}
	if len(ok) < 2 {
		return nil, o.Model, fmt.Errorf("至少需要 2 次成功的诊断才能汇总（现有 %d 次）", len(ok))
	}
	d.Basis, d.Price = ok[0].Basis, ok[0].Price
	name := d.Ticker
	if run, _ := r.st.LatestDoneRun(); run != nil {
		if raw, _ := r.st.DetailByCIK(run.ID, d.CIK); raw != nil {
			var head struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(raw, &head)
			name = head.Name
		}
	}
	out, err := run(ctx, o, r.work, buildRollupPrompt(d.Ticker, name, ok, time.Now()), rollupSchema)
	if err != nil {
		return nil, o.Model, err
	}
	if m, err := json.Marshal(out.Meta); err == nil {
		d.Meta = m
	}
	return normalizeFalsifiers(out.JSON), out.Model, nil
}

// xpulse asks Grok, with X and web search, what X has been saying about the company in the last 30 days.
func (r *Runner) xpulse(ctx context.Context, o runOpts, d *store.Diagnosis) (json.RawMessage, string, error) {
	run0, err := r.st.LatestDoneRun()
	if err != nil || run0 == nil {
		return nil, o.Model, fmt.Errorf("还没有完成的筛选")
	}
	raw, err := r.st.DetailByCIK(run0.ID, d.CIK)
	if err != nil || raw == nil {
		return nil, o.Model, fmt.Errorf("该公司不在最近一次筛选结果中")
	}
	var e screen.Evaluation
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, o.Model, err
	}
	if v := e.Valuation; v != nil {
		d.Basis, d.Price = v.Basis, v.Price
	}
	out, err := run(ctx, o, r.work, buildXPulsePrompt(&e, time.Now()), xpulseSchema)
	if err != nil {
		return nil, o.Model, err
	}
	if m, err := json.Marshal(out.Meta); err == nil {
		d.Meta = m
	}
	var res XPulse
	if err := json.Unmarshal(out.JSON, &res); err != nil || res.Summary == "" {
		return out.JSON, out.Model, fmt.Errorf("返回结果缺少必要字段")
	}
	if n := verifyPosts(res.Posts); n > 0 {
		res.Coverage = strings.TrimSpace(fmt.Sprintf("%s（系统核对：%d / %d 条帖子的链接与日期对不上，已标为可疑）", res.Coverage, n, len(res.Posts)))
	}
	b, _ := json.Marshal(res)
	return b, out.Model, nil
}
