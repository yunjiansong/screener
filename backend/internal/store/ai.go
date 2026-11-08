package store

import (
	"database/sql"
	"encoding/json"
)

// AI diagnoses: every run (manual, scheduled, alert-triggered) is kept, so the history can be
// compared and rolled up. Kind "diagnosis" is one look at the company; "rollup" summarizes
// several diagnoses.

const aiSchema = `
CREATE TABLE IF NOT EXISTS ai_diagnosis (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	cik INTEGER NOT NULL,
	ticker TEXT NOT NULL,
	at TEXT NOT NULL,
	kind TEXT NOT NULL,              -- diagnosis | rollup | xpulse
	trigger TEXT NOT NULL,           -- manual | schedule | alert:<rules> | auto
	engine TEXT NOT NULL,            -- codex | claude | agy | grok
	model TEXT NOT NULL DEFAULT '',
	basis TEXT NOT NULL DEFAULT '',  -- which financial period the data referred to
	price REAL NOT NULL DEFAULT 0,
	result_json TEXT NOT NULL DEFAULT '{}',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ai_diagnosis_cik ON ai_diagnosis(cik, id DESC);
CREATE TABLE IF NOT EXISTS ai_settings (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	json TEXT NOT NULL
);`

type Diagnosis struct {
	ID         int64           `json:"id"`
	CIK        int             `json:"cik"`
	Ticker     string          `json:"ticker"`
	At         string          `json:"at"`
	Kind       string          `json:"kind"`
	Trigger    string          `json:"trigger"`
	Engine     string          `json:"engine"`
	Model      string          `json:"model"`
	Basis      string          `json:"basis"`
	Price      float64         `json:"price"`
	Result     json.RawMessage `json:"result"`
	DurationMs int64           `json:"durationMs"`
	Error      string          `json:"error"`
	// Meta: how the call ran (turns, tokens, cost, tools offered), as the engine reported it.
	Meta json.RawMessage `json:"meta,omitempty"`
}

func (s *Store) SaveDiagnosis(d Diagnosis) (int64, error) {
	if d.Result == nil {
		d.Result = json.RawMessage("{}")
	}
	if d.At == "" {
		d.At = now()
	}
	if d.Meta == nil {
		d.Meta = json.RawMessage("{}")
	}
	r, err := s.db.Exec(`INSERT INTO ai_diagnosis(cik,ticker,at,kind,trigger,engine,model,basis,price,result_json,duration_ms,error,meta_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.CIK, d.Ticker, d.At, d.Kind, d.Trigger, d.Engine, d.Model, d.Basis, d.Price,
		string(d.Result), d.DurationMs, d.Error, string(d.Meta))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// Diagnoses returns a company's history, newest first; kind "" means all kinds.
func (s *Store) Diagnoses(cik int, kind string, limit int) ([]Diagnosis, error) {
	rows, err := s.db.Query(`SELECT id,cik,ticker,at,kind,trigger,engine,model,basis,price,result_json,duration_ms,error,meta_json
		FROM ai_diagnosis WHERE cik=? AND (?='' OR kind=?) ORDER BY id DESC LIMIT ?`, cik, kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Diagnosis{}
	for rows.Next() {
		var d Diagnosis
		var res, meta string
		if err := rows.Scan(&d.ID, &d.CIK, &d.Ticker, &d.At, &d.Kind, &d.Trigger, &d.Engine, &d.Model, &d.Basis, &d.Price,
			&res, &d.DurationMs, &d.Error, &meta); err != nil {
			return nil, err
		}
		d.Result = json.RawMessage(res)
		if meta != "{}" && meta != "" {
			d.Meta = json.RawMessage(meta)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LastDiagnosisAt is the time of the company's latest successful run of that kind ("" if none).
func (s *Store) LastDiagnosisAt(cik int, kind string) (string, error) {
	var at sql.NullString
	err := s.db.QueryRow(`SELECT MAX(at) FROM ai_diagnosis WHERE cik=? AND kind=? AND error=''`, cik, kind).Scan(&at)
	return at.String, err
}

// DiagnosesSince counts runs (any company, any outcome) since the given RFC3339 time.
func (s *Store) DiagnosesSince(at string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_diagnosis WHERE at>=?`, at).Scan(&n)
	return n, err
}

func (s *Store) DeleteDiagnosis(id int64) error {
	_, err := s.db.Exec(`DELETE FROM ai_diagnosis WHERE id=?`, id)
	return err
}

func (s *Store) AISettings() (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow(`SELECT json FROM ai_settings WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

func (s *Store) SaveAISettings(v any) error {
	b, _ := json.Marshal(v)
	_, err := s.db.Exec(`INSERT INTO ai_settings(id,json) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET json=excluded.json`, string(b))
	return err
}

// AIBrief is the list-view digest of a company's AI results.
type AIBrief struct {
	Rollup  *Verdict           `json:"rollup,omitempty"`  // latest successful rollup
	Engines map[string]Verdict `json:"engines,omitempty"` // latest successful diagnosis per engine
	XPulse  *int               `json:"xpulse,omitempty"`  // latest X sentiment score (-5…+5)
}

type Verdict struct {
	Stance     string `json:"stance"`
	Confidence int    `json:"confidence"`
	Summary    string `json:"summary"`
	At         string `json:"at"`
}

// AIBriefs returns the digest for every company with at least one successful run.
func (s *Store) AIBriefs() (map[int]*AIBrief, error) {
	rows, err := s.db.Query(`SELECT cik, kind, engine, at, result_json FROM ai_diagnosis WHERE error='' ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]*AIBrief{}
	for rows.Next() {
		var cik int
		var kind, engine, at, res string
		if err := rows.Scan(&cik, &kind, &engine, &at, &res); err != nil {
			return nil, err
		}
		b := out[cik]
		if b == nil {
			b = &AIBrief{Engines: map[string]Verdict{}}
			out[cik] = b
		}
		var r struct {
			Verdict
			SentimentScore *int `json:"sentimentScore"`
		}
		if json.Unmarshal([]byte(res), &r) != nil {
			continue
		}
		r.Verdict.At = at
		switch kind {
		case "rollup":
			if b.Rollup == nil {
				v := r.Verdict
				b.Rollup = &v
			}
		case "xpulse":
			if b.XPulse == nil && r.SentimentScore != nil {
				b.XPulse = r.SentimentScore
			}
		case "diagnosis":
			if _, ok := b.Engines[engine]; !ok {
				b.Engines[engine] = r.Verdict
			}
		}
	}
	return out, rows.Err()
}
