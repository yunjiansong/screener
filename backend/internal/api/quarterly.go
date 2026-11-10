package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"stockscreener/internal/pipeline"
	"stockscreener/internal/screen"
)

// refreshQuarterly: POST /api/companies/{ticker}/quarterly. A foreign filer's latest TTM from
// Yahoo quarterly figures, re-evaluated into the latest run; later screens keep doing it.
func (s *Server) refreshQuarterly(w http.ResponseWriter, r *http.Request) {
	runID, prev, ok := s.latestEvaluation(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	e, err := pipeline.RefreshQuarterly(ctx, s.cfg, prev)
	switch {
	case errors.Is(err, pipeline.ErrHasQuarterly):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, pipeline.ErrNoQuarterly):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		fail(w, err)
		return
	}
	if err := s.st.ReplaceEvaluation(runID, e); err != nil {
		fail(w, err)
		return
	}
	if err := s.st.MarkQuarterly(e.CIK, e.Ticker); err != nil {
		fail(w, err)
		return
	}
	s.peers.reset()
	last := e.TTM[len(e.TTM)-1]
	writeJSON(w, map[string]any{"end": last.End, "stage": e.Stage, "prevStage": prev.Stage})
}

// latestEvaluation loads the path's company from the latest finished run (writing the
// error response itself when it can't).
func (s *Server) latestEvaluation(w http.ResponseWriter, r *http.Request) (int64, *screen.Evaluation, bool) {
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		http.NotFound(w, r)
		return 0, nil, false
	}
	raw, err := s.st.Detail(run.ID, strings.ToUpper(r.PathValue("ticker")))
	if err != nil || raw == nil {
		http.NotFound(w, r)
		return 0, nil, false
	}
	var e screen.Evaluation
	if err := json.Unmarshal(raw, &e); err != nil {
		fail(w, err)
		return 0, nil, false
	}
	return run.ID, &e, true
}

// refreshPrice: POST /api/companies/{ticker}/price. Re-evaluates the company on the latest
// Yahoo price into the latest run (financials unchanged).
func (s *Server) refreshPrice(w http.ResponseWriter, r *http.Request) {
	runID, prev, ok := s.latestEvaluation(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	e, err := pipeline.RefreshPrice(ctx, s.cfg, prev)
	if errors.Is(err, pipeline.ErrNoValuation) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.st.ReplaceEvaluation(runID, e); err != nil {
		fail(w, err)
		return
	}
	s.peers.reset()
	out := map[string]any{"stage": e.Stage, "prevStage": prev.Stage}
	if v := e.Valuation; v != nil {
		out["price"] = v.Price
	}
	if v := prev.Valuation; v != nil {
		out["prevPrice"] = v.Price
	}
	writeJSON(w, out)
}
