package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"stockscreener/internal/diagnose"
	"stockscreener/internal/store"
)

// Skills: the person's own analysis templates, run over a company's facts only.

func (s *Server) listSkills(w http.ResponseWriter, _ *http.Request) {
	items, err := s.st.Skills()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "include": diagnose.IncludeKeys})
}

func skillID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// saveSkill creates (POST /api/skills) or replaces (PUT /api/skills/{id}) a skill.
func (s *Server) saveSkill(w http.ResponseWriter, r *http.Request) {
	var k store.Skill
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&k); err != nil {
		http.Error(w, "请求格式不对："+err.Error(), http.StatusBadRequest)
		return
	}
	k.ID = 0
	if r.PathValue("id") != "" {
		id, ok := skillID(w, r, "id")
		if !ok {
			return
		}
		k.ID = id
	}
	k.Name, k.Description, k.Instructions = strings.TrimSpace(k.Name), strings.TrimSpace(k.Description), strings.TrimSpace(k.Instructions)
	switch {
	case k.Name == "" || utf8.RuneCountInString(k.Name) > 60:
		http.Error(w, "名称必填，60 字以内", http.StatusBadRequest)
		return
	case k.Instructions == "":
		http.Error(w, "分析指令必填", http.StatusBadRequest)
		return
	case utf8.RuneCountInString(k.Description) > 300:
		http.Error(w, "说明 300 字以内", http.StatusBadRequest)
		return
	case k.Engine != "" && !diagnose.ValidEngine(k.Engine):
		http.Error(w, "未知引擎 "+k.Engine, http.StatusBadRequest)
		return
	}
	inc := []string{}
	for _, x := range k.Include {
		if slices.Contains(diagnose.IncludeKeys, x) && !slices.Contains(inc, x) {
			inc = append(inc, x)
		}
	}
	k.Include = inc
	out, err := s.st.SaveSkill(k)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	id, ok := skillID(w, r, "id")
	if !ok {
		return
	}
	if err := s.st.DeleteSkill(id); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// skillRuns: a company's saved skill results and the skill jobs waiting or running.
func (s *Server) skillRuns(w http.ResponseWriter, r *http.Request) {
	cik, _, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	items, err := s.st.SkillRuns(cik, 100)
	if err != nil {
		fail(w, err)
		return
	}
	var pending []diagnose.PendingJob
	for _, p := range s.AI.Pending(cik) {
		if p.Kind == "skill" {
			pending = append(pending, p)
		}
	}
	writeJSON(w, map[string]any{"items": items, "pending": pending, "engine": s.AI.Settings().Engine})
}

// runSkill: POST /api/companies/{ticker}/skills/{id}/run?engine=
func (s *Server) runSkill(w http.ResponseWriter, r *http.Request) {
	cik, ticker, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, ok := skillID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.st.Skill(id); err != nil {
		http.NotFound(w, r)
		return
	}
	engine := r.URL.Query().Get("engine")
	if engine != "" && !diagnose.ValidEngine(engine) {
		http.Error(w, "未知引擎 "+engine, http.StatusBadRequest)
		return
	}
	if !s.AI.EnqueueSkill(cik, ticker, id, engine) {
		http.Error(w, "这个 skill 已经在排队或进行中", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// skillPrompt: the full prompt a skill would send, as text, for copy-paste into a chat.
// Without a skill id it is the facts block alone.
func (s *Server) skillPrompt(w http.ResponseWriter, r *http.Request) {
	cik, _, ok := s.cikOf(r.PathValue("ticker"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var text string
	var err error
	if raw := r.URL.Query().Get("skill"); raw != "" {
		id, perr := strconv.ParseInt(raw, 10, 64)
		sk, serr := s.st.Skill(id)
		if perr != nil || serr != nil {
			http.NotFound(w, r)
			return
		}
		text, _, err = s.AI.SkillPrompt(ctx, cik, sk)
	} else {
		text, err = s.AI.FactsPrompt(ctx, cik)
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

func (s *Server) deleteSkillRun(w http.ResponseWriter, r *http.Request) {
	id, ok := skillID(w, r, "id")
	if !ok {
		return
	}
	if err := s.st.DeleteSkillRun(id); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
