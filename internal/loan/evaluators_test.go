package loan

import (
	"context"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

const testRulesDir = "../../rules"

// newLoanRuleEngine loads the real rules/ tree from disk and registers the
// three D5 evaluators, mirroring what main.go will do.
func newLoanRuleEngine(t *testing.T) *rules.Engine {
	t.Helper()
	st := memstore.New()
	clock := simclock.Fixed(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := rules.NewEngine(st, clock, testRulesDir)
	e.RegisterEvaluator("risk_scoring", EvaluateRiskScore)
	e.RegisterEvaluator("loan_underwriting", EvaluateUnderwriting)
	e.RegisterEvaluator("loan_servicing", EvaluateServicing)
	if err := e.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}
	return e
}

// TestRunExamples_LoanRules asserts every worked example shipped in
// rules/loan/*.yaml passes against the real evaluators.
func TestRunExamples_LoanRules(t *testing.T) {
	e := newLoanRuleEngine(t)

	for _, ruleID := range []string{"risk_scoring", "loan_underwriting", "loan_servicing"} {
		rule, err := e.Get(ruleID)
		if err != nil {
			t.Fatalf("Get(%q): %v", ruleID, err)
		}
		results := e.RunExamples(rule)
		if len(results) == 0 {
			t.Fatalf("%s: no examples found", ruleID)
		}
		for _, r := range results {
			if !r.Passed {
				t.Errorf("%s example %q failed: %s\n  expected: %v\n  actual:   %v", ruleID, r.Name, r.Error, r.Expected, r.Actual)
			}
		}
	}
}

func TestEvaluateRiskScore_Direct(t *testing.T) {
	e := newLoanRuleEngine(t)
	rule, err := e.Get("risk_scoring")
	if err != nil {
		t.Fatal(err)
	}
	out, err := EvaluateRiskScore(rule.Content, map[string]any{
		"credit_score":           760,
		"monthly_income_cents":   int64(900000),
		"account_age_days":       900,
		"existing_balance_cents": "500000", // tolerate strings
	})
	if err != nil {
		t.Fatalf("EvaluateRiskScore: %v", err)
	}
	if out["risk_score"] != int64(100) {
		t.Errorf("risk_score = %v, want 100", out["risk_score"])
	}
	if out["tier"] != "A" {
		t.Errorf("tier = %v, want A", out["tier"])
	}
	if out["pd_bps"] != int64(150) {
		t.Errorf("pd_bps = %v, want 150", out["pd_bps"])
	}
}

func TestEvaluateUnderwriting_NoTierMatched_NoTierReported(t *testing.T) {
	e := newLoanRuleEngine(t)
	rule, err := e.Get("loan_underwriting")
	if err != nil {
		t.Fatal(err)
	}
	out, err := EvaluateUnderwriting(rule.Content, map[string]any{
		"credit_score":                590,
		"monthly_income_cents":        180000,
		"existing_debt_monthly_cents": 120000,
		"account_age_days":            60,
		"risk_score":                  30, // below every pricing_tiers min_risk_score
		"requested_principal_cents":   2000000,
		"term_months":                 36,
		"proposed_installment_cents":  90000,
	})
	if err != nil {
		t.Fatalf("EvaluateUnderwriting: %v", err)
	}
	if approved, _ := out["approved"].(bool); approved {
		t.Errorf("expected declined, got approved")
	}
	if _, ok := out["tier"]; ok {
		t.Errorf("expected no tier reported when none matched, got %v", out["tier"])
	}
	reasons, _ := out["reasons"].([]string)
	if len(reasons) == 0 {
		t.Errorf("expected reasons, got none")
	}
}
