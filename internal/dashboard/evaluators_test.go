package dashboard_test

import (
	"context"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/dashboard"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

// testRulesDir mirrors internal/rules' own test convention: tests run from
// internal/dashboard, so the shared rules/ tree is two levels up.
const testRulesDir = "../../rules"

func newTestEngine(t *testing.T) *rules.Engine {
	t.Helper()
	st := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := rules.NewEngine(st, clock, testRulesDir)
	e.RegisterEvaluator("dashboard_thresholds", dashboard.EvaluateThresholds)
	e.RegisterEvaluator("risk_weights", dashboard.EvaluateRiskWeights)
	e.RegisterEvaluator("liquidity_stress", dashboard.EvaluateLiquidityStress)
	if err := e.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}
	return e
}

// TestDashboardRuleExamples loads the real rules/dashboard/*.yaml files
// through the real rules engine and asserts every worked example for all
// three dashboard rule types passes against our evaluators.
func TestDashboardRuleExamples(t *testing.T) {
	e := newTestEngine(t)

	for _, ruleID := range []string{"dashboard_thresholds", "risk_weights", "liquidity_stress"} {
		rule, err := e.Get(ruleID)
		if err != nil {
			t.Fatalf("Get(%q): %v", ruleID, err)
		}
		if len(rule.Examples) < 2 {
			t.Fatalf("%s: expected at least 2 examples, got %d", ruleID, len(rule.Examples))
		}
		results := e.RunExamples(rule)
		for _, res := range results {
			if !res.Passed {
				t.Errorf("%s example %q failed: %s\n  expected: %#v\n  actual:   %#v", ruleID, res.Name, res.Error, res.Expected, res.Actual)
			}
		}
	}
}

// TestEvaluateThresholds_TargetsExactMatch pins the boundary behaviour: a
// value exactly at a minimum target is "ok", and a value exactly at the
// maximum target for loan_to_deposit is also "ok" (both are <=/>= not
// strict).
func TestEvaluateThresholds_TargetsExactMatch(t *testing.T) {
	content := map[string]any{
		"targets": map[string]any{
			"lcr_minimum_pct":              100.0,
			"nsfr_minimum_pct":             100.0,
			"capital_adequacy_minimum_pct": 10.5,
			"loan_to_deposit_maximum_pct":  90.0,
		},
	}
	out, err := dashboard.EvaluateThresholds(content, map[string]any{
		"lcr_pct": 100.0, "nsfr_pct": 100.0, "capital_adequacy_pct": 10.5, "loan_to_deposit_pct": 90.0,
	})
	if err != nil {
		t.Fatalf("EvaluateThresholds: %v", err)
	}
	want := map[string]any{"lcr": "ok", "nsfr": "ok", "capital_adequacy": "ok", "loan_to_deposit": "ok"}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q] = %v, want %v", k, out[k], v)
		}
	}
}

// TestEvaluateRiskWeights_UnknownTierFallsBackToDefault exercises the
// default_weight_pct fallback path for a tier not present in
// weights_by_tier_pct.
func TestEvaluateRiskWeights_UnknownTierFallsBackToDefault(t *testing.T) {
	content := map[string]any{
		"weights_by_tier_pct":  map[string]any{"A": 50.0},
		"defaulted_weight_pct": 150.0,
		"default_weight_pct":   100.0,
	}
	out, err := dashboard.EvaluateRiskWeights(content, map[string]any{
		"tier": "Z", "status": "active", "ead_cents": int64(20000),
	})
	if err != nil {
		t.Fatalf("EvaluateRiskWeights: %v", err)
	}
	if out["risk_weight_pct"] != 100.0 {
		t.Errorf("risk_weight_pct = %v, want 100", out["risk_weight_pct"])
	}
	if out["rwa_cents"] != int64(20000) {
		t.Errorf("rwa_cents = %v, want 20000", out["rwa_cents"])
	}
}

// TestEvaluateLiquidityStress_ZeroDeposits exercises the "no_outflows"
// escape hatch when there are no deposits to run off.
func TestEvaluateLiquidityStress_ZeroDeposits(t *testing.T) {
	content := map[string]any{"deposit_runoff_pct": 10.0}
	out, err := dashboard.EvaluateLiquidityStress(content, map[string]any{
		"hqla_cents": int64(5000), "deposits_cents": int64(0),
	})
	if err != nil {
		t.Fatalf("EvaluateLiquidityStress: %v", err)
	}
	if out["net_outflows_cents"] != int64(0) {
		t.Errorf("net_outflows_cents = %v, want 0", out["net_outflows_cents"])
	}
	if out["lcr_pct"] != 0.0 {
		t.Errorf("lcr_pct = %v, want 0", out["lcr_pct"])
	}
	if out["note"] != "no_outflows" {
		t.Errorf("note = %v, want no_outflows", out["note"])
	}
}
