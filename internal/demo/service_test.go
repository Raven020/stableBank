package demo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/dashboard"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/loan"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

func fixedClock() simclock.Fixed {
	return simclock.Fixed(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
}

// newTestEngine builds a real rules.Engine against the committed rules/
// directory, with every domain evaluator registered exactly as main.go
// wires them, backed by a fresh in-memory store per test.
func newTestEngine(t *testing.T) *rules.Engine {
	t.Helper()
	st := memstore.New()
	engine := rules.NewEngine(st, fixedClock(), "../../rules")
	engine.RegisterEvaluator("fx_policy", ledger.EvaluateFXPolicy)
	engine.RegisterEvaluator("depeg_policy", ledger.EvaluateDepegPolicy)
	engine.RegisterEvaluator("spend_waterfall", account.EvaluateWaterfall)
	engine.RegisterEvaluator("risk_scoring", loan.EvaluateRiskScore)
	engine.RegisterEvaluator("loan_underwriting", loan.EvaluateUnderwriting)
	engine.RegisterEvaluator("loan_servicing", loan.EvaluateServicing)
	engine.RegisterEvaluator("dashboard_thresholds", dashboard.EvaluateThresholds)
	engine.RegisterEvaluator("risk_weights", dashboard.EvaluateRiskWeights)
	engine.RegisterEvaluator("liquidity_stress", dashboard.EvaluateLiquidityStress)
	if err := engine.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}
	return engine
}

// stubHandler is the "full mux" the demo Service is injected with in
// tests: it records every request it receives and returns canned JSON for
// the paths the demo script calls, without touching any real domain
// service. This matches the contract's instruction to stub call_api in
// tests rather than build the whole application.
type stubHandler struct {
	mu       sync.Mutex
	requests []stubRequest
}

type stubRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func (h *stubHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		if len(data) > 0 {
			_ = json.Unmarshal(data, &body)
		}
	}
	h.mu.Lock()
	h.requests = append(h.requests, stubRequest{Method: r.Method, Path: r.URL.Path, Body: body})
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/loan/underwrite":
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"decision": "approved", "applicant_id": body["applicant_id"]})
	case r.Method == http.MethodPost && r.URL.Path == "/simulate/purchase":
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "approved", "amount_cents": body["amount_cents"]})
	case r.Method == http.MethodPost && r.URL.Path == "/simulate/advance-time":
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"now": "2026-10-17T12:00:00Z", "previous": "2026-09-16T12:00:00Z"})
	case r.Method == http.MethodGet && r.URL.Path == "/dashboard/ratios":
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"lcr_pct": 118})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
	}
}

func (h *stubHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func newTestService(t *testing.T, scenarioPath string) (*Service, *rules.Engine, *stubHandler) {
	t.Helper()
	engine := newTestEngine(t)
	handler := &stubHandler{}
	svc := New(engine, handler, fixedClock(), scenarioPath)
	if err := svc.LoadError(); err != nil {
		t.Fatalf("unexpected load error for %q: %v", scenarioPath, err)
	}
	return svc, engine, handler
}

func writeScenarioFile(t *testing.T, yamlBody string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scenarios.yaml")
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("writing fixture scenario file: %v", err)
	}
	return path
}

func TestLoadRejectsUnregisteredRule(t *testing.T) {
	engine := newTestEngine(t)
	path := writeScenarioFile(t, `
scenarios:
  - scenario_id: bad
    description: exercises an unregistered rule_id
    steps:
      - id: step-1
        label: bad edit
        action: edit_rule
        rule_id: not_a_real_rule
        field_path: foo.bar
        new_value: 1
        narration: n
`)
	svc := New(engine, &stubHandler{}, fixedClock(), path)
	err := svc.LoadError()
	if err == nil {
		t.Fatal("expected a load error for an edit_rule step targeting an unregistered rule_id")
	}
	if !strings.Contains(err.Error(), "not_a_real_rule") {
		t.Fatalf("error does not mention the offending rule_id: %v", err)
	}
}

func TestLoadRejectsUnknownAction(t *testing.T) {
	engine := newTestEngine(t)
	path := writeScenarioFile(t, `
scenarios:
  - scenario_id: bad
    description: exercises an unknown action
    steps:
      - id: step-1
        label: bogus
        action: teleport_operator
        narration: n
`)
	svc := New(engine, &stubHandler{}, fixedClock(), path)
	if err := svc.LoadError(); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("expected an 'unknown action' load error, got %v", err)
	}
}

func TestLoadRejectsMissingFields(t *testing.T) {
	engine := newTestEngine(t)
	path := writeScenarioFile(t, `
scenarios:
  - scenario_id: bad
    description: call_api missing path
    steps:
      - id: step-1
        label: bogus
        action: call_api
        method: GET
        narration: n
`)
	svc := New(engine, &stubHandler{}, fixedClock(), path)
	if err := svc.LoadError(); err == nil {
		t.Fatal("expected a load error for call_api missing path")
	}
}

func TestStepSequencing(t *testing.T) {
	svc, _, _ := newTestService(t, "../../demo/demo-scenarios.yaml")
	ctx := context.Background()

	_, err := svc.RunStep(ctx, "live-demo-v1", "step-3-rerun-same-applicant", false)
	if !errors.Is(err, ErrPreviousStepNotRun) {
		t.Fatalf("expected ErrPreviousStepNotRun, got %v", err)
	}

	if _, err := svc.RunStep(ctx, "live-demo-v1", "step-3-rerun-same-applicant", true); err != nil {
		t.Fatalf("force=true should bypass the sequencing gate: %v", err)
	}
}

func TestLiveDemoFullRunThenReset(t *testing.T) {
	svc, engine, handler := newTestService(t, "../../demo/demo-scenarios.yaml")
	ctx := context.Background()

	baseline, err := engine.Get("loan_underwriting")
	if err != nil {
		t.Fatal(err)
	}
	baselineVersion := baseline.Version

	steps := []string{
		"step-1-baseline",
		"step-2-tighten-credit-score",
		"step-3-rerun-same-applicant",
		"step-4-flip-waterfall",
		"step-5-rerun-purchase",
		"step-6-lower-lcr-target",
		"step-7-reset",
	}
	for _, id := range steps {
		resp, err := svc.RunStep(ctx, "live-demo-v1", id, false)
		if err != nil {
			t.Fatalf("running %s: %v", id, err)
		}
		if !resp.OK {
			t.Fatalf("step %s: OK = false", id)
		}
		if resp.StepID != id {
			t.Fatalf("step %s: response StepID = %q", id, resp.StepID)
		}
	}

	if handler.count() != 3 {
		t.Fatalf("expected 3 call_api requests recorded (steps 1,3,5), got %d", handler.count())
	}

	// After the full run + reset, every touched rule must be back to its
	// pre-demo content, but as a *new* version (never the old version
	// number reused).
	loanRule, err := engine.Get("loan_underwriting")
	if err != nil {
		t.Fatal(err)
	}
	if loanRule.Version == baselineVersion {
		t.Fatalf("reset should mint a new version, still at %d", loanRule.Version)
	}
	elig, _ := loanRule.Content["eligibility"].(map[string]any)
	if got := elig["min_credit_score"]; got != float64(620) {
		t.Fatalf("min_credit_score after reset = %v, want 620", got)
	}

	waterfallRule, err := engine.Get("spend_waterfall")
	if err != nil {
		t.Fatal(err)
	}
	conds, _ := waterfallRule.Content["conditions_evaluated_in_order"].([]any)
	if len(conds) == 0 {
		t.Fatal("spend_waterfall content missing conditions_evaluated_in_order")
	}
	first, _ := conds[0].(map[string]any)
	if first["rule"] != "prefer_fiat_if_sufficient" {
		t.Fatalf("spend_waterfall first condition after reset = %v, want prefer_fiat_if_sufficient", first["rule"])
	}

	thresholdsRule, err := engine.Get("dashboard_thresholds")
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := thresholdsRule.Content["targets"].(map[string]any)
	if got := targets["lcr_minimum_pct"]; got != float64(100) {
		t.Fatalf("lcr_minimum_pct after reset = %v, want 100", got)
	}
}

func TestEditRuleStepReflectsImmediatelyInEngine(t *testing.T) {
	svc, engine, _ := newTestService(t, "../../demo/demo-scenarios.yaml")
	ctx := context.Background()

	if _, err := svc.RunStep(ctx, "live-demo-v1", "step-1-baseline", false); err != nil {
		t.Fatalf("step-1: %v", err)
	}
	resp, err := svc.RunStep(ctx, "live-demo-v1", "step-2-tighten-credit-score", false)
	if err != nil {
		t.Fatalf("step-2: %v", err)
	}
	result, ok := resp.Result.(EditRuleResult)
	if !ok {
		t.Fatalf("step-2 result type = %T, want EditRuleResult", resp.Result)
	}
	if result.NewVersion != 2 {
		t.Fatalf("expected new_version 2, got %d", result.NewVersion)
	}
	if result.Warning == "" {
		t.Fatal("expected a warning: raising min_credit_score to 700 breaks the rule's own documented approval example")
	}

	rule, err := engine.Get("loan_underwriting")
	if err != nil {
		t.Fatal(err)
	}
	if rule.Version != 2 {
		t.Fatalf("engine.Get version = %d, want 2", rule.Version)
	}
	elig, _ := rule.Content["eligibility"].(map[string]any)
	if got := elig["min_credit_score"]; got != float64(700) {
		t.Fatalf("min_credit_score = %v, want 700", got)
	}
}

func TestPortfolioOverTimeScenarioGetSupportsNoBody(t *testing.T) {
	svc, _, handler := newTestService(t, "../../demo/demo-scenarios.yaml")
	ctx := context.Background()

	if _, err := svc.RunStep(ctx, "portfolio-over-time", "step-1-advance-time", false); err != nil {
		t.Fatalf("step-1: %v", err)
	}
	resp, err := svc.RunStep(ctx, "portfolio-over-time", "step-2-check-ratios", false)
	if err != nil {
		t.Fatalf("step-2 (GET, no body): %v", err)
	}
	result, ok := resp.Result.(CallAPIResult)
	if !ok {
		t.Fatalf("result type = %T, want CallAPIResult", resp.Result)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Status)
	}
	if handler.count() != 2 {
		t.Fatalf("expected 2 recorded requests, got %d", handler.count())
	}
}
