// Package store persists screening runs and the owner's per-company judgements in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"time"

	_ "modernc.org/sqlite"

	"stockscreener/internal/screen"
)

const schema = `
CREATE TABLE IF NOT EXISTS runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at TEXT NOT NULL,
	finished_at TEXT,
	status TEXT NOT NULL,          -- running | done | failed
	message TEXT NOT NULL DEFAULT '',
	config_json TEXT NOT NULL,
	funnel_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS evaluations (
	run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	cik INTEGER NOT NULL,
	ticker TEXT NOT NULL,
	stage INTEGER NOT NULL,
	score REAL NOT NULL,
	summary_json TEXT NOT NULL,
	detail_json TEXT NOT NULL,
	PRIMARY KEY (run_id, cik)
);
CREATE INDEX IF NOT EXISTS evaluations_ticker ON evaluations(run_id, ticker);
-- Judgements survive across runs: they are the human layer 4.
CREATE TABLE IF NOT EXISTS judgements (
	cik INTEGER PRIMARY KEY,
	ticker TEXT NOT NULL,
	verdict TEXT NOT NULL,         -- external | internal | unsure | watch | reject | '' (notes only)
	note TEXT NOT NULL DEFAULT '', -- legacy free-form note
	updated_at TEXT NOT NULL
);`

// Columns added after the first release; ALTER fails harmlessly once they exist.
var migrations = []string{
	`ALTER TABLE judgements ADD COLUMN worry TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE judgements ADD COLUMN why TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE judgements ADD COLUMN falsify TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE judgements ADD COLUMN review_on TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE judgements ADD COLUMN falsify_rules TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE judgements ADD COLUMN snapshot_json TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE ai_diagnosis ADD COLUMN meta_json TEXT NOT NULL DEFAULT '{}'`,
	// Chinese names, filled once by scripts/zh_names.py (Wikidata).
	// Intrinsic-value assumptions the person set on the detail page; cik 0 holds the defaults.
	`CREATE TABLE IF NOT EXISTS value_params (cik INTEGER PRIMARY KEY, json TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS company_zh (cik INTEGER PRIMARY KEY, ticker TEXT NOT NULL, name_zh TEXT NOT NULL, source TEXT NOT NULL)`,
}

const judgementCols = `cik,ticker,verdict,note,worry,why,falsify,review_on,updated_at,falsify_rules,snapshot_json`

type Store struct{ db *sql.DB }

type Run struct {
	ID         int64           `json:"id"`
	StartedAt  string          `json:"startedAt"`
	FinishedAt *string         `json:"finishedAt"`
	Status     string          `json:"status"`
	Message    string          `json:"message"`
	Funnel     json.RawMessage `json:"funnel"`
}

type Judgement struct {
	CIK       int    `json:"cik"`
	Ticker    string `json:"ticker"`
	Verdict   string `json:"verdict"` // may be empty when only notes were written
	Note      string `json:"note"`
	Worry     string `json:"worry"`    // what the market fears
	Why       string `json:"why"`      // why it is temporary / external
	Falsify   string `json:"falsify"`  // what fact would prove the thesis wrong
	ReviewOn  string `json:"reviewOn"` // YYYY-MM-DD
	UpdatedAt string `json:"updatedAt"`
	// FalsifyRules are the quantified falsification conditions, checked on every data refresh.
	FalsifyRules []screen.FalsifyRule `json:"falsifyRules"`
	// Snapshot freezes the numbers the judgement was made on (set by the server on save).
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// Summary is the list-view projection of an Evaluation.
type Summary struct {
	CIK         int           `json:"cik"`
	Ticker      string        `json:"ticker"`
	Name        string        `json:"name"`
	NameZh      string        `json:"nameZh,omitempty"`
	Sector      string        `json:"sector"`
	Industry    string        `json:"industry"`
	Currency    string        `json:"currency"`
	Stage       int           `json:"stage"`
	Score       float64       `json:"score"`
	Quality     float64       `json:"quality"`
	MarketCap   *float64      `json:"marketCap"`
	Drawdown    *float64      `json:"drawdown"`
	FCFYield    *float64      `json:"fcfYield"`
	Percentile  *float64      `json:"percentile"`
	Upside      *float64      `json:"upside"`
	Downside    *float64      `json:"downside"`
	Convexity   *float64      `json:"convexity"`
	FailedCheck string        `json:"failedCheck"` // first failing check, i.e. where it stopped
	Hints       []screen.Hint `json:"hints"`
	Judgement   *Judgement    `json:"judgement,omitempty"`
	AI          *AIBrief      `json:"ai,omitempty"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if _, err := db.Exec(alertsSchema); err != nil {
		return nil, err
	}
	if _, err := db.Exec(aiSchema); err != nil {
		return nil, err
	}
	if _, err := db.Exec(skillsSchema); err != nil {
		return nil, err
	}
	for _, m := range migrations {
		_, _ = db.Exec(m)
	}
	// A process that died mid-run leaves a run marked running forever.
	_, err = db.Exec(`UPDATE runs SET status='failed', message='进程中断' WHERE status='running'`)
	return &Store{db}, err
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) StartRun(config any) (int64, error) {
	cfg, _ := json.Marshal(config)
	res, err := s.db.Exec(`INSERT INTO runs(started_at,status,config_json) VALUES(?,?,?)`, now(), "running", string(cfg))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetMessage(id int64, msg string) {
	_, _ = s.db.Exec(`UPDATE runs SET message=? WHERE id=?`, msg, id)
}

func (s *Store) FinishRun(id int64, runErr error, funnel any) error {
	status, msg := "done", "完成"
	if runErr != nil {
		status, msg = "failed", runErr.Error()
	}
	f, _ := json.Marshal(funnel)
	_, err := s.db.Exec(`UPDATE runs SET status=?, message=?, finished_at=?, funnel_json=? WHERE id=?`, status, msg, now(), string(f), id)
	if err == nil && runErr == nil {
		// Keep the last 10 completed runs.
		_, err = s.db.Exec(`DELETE FROM runs WHERE status='done' AND id NOT IN (SELECT id FROM runs WHERE status='done' ORDER BY id DESC LIMIT 10)`)
	}
	return err
}

func (s *Store) SaveEvaluations(runID int64, evals []*screen.Evaluation) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO evaluations(run_id,cik,ticker,stage,score,summary_json,detail_json) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	for _, e := range evals {
		sum, _ := json.Marshal(Summarize(e))
		det, _ := json.Marshal(e)
		if _, err := stmt.Exec(runID, e.CIK, e.Ticker, e.Stage, e.Score, string(sum), string(det)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func Summarize(e *screen.Evaluation) Summary {
	s := Summary{CIK: e.CIK, Ticker: e.Ticker, Name: e.Name, Sector: e.Sector, Industry: e.Industry,
		Currency: e.Currency, Stage: e.Stage, Score: e.Score, Quality: e.Quality.Score, Hints: e.Hints}
	if v := e.Valuation; v != nil {
		s.MarketCap, s.Drawdown, s.FCFYield = &v.MarketCapUSD, &v.Drawdown, v.FCFYield
		s.Percentile, s.Upside, s.Downside, s.Convexity = v.Percentile, v.Upside, v.Downside, v.Convexity
	}
	for _, c := range e.Checks {
		if !c.Pass {
			s.FailedCheck = c.Name
			break
		}
	}
	return s
}

func (s *Store) LatestRun() (*Run, error) {
	return s.run(`SELECT id,started_at,finished_at,status,message,funnel_json FROM runs ORDER BY id DESC LIMIT 1`)
}

func (s *Store) LatestDoneRun() (*Run, error) {
	return s.run(`SELECT id,started_at,finished_at,status,message,funnel_json FROM runs WHERE status='done' ORDER BY id DESC LIMIT 1`)
}

func (s *Store) run(q string) (*Run, error) {
	var r Run
	var funnel string
	err := s.db.QueryRow(q).Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Message, &funnel)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	r.Funnel = json.RawMessage(funnel)
	return &r, err
}

// Summaries returns rows of a run with stage >= minStage, best score first, joined with judgements.
func (s *Store) Summaries(runID int64, minStage int) ([]Summary, error) {
	rows, err := s.db.Query(`SELECT e.summary_json, j.cik, j.ticker, j.verdict, j.note, j.worry, j.why, j.falsify, j.review_on, j.updated_at, j.falsify_rules, j.snapshot_json
		FROM evaluations e LEFT JOIN judgements j ON j.cik = e.cik
		WHERE e.run_id=? AND e.stage>=? ORDER BY e.stage DESC, e.score DESC`, runID, minStage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var raw string
		var jc sql.NullInt64
		var jt, jv, jn, jw, jy, jf, jr, ju, jrules, jsnap sql.NullString
		if err := rows.Scan(&raw, &jc, &jt, &jv, &jn, &jw, &jy, &jf, &jr, &ju, &jrules, &jsnap); err != nil {
			return nil, err
		}
		var sum Summary
		if err := json.Unmarshal([]byte(raw), &sum); err != nil {
			return nil, err
		}
		if jc.Valid {
			sum.Judgement = &Judgement{CIK: int(jc.Int64), Ticker: jt.String, Verdict: jv.String, Note: jn.String, Worry: jw.String,
				Why: jy.String, Falsify: jf.String, ReviewOn: jr.String, UpdatedAt: ju.String, FalsifyRules: parseRules(jrules.String), Snapshot: rawOrNil(jsnap.String)}
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

func (s *Store) Detail(runID int64, ticker string) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow(`SELECT detail_json FROM evaluations WHERE run_id=? AND ticker=?`, runID, ticker).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

func (s *Store) Judgement(cik int) (*Judgement, error) {
	var j Judgement
	var rules, snap string
	err := s.db.QueryRow(`SELECT `+judgementCols+` FROM judgements WHERE cik=?`, cik).
		Scan(&j.CIK, &j.Ticker, &j.Verdict, &j.Note, &j.Worry, &j.Why, &j.Falsify, &j.ReviewOn, &j.UpdatedAt, &rules, &snap)
	j.FalsifyRules, j.Snapshot = parseRules(rules), rawOrNil(snap)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &j, err
}

func (s *Store) SaveJudgement(j Judgement) (*Judgement, error) {
	j.UpdatedAt = now()
	if j.FalsifyRules == nil {
		j.FalsifyRules = []screen.FalsifyRule{}
	}
	rules, _ := json.Marshal(j.FalsifyRules)
	snap := "{}"
	if len(j.Snapshot) > 0 {
		snap = string(j.Snapshot)
	}
	_, err := s.db.Exec(`INSERT INTO judgements(`+judgementCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(cik) DO UPDATE SET ticker=excluded.ticker, verdict=excluded.verdict, note=excluded.note,
			worry=excluded.worry, why=excluded.why, falsify=excluded.falsify, review_on=excluded.review_on, updated_at=excluded.updated_at,
			falsify_rules=excluded.falsify_rules, snapshot_json=excluded.snapshot_json`,
		j.CIK, j.Ticker, j.Verdict, j.Note, j.Worry, j.Why, j.Falsify, j.ReviewOn, j.UpdatedAt, string(rules), snap)
	return &j, err
}

func (s *Store) DeleteJudgement(cik int) error {
	_, err := s.db.Exec(`DELETE FROM judgements WHERE cik=?`, cik)
	return err
}

func parseRules(s string) []screen.FalsifyRule {
	out := []screen.FalsifyRule{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// Candidates returns the run's stage-3 companies: cik → ticker.
func (s *Store) Candidates(runID int64) (map[int]string, error) {
	rows, err := s.db.Query(`SELECT cik, ticker FROM evaluations WHERE run_id=? AND stage>=3`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var cik int
		var t string
		if err := rows.Scan(&cik, &t); err != nil {
			return nil, err
		}
		out[cik] = t
	}
	return out, rows.Err()
}

// DoneRunsBefore lists finished runs older than id, newest first.
func (s *Store) DoneRunsBefore(id int64) ([]Run, error) {
	rows, err := s.db.Query(`SELECT id,started_at,finished_at,status,message,funnel_json FROM runs WHERE status='done' AND id<? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var funnel string
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Message, &funnel); err != nil {
			return nil, err
		}
		r.Funnel = json.RawMessage(funnel)
		out = append(out, r)
	}
	return out, rows.Err()
}

// JudgementsWithWork returns judgements that have falsify rules or a review date.
func (s *Store) JudgementsWithWork() ([]Judgement, error) {
	rows, err := s.db.Query(`SELECT ` + judgementCols + ` FROM judgements WHERE falsify_rules NOT IN ('', '[]', 'null') OR review_on != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Judgement
	for rows.Next() {
		var j Judgement
		var rules string
		var snap string
		if err := rows.Scan(&j.CIK, &j.Ticker, &j.Verdict, &j.Note, &j.Worry, &j.Why, &j.Falsify, &j.ReviewOn, &j.UpdatedAt, &rules, &snap); err != nil {
			return nil, err
		}
		j.FalsifyRules, j.Snapshot = parseRules(rules), rawOrNil(snap)
		out = append(out, j)
	}
	return out, rows.Err()
}

func rawOrNil(s string) json.RawMessage {
	if s == "" || s == "{}" {
		return nil
	}
	return json.RawMessage(s)
}

// EachDetail calls fn with every evaluation of a run that reached at least minStage.
func (s *Store) EachDetail(runID int64, minStage int, fn func(raw []byte)) error {
	rows, err := s.db.Query(`SELECT detail_json FROM evaluations WHERE run_id=? AND stage>=?`, runID, minStage)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		fn(raw)
	}
	return rows.Err()
}

// ZhNames maps CIK to the company's Chinese name, for the companies that have one.
func (s *Store) ZhNames() map[int]string {
	out := map[int]string{}
	rows, err := s.db.Query(`SELECT cik, name_zh FROM company_zh`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var cik int
		var n string
		if rows.Scan(&cik, &n) == nil {
			out[cik] = n
		}
	}
	return out
}

// ValueParams returns the saved assumptions for cik (0 = defaults), nil when none.
func (s *Store) ValueParams(cik int) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow(`SELECT json FROM value_params WHERE cik=?`, cik).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

// SaveValueParams stores (or, with nil, clears) the assumptions for cik.
func (s *Store) SaveValueParams(cik int, raw json.RawMessage) error {
	if raw == nil {
		_, err := s.db.Exec(`DELETE FROM value_params WHERE cik=?`, cik)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO value_params(cik, json, updated_at) VALUES(?,?,?)
		ON CONFLICT(cik) DO UPDATE SET json=excluded.json, updated_at=excluded.updated_at`, cik, string(raw), now())
	return err
}
