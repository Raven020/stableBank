package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"gopkg.in/yaml.v3"
)

// Sentinel errors mapped onto HTTP status codes by http.go.
var (
	ErrScenarioNotFound   = errors.New("demo: scenario not found")
	ErrStepNotFound       = errors.New("demo: step not found")
	ErrPreviousStepNotRun = errors.New("demo: previous_step_not_run")
)

// CallAPIResult is the outcome of a call_api step: the recorded HTTP
// status and body, parsed as JSON when possible.
type CallAPIResult struct {
	Status   int `json:"status"`
	Response any `json:"response,omitempty"`
}

// EditRuleResult is the outcome of an edit_rule step.
type EditRuleResult struct {
	RuleID          string                `json:"rule_id"`
	FieldPath       string                `json:"field_path"`
	OldValue        any                   `json:"old_value"`
	NewValue        any                   `json:"new_value"`
	PreviousVersion int                   `json:"previous_version"`
	NewVersion      int                   `json:"new_version"`
	Diff            string                `json:"diff"`
	Examples        []rules.ExampleResult `json:"examples"`
	Warning         string                `json:"warning,omitempty"`
}

// RestoredRule is one entry of a ResetResult.
type RestoredRule struct {
	RuleID          string `json:"rule_id"`
	RestoredVersion int    `json:"restored_version"`
	NewVersion      int    `json:"new_version"`
}

// ResetResult is the outcome of a reset_scenario step.
type ResetResult struct {
	Restored []RestoredRule `json:"restored"`
}

// runRecord is the per-step run state kept in a scenarioState.
type runRecord struct {
	Ran    bool
	RanAt  time.Time
	Result any
	OK     bool
	ErrMsg string
}

// scenarioState is the live, in-memory state of one started scenario.
type scenarioState struct {
	startedAt time.Time
	snapshot  map[string]int // rule_id -> live version at Start time
	runs      map[string]*runRecord
}

// Service is the D8 demo-mode engine: it loads demo/demo-scenarios.yaml,
// runs steps against the real rules engine (edit_rule) and the real HTTP
// mux (call_api, via httptest — no network), and tracks enough state per
// scenario to sequence steps and reset every rule a scenario touched back
// to its pre-demo version.
type Service struct {
	engine  *rules.Engine
	handler http.Handler
	clock   simclock.Clock
	path    string

	mu        sync.RWMutex
	scenarios []Scenario
	byID      map[string]*Scenario
	loadErr   error

	stateMu sync.Mutex
	states  map[string]*scenarioState
}

// New constructs a Service and immediately loads and validates the
// scenario file at path. A load problem (unknown action, missing fields,
// edit_rule on an unregistered rule_id, etc.) does not panic: it is
// recorded and surfaced via LoadError() and by the HTTP layer, so a bad
// demo script cannot take down the rest of the PoC binary. Reload() (also
// reachable via POST /demo/scenarios/{id}/reload) retries the load.
func New(r *rules.Engine, handler http.Handler, clock simclock.Clock, path string) *Service {
	s := &Service{
		engine:  r,
		handler: handler,
		clock:   clock,
		path:    path,
		states:  map[string]*scenarioState{},
	}
	_ = s.Reload()
	return s
}

// Reload re-reads and re-validates the scenario file, replacing the
// in-memory scenario definitions on success. All run state is cleared
// (the definitions it refers to may no longer match). On failure the
// previously loaded scenarios (if any) are left in place and the error is
// both returned and recorded for LoadError().
func (s *Service) Reload() error {
	scenarios, err := loadAndValidate(s.path, s.engine)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadErr = err
	if err != nil {
		return err
	}
	byID := make(map[string]*Scenario, len(scenarios))
	for i := range scenarios {
		byID[scenarios[i].ScenarioID] = &scenarios[i]
	}
	s.scenarios = scenarios
	s.byID = byID

	s.stateMu.Lock()
	s.states = map[string]*scenarioState{}
	s.stateMu.Unlock()
	return nil
}

// LoadError returns the error from the most recent load/reload attempt, or
// nil if the scenario file is currently loaded successfully.
func (s *Service) LoadError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

// Scenarios returns the currently loaded scenario definitions.
func (s *Service) Scenarios() []Scenario {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Scenario, len(s.scenarios))
	copy(out, s.scenarios)
	return out
}

func (s *Service) scenario(id string) (*Scenario, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrScenarioNotFound, id)
	}
	return sc, nil
}

// Start snapshots the live version of every rule referenced by an
// edit_rule step in scenario id, and clears any prior run state, so the
// scenario can be run (and later reset) from a clean baseline.
func (s *Service) Start(ctx context.Context, id string) error {
	sc, err := s.scenario(id)
	if err != nil {
		return err
	}
	_, err = s.startScenario(sc)
	return err
}

// snapshotVersions returns the live version of every rule touched by an
// edit_rule step in sc, keyed by rule_id.
func (s *Service) snapshotVersions(sc *Scenario) (map[string]int, error) {
	snap := make(map[string]int)
	for _, ruleID := range ruleIDsForScenario(sc) {
		rule, err := s.engine.Get(ruleID)
		if err != nil {
			return nil, fmt.Errorf("demo: scenario %q: snapshotting rule %q: %w", sc.ScenarioID, ruleID, err)
		}
		snap[ruleID] = rule.Version
	}
	return snap, nil
}

func (s *Service) startScenario(sc *Scenario) (*scenarioState, error) {
	snap, err := s.snapshotVersions(sc)
	if err != nil {
		return nil, err
	}
	st := &scenarioState{
		startedAt: s.clock.Now(),
		snapshot:  snap,
		runs:      map[string]*runRecord{},
	}
	s.stateMu.Lock()
	s.states[sc.ScenarioID] = st
	s.stateMu.Unlock()
	return st, nil
}

func (s *Service) ensureStarted(sc *Scenario) (*scenarioState, error) {
	s.stateMu.Lock()
	st, ok := s.states[sc.ScenarioID]
	s.stateMu.Unlock()
	if ok {
		return st, nil
	}
	return s.startScenario(sc)
}

// StepRunResponse is the shape returned by RunStep and the HTTP run
// endpoint.
type StepRunResponse struct {
	StepID     string `json:"step_id"`
	Label      string `json:"label"`
	Action     string `json:"action"`
	Narration  string `json:"narration"`
	OK         bool   `json:"ok"`
	Result     any    `json:"result,omitempty"`
	NextStepID string `json:"next_step_id,omitempty"`
}

// RunStep runs one step of scenario id. If the scenario has not been
// started yet, it is auto-started first. Steps must be run in order:
// running step N requires step N-1 to have already run successfully
// (steps at index 0, and reset_scenario steps at any index, are always
// allowed) unless force is true, which bypasses the check for replays.
func (s *Service) RunStep(ctx context.Context, scenarioID, stepID string, force bool) (StepRunResponse, error) {
	sc, err := s.scenario(scenarioID)
	if err != nil {
		return StepRunResponse{}, err
	}
	st, err := s.ensureStarted(sc)
	if err != nil {
		return StepRunResponse{}, err
	}
	idx, step, err := findStep(sc, stepID)
	if err != nil {
		return StepRunResponse{}, err
	}

	if step.Action != ActionResetScenario && idx > 0 && !force {
		prevID := sc.Steps[idx-1].ID
		s.stateMu.Lock()
		prevRec, ran := st.runs[prevID]
		s.stateMu.Unlock()
		if !ran || !prevRec.Ran {
			return StepRunResponse{}, fmt.Errorf("%w: step %q must run before %q", ErrPreviousStepNotRun, prevID, stepID)
		}
	}

	var (
		result any
		runErr error
	)
	switch step.Action {
	case ActionCallAPI:
		result, runErr = s.callAPI(step)
	case ActionEditRule:
		result, runErr = s.editRule(ctx, sc.ScenarioID, step)
	case ActionResetScenario:
		result, runErr = s.resetScenario(ctx, sc, st)
	default:
		runErr = fmt.Errorf("demo: unknown action %q", step.Action)
	}

	rec := &runRecord{
		Ran:    runErr == nil,
		RanAt:  s.clock.Now(),
		Result: result,
		OK:     runErr == nil,
	}
	if runErr != nil {
		rec.ErrMsg = runErr.Error()
	}
	s.stateMu.Lock()
	st.runs[step.ID] = rec
	s.stateMu.Unlock()

	if runErr != nil {
		return StepRunResponse{}, runErr
	}

	nextID := ""
	if idx+1 < len(sc.Steps) {
		nextID = sc.Steps[idx+1].ID
	}
	return StepRunResponse{
		StepID:     step.ID,
		Label:      step.Label,
		Action:     string(step.Action),
		Narration:  step.Narration,
		OK:         true,
		Result:     result,
		NextStepID: nextID,
	}, nil
}

// callAPI builds an *http.Request against s.handler using
// httptest.NewRecorder (no network) and returns its status and parsed (or
// raw) body.
func (s *Service) callAPI(step Step) (CallAPIResult, error) {
	var body []byte
	if step.Request != nil {
		b, err := json.Marshal(step.Request)
		if err != nil {
			return CallAPIResult{}, fmt.Errorf("demo: step %q: marshaling request: %w", step.ID, err)
		}
		body = b
	}

	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(step.Method, step.Path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	result := CallAPIResult{Status: rec.Code}
	respBody := rec.Body.Bytes()
	if len(respBody) > 0 {
		var parsed any
		if err := json.Unmarshal(respBody, &parsed); err == nil {
			result.Response = parsed
		} else {
			result.Response = string(respBody)
		}
	}
	return result, nil
}

// editRule loads the live YAML of step.RuleID, sets step.FieldPath to
// step.NewValue on the parsed yaml.v3 Node tree (preserving comments and
// formatting), and saves the result through the real rules engine.
func (s *Service) editRule(ctx context.Context, scenarioID string, step Step) (EditRuleResult, error) {
	rule, err := s.engine.Get(step.RuleID)
	if err != nil {
		return EditRuleResult{}, fmt.Errorf("demo: step %q: %w", step.ID, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(rule.Raw, &doc); err != nil {
		return EditRuleResult{}, fmt.Errorf("demo: step %q: parsing current YAML for %q: %w", step.ID, step.RuleID, err)
	}
	oldValue, err := setFieldPath(&doc, step.FieldPath, step.NewValue)
	if err != nil {
		return EditRuleResult{}, fmt.Errorf("demo: step %q: %w", step.ID, err)
	}
	newRaw, err := yaml.Marshal(&doc)
	if err != nil {
		return EditRuleResult{}, fmt.Errorf("demo: step %q: re-marshaling %q: %w", step.ID, step.RuleID, err)
	}

	changeNote := fmt.Sprintf("%s/%s: %s", scenarioID, step.ID, step.Label)
	saveResult, err := s.engine.Save(ctx, step.RuleID, newRaw, rules.SaveOptions{
		Author:               "demo-mode",
		ChangeNote:           changeNote,
		AllowFailingExamples: step.AllowFailingExamples,
	})
	if err != nil {
		return EditRuleResult{}, err
	}

	out := EditRuleResult{
		RuleID:          step.RuleID,
		FieldPath:       step.FieldPath,
		OldValue:        oldValue,
		NewValue:        step.NewValue,
		PreviousVersion: saveResult.Previous,
		NewVersion:      saveResult.Version,
		Diff:            saveResult.Diff,
		Examples:        saveResult.Examples,
	}
	for _, ex := range saveResult.Examples {
		if !ex.Passed {
			out.Warning = "one or more of this rule's documented examples no longer pass after this edit"
			break
		}
	}
	return out, nil
}

// resetScenario reactivates the snapshotted (pre-demo) version of every
// rule whose live version has moved on, then clears run state and
// re-snapshots against the just-restored baseline so the scenario is safe
// to run again.
func (s *Service) resetScenario(ctx context.Context, sc *Scenario, st *scenarioState) (ResetResult, error) {
	var restored []RestoredRule
	for _, ruleID := range ruleIDsForScenario(sc) {
		snapVersion, ok := st.snapshot[ruleID]
		if !ok {
			continue
		}
		live, err := s.engine.Get(ruleID)
		if err != nil {
			return ResetResult{}, fmt.Errorf("demo: reset: %w", err)
		}
		if live.Version == snapVersion {
			continue
		}
		result, err := s.engine.Reactivate(ctx, ruleID, snapVersion, "demo reset")
		if err != nil {
			return ResetResult{}, fmt.Errorf("demo: reset: reactivating %q to version %d: %w", ruleID, snapVersion, err)
		}
		restored = append(restored, RestoredRule{
			RuleID:          ruleID,
			RestoredVersion: snapVersion,
			NewVersion:      result.Version,
		})
	}

	freshSnap, err := s.snapshotVersions(sc)
	if err != nil {
		return ResetResult{}, fmt.Errorf("demo: reset: re-snapshotting: %w", err)
	}
	s.stateMu.Lock()
	st.snapshot = freshSnap
	st.runs = map[string]*runRecord{}
	st.startedAt = s.clock.Now()
	s.stateMu.Unlock()

	return ResetResult{Restored: restored}, nil
}
