package demo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/rules"
)

// RegisterRoutes wires every /demo HTTP endpoint described in
// CONTRACTS.md onto mux. This whole surface is PoC-only and must not be
// mounted in a production build.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /demo/scenarios", s.handleList)
	mux.HandleFunc("GET /demo/scenarios/{id}", s.handleGet)
	mux.HandleFunc("POST /demo/scenarios/{id}/start", s.handleStart)
	mux.HandleFunc("POST /demo/scenarios/{id}/steps/{step_id}/run", s.handleRunStep)
	mux.HandleFunc("POST /demo/scenarios/{id}/reset", s.handleReset)
	mux.HandleFunc("POST /demo/scenarios/{id}/reload", s.handleReload)
}

// StepBrief is the lightweight per-step shape returned by GET /demo/scenarios.
type StepBrief struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Action    string `json:"action"`
	Narration string `json:"narration"`
}

// ScenarioBrief is the listing shape returned by GET /demo/scenarios.
type ScenarioBrief struct {
	ScenarioID  string      `json:"scenario_id"`
	Description string      `json:"description"`
	Steps       []StepBrief `json:"steps"`
	Started     bool        `json:"started"`
	RanSteps    []string    `json:"ran_steps"`
}

// StepView is the per-step shape returned by GET /demo/scenarios/{id}: the
// full step definition plus its run state.
type StepView struct {
	Step
	Ran        bool       `json:"ran"`
	RanAt      *time.Time `json:"ran_at,omitempty"`
	LastOK     *bool      `json:"last_ok,omitempty"`
	LastResult any        `json:"last_result,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

// ScenarioDetail is the shape returned by GET /demo/scenarios/{id}.
type ScenarioDetail struct {
	ScenarioID  string     `json:"scenario_id"`
	Description string     `json:"description"`
	Steps       []StepView `json:"steps"`
	Started     bool       `json:"started"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
}

func (s *Service) brief(sc *Scenario) ScenarioBrief {
	s.stateMu.Lock()
	st, started := s.states[sc.ScenarioID]
	s.stateMu.Unlock()

	steps := make([]StepBrief, 0, len(sc.Steps))
	var ran []string
	for _, step := range sc.Steps {
		steps = append(steps, StepBrief{ID: step.ID, Label: step.Label, Action: string(step.Action), Narration: step.Narration})
		if started {
			if rec, ok := st.runs[step.ID]; ok && rec.Ran {
				ran = append(ran, step.ID)
			}
		}
	}
	return ScenarioBrief{
		ScenarioID:  sc.ScenarioID,
		Description: sc.Description,
		Steps:       steps,
		Started:     started,
		RanSteps:    ran,
	}
}

func (s *Service) detail(sc *Scenario) ScenarioDetail {
	s.stateMu.Lock()
	st, started := s.states[sc.ScenarioID]
	s.stateMu.Unlock()

	steps := make([]StepView, 0, len(sc.Steps))
	for _, step := range sc.Steps {
		view := StepView{Step: step}
		if started {
			if rec, ok := st.runs[step.ID]; ok {
				view.Ran = rec.Ran
				ranAt := rec.RanAt
				view.RanAt = &ranAt
				ok := rec.OK
				view.LastOK = &ok
				view.LastResult = rec.Result
				view.LastError = rec.ErrMsg
			}
		}
		steps = append(steps, view)
	}
	d := ScenarioDetail{
		ScenarioID:  sc.ScenarioID,
		Description: sc.Description,
		Steps:       steps,
		Started:     started,
	}
	if started {
		startedAt := st.startedAt
		d.StartedAt = &startedAt
	}
	return d
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	if err := s.LoadError(); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "load_failed", err.Error())
		return
	}
	scenarios := s.Scenarios()
	out := make([]ScenarioBrief, 0, len(scenarios))
	for i := range scenarios {
		out = append(out, s.brief(&scenarios[i]))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scenario(r.PathValue("id"))
	if err != nil {
		writeDemoError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.detail(sc))
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Start(r.Context(), id); err != nil {
		writeDemoError(w, err)
		return
	}
	sc, err := s.scenario(id)
	if err != nil {
		writeDemoError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.detail(sc))
}

type runStepRequest struct {
	Force bool `json:"force"`
}

func (s *Service) handleRunStep(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stepID := r.PathValue("step_id")

	var req runStepRequest
	if r.Body != nil {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &req); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
	}

	result, err := s.RunStep(r.Context(), id, stepID, req.Force)
	if err != nil {
		writeDemoError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleReset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sc, err := s.scenario(id)
	if err != nil {
		writeDemoError(w, err)
		return
	}
	st, err := s.ensureStarted(sc)
	if err != nil {
		writeDemoError(w, err)
		return
	}
	result, err := s.resetScenario(r.Context(), sc, st)
	if err != nil {
		writeDemoError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleReload(w http.ResponseWriter, r *http.Request) {
	// id is accepted for symmetry with the other /demo/scenarios/{id}/*
	// endpoints, but reload re-reads the whole file (there is only one).
	if err := s.Reload(); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "load_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"reloaded": true, "scenarios": s.Scenarios()})
}

// writeDemoError maps Service errors onto HTTP status codes.
func writeDemoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrScenarioNotFound), errors.Is(err, ErrStepNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrPreviousStepNotRun):
		httpx.WriteError(w, http.StatusConflict, "previous_step_not_run", err.Error())
	default:
		var ef *rules.ExamplesFailedError
		if errors.As(err, &ef) {
			httpx.WriteError(w, http.StatusConflict, "examples_failed", map[string]any{"examples": ef.Results})
			return
		}
		var ve *rules.ValidationError
		if errors.As(err, &ve) {
			httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", ve.Problems)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
