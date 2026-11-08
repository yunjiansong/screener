package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Skills: analysis templates the person writes. A skill is run against one company's facts
// (no screening stance or guidance); each run keeps the instructions it ran with, so editing
// a skill later doesn't rewrite past results.

const skillsSchema = `
CREATE TABLE IF NOT EXISTS skills (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	instructions TEXT NOT NULL,
	include_json TEXT NOT NULL DEFAULT '[]', -- optional, non-factual material: screen | judgement | lastAI | xpulse
	engine TEXT NOT NULL DEFAULT '',         -- default engine; '' = the AI settings' engine
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skill_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	skill_id INTEGER NOT NULL,
	skill_name TEXT NOT NULL,
	instructions TEXT NOT NULL,
	include_json TEXT NOT NULL DEFAULT '[]',
	cik INTEGER NOT NULL,
	ticker TEXT NOT NULL,
	at TEXT NOT NULL,
	engine TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	price REAL NOT NULL DEFAULT 0,
	basis TEXT NOT NULL DEFAULT '',
	output TEXT NOT NULL DEFAULT '',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	meta_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS skill_runs_cik ON skill_runs(cik, id DESC);`

type Skill struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Include      []string `json:"include"`
	Engine       string   `json:"engine"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
	Runs         int      `json:"runs"` // results saved, all companies
}

type SkillRun struct {
	ID           int64           `json:"id"`
	SkillID      int64           `json:"skillId"`
	SkillName    string          `json:"skillName"`
	Instructions string          `json:"instructions"`
	Include      []string        `json:"include"`
	CIK          int             `json:"cik"`
	Ticker       string          `json:"ticker"`
	At           string          `json:"at"`
	Engine       string          `json:"engine"`
	Model        string          `json:"model"`
	Price        float64         `json:"price"`
	Basis        string          `json:"basis"`
	Output       string          `json:"output"`
	DurationMs   int64           `json:"durationMs"`
	Error        string          `json:"error"`
	Meta         json.RawMessage `json:"meta,omitempty"`
}

var ErrNotFound = errors.New("not found")

func includeJSON(xs []string) string {
	if xs == nil {
		xs = []string{}
	}
	b, _ := json.Marshal(xs)
	return string(b)
}

func (s *Store) Skills() ([]Skill, error) {
	rows, err := s.db.Query(`SELECT s.id,s.name,s.description,s.instructions,s.include_json,s.engine,s.created_at,s.updated_at,
		(SELECT COUNT(*) FROM skill_runs r WHERE r.skill_id=s.id AND r.error='')
		FROM skills s ORDER BY s.name COLLATE NOCASE, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		var k Skill
		var inc string
		if err := rows.Scan(&k.ID, &k.Name, &k.Description, &k.Instructions, &inc, &k.Engine, &k.CreatedAt, &k.UpdatedAt, &k.Runs); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(inc), &k.Include)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) Skill(id int64) (*Skill, error) {
	var k Skill
	var inc string
	err := s.db.QueryRow(`SELECT id,name,description,instructions,include_json,engine,created_at,updated_at FROM skills WHERE id=?`, id).
		Scan(&k.ID, &k.Name, &k.Description, &k.Instructions, &inc, &k.Engine, &k.CreatedAt, &k.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(inc), &k.Include)
	return &k, nil
}

// SaveSkill inserts (ID 0) or updates a skill and returns it as stored.
func (s *Store) SaveSkill(k Skill) (*Skill, error) {
	t := now()
	if k.ID == 0 {
		r, err := s.db.Exec(`INSERT INTO skills(name,description,instructions,include_json,engine,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			k.Name, k.Description, k.Instructions, includeJSON(k.Include), k.Engine, t, t)
		if err != nil {
			return nil, err
		}
		if k.ID, err = r.LastInsertId(); err != nil {
			return nil, err
		}
	} else {
		r, err := s.db.Exec(`UPDATE skills SET name=?,description=?,instructions=?,include_json=?,engine=?,updated_at=? WHERE id=?`,
			k.Name, k.Description, k.Instructions, includeJSON(k.Include), k.Engine, t, k.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.Skill(k.ID)
}

// DeleteSkill removes the skill; its saved results stay (they carry their own copy of the instructions).
func (s *Store) DeleteSkill(id int64) error {
	r, err := s.db.Exec(`DELETE FROM skills WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SaveSkillRun(x SkillRun) (int64, error) {
	if x.At == "" {
		x.At = now()
	}
	if x.Meta == nil {
		x.Meta = json.RawMessage("{}")
	}
	r, err := s.db.Exec(`INSERT INTO skill_runs(skill_id,skill_name,instructions,include_json,cik,ticker,at,engine,model,price,basis,output,duration_ms,error,meta_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, x.SkillID, x.SkillName, x.Instructions, includeJSON(x.Include), x.CIK, x.Ticker, x.At,
		x.Engine, x.Model, x.Price, x.Basis, x.Output, x.DurationMs, x.Error, string(x.Meta))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// SkillRuns returns a company's skill results, newest first.
func (s *Store) SkillRuns(cik int, limit int) ([]SkillRun, error) {
	rows, err := s.db.Query(`SELECT id,skill_id,skill_name,instructions,include_json,cik,ticker,at,engine,model,price,basis,output,duration_ms,error,meta_json
		FROM skill_runs WHERE cik=? ORDER BY id DESC LIMIT ?`, cik, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillRun{}
	for rows.Next() {
		var x SkillRun
		var inc, meta string
		if err := rows.Scan(&x.ID, &x.SkillID, &x.SkillName, &x.Instructions, &inc, &x.CIK, &x.Ticker, &x.At, &x.Engine, &x.Model,
			&x.Price, &x.Basis, &x.Output, &x.DurationMs, &x.Error, &meta); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(inc), &x.Include)
		if meta != "{}" && meta != "" {
			x.Meta = json.RawMessage(meta)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSkillRun(id int64) error {
	r, err := s.db.Exec(`DELETE FROM skill_runs WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
