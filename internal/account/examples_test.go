package account_test

import (
	"context"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

// TestSpendWaterfallExamples loads the real rules/account/spend_waterfall.yaml
// through the real rules engine (schema validation included) and asserts
// account.EvaluateWaterfall reproduces every worked example.
func TestSpendWaterfallExamples(t *testing.T) {
	ctx := context.Background()
	engine := rules.NewEngine(memstore.New(), simclock.Fixed(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)), "../../rules")
	engine.RegisterEvaluator("spend_waterfall", account.EvaluateWaterfall)
	if err := engine.LoadFromDisk(ctx); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}

	rule, err := engine.Get("spend_waterfall")
	if err != nil {
		t.Fatalf("Get(spend_waterfall): %v", err)
	}
	if len(rule.Examples) < 2 {
		t.Fatalf("expected at least 2 examples, got %d", len(rule.Examples))
	}

	results := engine.RunExamples(rule)
	for _, r := range results {
		if !r.Passed {
			t.Errorf("example %q failed: %s (expected %#v, actual %#v)", r.Name, r.Error, r.Expected, r.Actual)
		}
	}
}
