package store

import (
	"database/sql"
	"encoding/json"
)

// Watchlist, alert settings and alert state. The scanner (package alerts) owns the logic;
// this file only persists it.

const alertsSchema = `
CREATE TABLE IF NOT EXISTS watchlist (
	cik INTEGER PRIMARY KEY,
	ticker TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	added_at TEXT NOT NULL,
	base_stage INTEGER NOT NULL DEFAULT 0,  -- stage when added; falling below it is an alert
	rules_json TEXT NOT NULL DEFAULT '{}',  -- per-company overrides of the default rules
	snapshot_json TEXT NOT NULL DEFAULT '{}', -- last scan's live numbers, for the UI
	scanned_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS alert_settings (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	json TEXT NOT NULL
);
-- Edge-trigger state: an alert fires when its condition turns true, not on every scan.
CREATE TABLE IF NOT EXISTS alert_state (
	cik INTEGER NOT NULL,
	rule TEXT NOT NULL,
	active INTEGER NOT NULL,
	value TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	PRIMARY KEY (cik, rule)
);
CREATE TABLE IF NOT EXISTS alert_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	at TEXT NOT NULL,
	cik INTEGER NOT NULL,
	ticker TEXT NOT NULL,
	rule TEXT NOT NULL,
	message TEXT NOT NULL,
	delivery TEXT NOT NULL DEFAULT ''  -- sent | outbox | error: ...
);
CREATE INDEX IF NOT EXISTS alert_log_at ON alert_log(at DESC);`

type WatchItem struct {
	CIK       int             `json:"cik"`
	Ticker    string          `json:"ticker"`
	Name      string          `json:"name"`
	AddedAt   string          `json:"addedAt"`
	BaseStage int             `json:"baseStage"`
	Rules     json.RawMessage `json:"rules"`
	Snapshot  json.RawMessage `json:"snapshot"`
	ScannedAt string          `json:"scannedAt"`
}

type AlertLog struct {
	ID       int64  `json:"id"`
	At       string `json:"at"`
	CIK      int    `json:"cik"`
	Ticker   string `json:"ticker"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
	Delivery string `json:"delivery"`
}

func (s *Store) Watchlist() ([]WatchItem, error) {
	rows, err := s.db.Query(`SELECT cik,ticker,name,added_at,base_stage,rules_json,snapshot_json,scanned_at FROM watchlist ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WatchItem{}
	for rows.Next() {
		var w WatchItem
		var rules, snap string
		if err := rows.Scan(&w.CIK, &w.Ticker, &w.Name, &w.AddedAt, &w.BaseStage, &rules, &snap, &w.ScannedAt); err != nil {
			return nil, err
		}
		w.Rules, w.Snapshot = json.RawMessage(rules), json.RawMessage(snap)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) WatchItem(cik int) (*WatchItem, error) {
	items, err := s.Watchlist()
	if err != nil {
		return nil, err
	}
	for _, w := range items {
		if w.CIK == cik {
			return &w, nil
		}
	}
	return nil, nil
}

// AddWatch inserts the company if absent (keeping existing rules); rules, when non-nil, replace the overrides.
func (s *Store) AddWatch(cik int, ticker, name string, stage int, rules json.RawMessage) error {
	if rules == nil {
		_, err := s.db.Exec(`INSERT INTO watchlist(cik,ticker,name,added_at,base_stage) VALUES(?,?,?,?,?) ON CONFLICT(cik) DO NOTHING`,
			cik, ticker, name, now(), stage)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO watchlist(cik,ticker,name,added_at,base_stage,rules_json) VALUES(?,?,?,?,?,?)
		ON CONFLICT(cik) DO UPDATE SET rules_json=excluded.rules_json`, cik, ticker, name, now(), stage, string(rules))
	return err
}

func (s *Store) RemoveWatch(cik int) error {
	if _, err := s.db.Exec(`DELETE FROM alert_state WHERE cik=?`, cik); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM watchlist WHERE cik=?`, cik)
	return err
}

func (s *Store) SetSnapshot(cik int, snapshot any) error {
	b, _ := json.Marshal(snapshot)
	_, err := s.db.Exec(`UPDATE watchlist SET snapshot_json=?, scanned_at=? WHERE cik=?`, string(b), now(), cik)
	return err
}

func (s *Store) AlertSettings() (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow(`SELECT json FROM alert_settings WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

func (s *Store) SaveAlertSettings(v any) error {
	b, _ := json.Marshal(v)
	_, err := s.db.Exec(`INSERT INTO alert_settings(id,json) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET json=excluded.json`, string(b))
	return err
}

// AlertState returns whether the rule was active at the last scan and its stored value.
func (s *Store) AlertState(cik int, rule string) (bool, string, error) {
	var active int
	var value string
	err := s.db.QueryRow(`SELECT active,value FROM alert_state WHERE cik=? AND rule=?`, cik, rule).Scan(&active, &value)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	return active == 1, value, err
}

func (s *Store) SetAlertState(cik int, rule string, active bool, value string) error {
	a := 0
	if active {
		a = 1
	}
	_, err := s.db.Exec(`INSERT INTO alert_state(cik,rule,active,value,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(cik,rule) DO UPDATE SET active=excluded.active, value=excluded.value, updated_at=excluded.updated_at`,
		cik, rule, a, value, now())
	return err
}

func (s *Store) LogAlert(cik int, ticker, rule, message, delivery string) error {
	_, err := s.db.Exec(`INSERT INTO alert_log(at,cik,ticker,rule,message,delivery) VALUES(?,?,?,?,?,?)`, now(), cik, ticker, rule, message, delivery)
	return err
}

func (s *Store) AlertLogs(limit int) ([]AlertLog, error) {
	rows, err := s.db.Query(`SELECT id,at,cik,ticker,rule,message,delivery FROM alert_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertLog{}
	for rows.Next() {
		var l AlertLog
		if err := rows.Scan(&l.ID, &l.At, &l.CIK, &l.Ticker, &l.Rule, &l.Message, &l.Delivery); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DetailByCIK returns the company's evaluation in the given run.
func (s *Store) DetailByCIK(runID int64, cik int) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow(`SELECT detail_json FROM evaluations WHERE run_id=? AND cik=?`, runID, cik).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return json.RawMessage(raw), err
}
