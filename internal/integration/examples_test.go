// Package integration holds cross-package tests that need every domain
// evaluator registered at once, mirroring the wiring in cmd/stablebank.
package integration

import (
	"context"
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

// TestEveryRuleFileExamplesPass is the single gate that makes the rule files
// self-verifying: each file's examples block is run through the evaluator
// that production uses for that rule type.
func TestEveryRuleFileExamplesPass(t *testing.T) {
	clock := simclock.Fixed(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC))
	engine := rules.NewEngine(memstore.New(), clock, "../../rules")
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
		t.Fatal(err)
	}

	summaries := engine.List()
	if len(summaries) != 9 {
		t.Fatalf("expected 9 registered rules, got %d", len(summaries))
	}
	for _, s := range summaries {
		rule, err := engine.Get(s.RuleID)
		if err != nil {
			t.Fatalf("%s: %v", s.RuleID, err)
		}
		if len(rule.Examples) < 2 {
			t.Errorf("%s: expected at least 2 examples, got %d", s.RuleID, len(rule.Examples))
		}
		for _, res := range engine.RunExamples(rule) {
			if !res.Passed {
				t.Errorf("%s / %q failed: %s (expected %v, actual %v)", s.RuleID, res.Name, res.Error, res.Expected, res.Actual)
			}
		}
	}
}
