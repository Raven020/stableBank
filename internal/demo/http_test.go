package demo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPScenarioLifecycle(t *testing.T) {
	svc, _, _ := newTestService(t, "../../demo/demo-scenarios.yaml")
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)

	// GET /demo/scenarios
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/scenarios", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /demo/scenarios status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var briefs []ScenarioBrief
	if err := json.Unmarshal(rec.Body.Bytes(), &briefs); err != nil {
		t.Fatalf("decoding scenario list: %v", err)
	}
	if len(briefs) != 2 {
		t.Fatalf("expected 2 scenarios, got %d", len(briefs))
	}

	// GET /demo/scenarios/{id}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/scenarios/live-demo-v1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /demo/scenarios/live-demo-v1 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var detail ScenarioDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decoding scenario detail: %v", err)
	}
	if len(detail.Steps) != 7 {
		t.Fatalf("expected 7 steps, got %d", len(detail.Steps))
	}
	if detail.Started {
		t.Fatal("scenario should not be started before /start is called")
	}

	// unknown scenario -> 404
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/scenarios/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown scenario status = %d, want 404", rec.Code)
	}

	// POST /demo/scenarios/{id}/start
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/live-demo-v1/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// POST .../steps/{step_id}/run (step-1, no body)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/live-demo-v1/steps/step-1-baseline/run", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("run step-1 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var runResp StepRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &runResp); err != nil {
		t.Fatalf("decoding run response: %v", err)
	}
	if !runResp.OK || runResp.NextStepID != "step-2-tighten-credit-score" {
		t.Fatalf("unexpected run response: %+v", runResp)
	}

	// Running step-3 out of order -> 409, with force -> 200.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/live-demo-v1/steps/step-3-rerun-same-applicant/run", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("out-of-order run status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	forceBody := strings.NewReader(`{"force": true}`)
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/live-demo-v1/steps/step-3-rerun-same-applicant/run", forceBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("forced run status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// POST /demo/scenarios/{id}/reset
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/live-demo-v1/reset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// POST /demo/scenarios/{id}/reload
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/live-demo-v1/reload", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reload status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPExamplesFailedWithoutAllowFlagIs409(t *testing.T) {
	engine := newTestEngine(t)
	path := writeScenarioFile(t, `
scenarios:
  - scenario_id: strict
    description: edits min_credit_score without allow_failing_examples
    steps:
      - id: step-1
        label: tighten without allowance
        action: edit_rule
        rule_id: loan_underwriting
        field_path: eligibility.min_credit_score
        new_value: 700
        narration: n
`)
	svc := New(engine, &stubHandler{}, fixedClock(), path)
	if err := svc.LoadError(); err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/demo/scenarios/strict/steps/step-1/run", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding error envelope: %v", err)
	}
	if envelope["error"] != "examples_failed" {
		t.Fatalf("error = %v, want examples_failed", envelope["error"])
	}
}
