package store

import (
	"encoding/json"

	"stockscreener/internal/screen"
)

// Foreign filers whose latest quarters someone pulled from Yahoo on the detail page. Every
// later screen fetches their quarterly figures too, even when they fail L1.
const quarterlySchema = `
CREATE TABLE IF NOT EXISTS quarterly_refresh (
	cik INTEGER PRIMARY KEY,
	ticker TEXT NOT NULL,
	at TEXT NOT NULL
);`

func (s *Store) MarkQuarterly(cik int, ticker string) error {
	_, err := s.db.Exec(`INSERT INTO quarterly_refresh(cik,ticker,at) VALUES(?,?,?)
		ON CONFLICT(cik) DO UPDATE SET ticker=excluded.ticker, at=excluded.at`, cik, ticker, now())
	return err
}

func (s *Store) QuarterlyCIKs() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT cik FROM quarterly_refresh`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var cik int
		if err := rows.Scan(&cik); err != nil {
			return nil, err
		}
		out[cik] = true
	}
	return out, rows.Err()
}

// ReplaceEvaluation overwrites one company's evaluation within a run.
func (s *Store) ReplaceEvaluation(runID int64, e *screen.Evaluation) error {
	sum, _ := json.Marshal(Summarize(e))
	det, _ := json.Marshal(e)
	r, err := s.db.Exec(`UPDATE evaluations SET stage=?, score=?, summary_json=?, detail_json=? WHERE run_id=? AND cik=?`,
		e.Stage, e.Score, string(sum), string(det), runID, e.CIK)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
