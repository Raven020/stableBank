package simulate_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Raven020/stableBank/internal/simulate"
)

func TestRunScenario_RepayReducesOutstandingPrincipal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	loanID := firstLoanID(t, h)

	before, err := h.loans.Get(ctx, loanID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	result, err := h.sim.RunScenario(ctx, simulate.Scenario{
		Steps: []simulate.Step{
			{Type: simulate.StepRepay, LoanID: loanID, Count: 1},
		},
	})
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if result.Summary.Failed != 0 {
		t.Fatalf("expected no failed steps, got %+v", result.Results)
	}

	after, err := h.loans.Get(ctx, loanID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.OutstandingPrincipalCents >= before.OutstandingPrincipalCents {
		t.Fatalf("outstanding principal did not decrease: before=%d after=%d", before.OutstandingPrincipalCents, after.OutstandingPrincipalCents)
	}
}

func TestRunScenario_MalformedStepFailsValidationBeforeExecuting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	before := h.clock.Now()

	_, err := h.sim.RunScenario(ctx, simulate.Scenario{
		Steps: []simulate.Step{
			{Type: simulate.StepRepay}, // missing required loan_id
		},
	})
	if err == nil {
		t.Fatalf("expected a validation error")
	}
	var verr *simulate.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *simulate.ValidationError, got %T: %v", err, err)
	}
	if len(verr.Errors) == 0 {
		t.Fatalf("expected at least one validation problem listed")
	}

	if !h.clock.Now().Equal(before) {
		t.Fatalf("clock moved despite a validation failure: before=%v now=%v", before, h.clock.Now())
	}
}

func TestRunScenario_UnknownStepTypeFailsValidation(t *testing.T) {
	h := newHarness(t)
	_, err := h.sim.RunScenario(context.Background(), simulate.Scenario{
		Steps: []simulate.Step{{Type: "not_a_real_step"}},
	})
	var verr *simulate.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *simulate.ValidationError, got %T: %v", err, err)
	}
}

func TestRunScenario_EmptyStepsFailsValidation(t *testing.T) {
	h := newHarness(t)
	_, err := h.sim.RunScenario(context.Background(), simulate.Scenario{Steps: nil})
	var verr *simulate.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *simulate.ValidationError, got %T: %v", err, err)
	}
}

func TestRunScenario_ContinuesAfterAFailingStep(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	loanID := firstLoanID(t, h)

	result, err := h.sim.RunScenario(ctx, simulate.Scenario{
		Steps: []simulate.Step{
			{Type: simulate.StepMissPayment, LoanID: "does-not-exist"}, // fails at runtime
			{Type: simulate.StepMissPayment, LoanID: loanID},           // still runs
		},
	})
	if err != nil {
		t.Fatalf("RunScenario should not return a top-level error for a runtime step failure: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 step results, got %d", len(result.Results))
	}
	if result.Results[0].OK {
		t.Fatalf("expected step 0 to fail")
	}
	if result.Results[0].Error == "" {
		t.Fatalf("expected step 0 to carry an error message")
	}
	if !result.Results[1].OK {
		t.Fatalf("expected step 1 to still run and succeed: %+v", result.Results[1])
	}
	if result.Summary.Failed != 1 || result.Summary.Succeeded != 1 {
		t.Fatalf("summary = %+v, want 1 failed / 1 succeeded", result.Summary)
	}
}

func TestFirstLoanPlaceholder_ResolvesToOldestActiveLoan(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	want := firstLoanID(t, h)

	result, err := h.sim.RunScenario(ctx, simulate.Scenario{
		Steps: []simulate.Step{
			{Type: simulate.StepMissPayment, LoanID: "$first_loan"},
		},
	})
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if result.Summary.Failed != 0 {
		t.Fatalf("expected the placeholder to resolve and the step to succeed: %+v", result.Results)
	}

	ln, err := h.loans.Get(ctx, want)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ln.MissedPayments == 0 {
		t.Fatalf("expected loan %s (resolved from $first_loan) to have a missed payment", want)
	}
}

func TestPresets_AllValidateAndMonthOfActivityRunsClean(t *testing.T) {
	presets := simulate.Presets()
	if len(presets) < 2 {
		t.Fatalf("expected at least 2 presets, got %d", len(presets))
	}

	for _, p := range presets {
		p := p
		t.Run(p.Name, func(t *testing.T) {
			h := newHarness(t)
			result, err := h.sim.RunScenario(context.Background(), p.Scenario)
			if err != nil {
				t.Fatalf("preset %q failed validation: %v", p.Name, err)
			}
			if p.Name == "month-of-activity" && result.Summary.Failed != 0 {
				t.Fatalf("preset %q: expected 0 failed steps, got %+v", p.Name, result.Results)
			}
		})
	}
}

func TestHTTP_ClockAndPresetsAndDuplicateRoutesDoNotPanic(t *testing.T) {
	h := newHarness(t)

	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/simulate/clock", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /simulate/clock: status %d body %s", rec.Code, rec.Body.String())
	}
	var clockView simulate.ClockView
	if err := json.Unmarshal(rec.Body.Bytes(), &clockView); err != nil {
		t.Fatalf("decode clock view: %v", err)
	}

	rec = httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/simulate/presets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /simulate/presets: status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Presets []simulate.Preset `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode presets: %v", err)
	}
	if len(body.Presets) == 0 {
		t.Fatalf("expected at least one preset over HTTP")
	}

	// account.Service.RegisterRoutes already registers POST
	// /simulate/purchase; confirm it still answers (i.e. simulate's
	// RegisterRoutes did not shadow or panic on it).
	rec = httptest.NewRecorder()
	purchaseBody := `{"account_id":"demo-account-1","amount_cents":100}`
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/simulate/purchase", strings.NewReader(purchaseBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /simulate/purchase: status %d body %s", rec.Code, rec.Body.String())
	}
}
