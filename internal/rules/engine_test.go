package rules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

const testRulesDir = "../../rules"

// testLoanServicingEvaluator is the trivial test evaluator required by the
// contract (domain evaluators live in the loan/ledger/account/dashboard
// packages, not here). It reproduces exactly the semantics documented in
// rules/loan/loan_servicing.yaml so that rule's own worked examples pass.
func testLoanServicingEvaluator(content, input map[string]any) (map[string]any, error) {
	grace := content["grace_period_days"].(float64)
	threshold := content["missed_payments_to_default"].(float64)
	fee := content["late_fee_cents"].(float64)

	daysPastDue := input["days_past_due"].(float64)
	missedSoFar := input["missed_payments_so_far"].(float64)

	isMissed := daysPastDue > grace
	lateFee := 0.0
	if isMissed {
		lateFee = fee
	}
	shouldDefault := isMissed && (missedSoFar+1) >= threshold

	return map[string]any{
		"is_missed":      isMissed,
		"late_fee_cents": lateFee,
		"should_default": shouldDefault,
	}, nil
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	st := memstore.New()
	clock := simclock.Fixed(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := NewEngine(st, clock, testRulesDir)
	e.RegisterEvaluator("loan_servicing", testLoanServicingEvaluator)
	if err := e.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}
	return e
}

func TestLoadFromDisk_AllNineRuleFiles(t *testing.T) {
	e := newTestEngine(t)

	want := []string{
		"loan_underwriting", "risk_scoring", "loan_servicing",
		"fx_policy", "depeg_policy", "spend_waterfall",
		"dashboard_thresholds", "risk_weights", "liquidity_stress",
	}
	summaries := e.List()
	if len(summaries) != len(want) {
		t.Fatalf("List() returned %d rules, want %d", len(summaries), len(want))
	}
	for _, id := range want {
		rule, err := e.Get(id)
		if err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
		if rule.Version != 1 {
			t.Errorf("Get(%q).Version = %d, want 1", id, rule.Version)
		}
		if len(rule.Examples) < 2 {
			t.Errorf("Get(%q) has %d examples, want >= 2", id, len(rule.Examples))
		}
		versions, err := e.Versions(context.Background(), id)
		if err != nil {
			t.Fatalf("Versions(%q): %v", id, err)
		}
		if len(versions) != 1 {
			t.Errorf("Versions(%q) has %d entries, want 1", id, len(versions))
		}
	}
}

func TestSave_CreatesVersion2AndHotReloads(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	live, err := e.Get("loan_servicing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	raw := live.Raw

	// A metadata-only edit: does not change semantics, so the embedded
	// examples still pass.
	edited := strings.Replace(string(raw), `last_reviewed: "2026-08-01"`, `last_reviewed: "2026-09-01"`, 1)
	if edited == string(raw) {
		t.Fatal("test setup: last_reviewed line not found in loan_servicing.yaml")
	}

	result, err := e.Save(ctx, "loan_servicing", []byte(edited), SaveOptions{Author: "test", ChangeNote: "bump review date"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if result.Version != 2 {
		t.Errorf("Save().Version = %d, want 2", result.Version)
	}
	if result.Previous != 1 {
		t.Errorf("Save().Previous = %d, want 1", result.Previous)
	}
	for _, ex := range result.Examples {
		if !ex.Passed {
			t.Errorf("example %q failed: %s", ex.Name, ex.Error)
		}
	}
	if result.Diff == "" {
		t.Error("Save().Diff is empty, want a rendered diff")
	}

	// Version 1 must now be closed.
	versions, err := e.Versions(ctx, "loan_servicing")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("Versions() has %d entries, want 2", len(versions))
	}
	if versions[0].EffectiveTo == nil {
		t.Error("version 1 should be closed (EffectiveTo set) after Save")
	}
	if versions[1].EffectiveTo != nil {
		t.Error("version 2 should still be open (EffectiveTo nil)")
	}

	// Hot reload: Get must reflect the new version synchronously, no I/O.
	reloaded, err := e.Get("loan_servicing")
	if err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	if reloaded.Version != 2 {
		t.Errorf("Get().Version after Save = %d, want 2", reloaded.Version)
	}
	if reloaded.LastReviewed != "2026-09-01" {
		t.Errorf("Get().LastReviewed after Save = %q, want 2026-09-01", reloaded.LastReviewed)
	}
}

func TestSave_ExamplesFailedUnlessAllowed(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	live, err := e.Get("loan_servicing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Change the configured late fee so the rule's own worked examples
	// (which expect 2500) now fail.
	edited := strings.Replace(string(live.Raw), "late_fee_cents: 2500", "late_fee_cents: 999", 1)
	if edited == string(live.Raw) {
		t.Fatal("test setup: late_fee_cents: 2500 not found")
	}

	_, err = e.Save(ctx, "loan_servicing", []byte(edited), SaveOptions{Author: "test"})
	if err == nil {
		t.Fatal("Save() with a broken example should have failed")
	}
	if !errors.Is(err, ErrExamplesFailed) {
		t.Errorf("Save() error = %v, want errors.Is(_, ErrExamplesFailed)", err)
	}
	var ef *ExamplesFailedError
	if !errors.As(err, &ef) {
		t.Fatalf("Save() error is not an *ExamplesFailedError: %v", err)
	}
	if countFailed(ef.Results) == 0 {
		t.Error("ExamplesFailedError.Results has no failing examples")
	}

	// Cache must not have moved.
	if r, _ := e.Get("loan_servicing"); r.Version != 1 {
		t.Errorf("Get().Version after failed Save = %d, want 1 (unchanged)", r.Version)
	}

	// With AllowFailingExamples the save must go through anyway.
	result, err := e.Save(ctx, "loan_servicing", []byte(edited), SaveOptions{Author: "test", AllowFailingExamples: true})
	if err != nil {
		t.Fatalf("Save() with AllowFailingExamples: %v", err)
	}
	if result.Version != 2 {
		t.Errorf("Save().Version = %d, want 2", result.Version)
	}
}

func TestReactivate_RestoresPreviousVersionContent(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	v1, err := e.Get("loan_servicing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	v1Content := v1.Content

	edited := strings.Replace(string(v1.Raw), `last_reviewed: "2026-08-01"`, `last_reviewed: "2026-09-01"`, 1)
	if _, err := e.Save(ctx, "loan_servicing", []byte(edited), SaveOptions{Author: "test", ChangeNote: "edit"}); err != nil {
		t.Fatalf("Save (v2): %v", err)
	}

	result, err := e.Reactivate(ctx, "loan_servicing", 1, "undo the metadata edit")
	if err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if result.Version != 3 {
		t.Errorf("Reactivate().Version = %d, want 3", result.Version)
	}
	if result.Previous != 2 {
		t.Errorf("Reactivate().Previous = %d, want 2", result.Previous)
	}

	v3, err := e.Get("loan_servicing")
	if err != nil {
		t.Fatalf("Get after Reactivate: %v", err)
	}
	if v3.Version != 3 {
		t.Errorf("live version after Reactivate = %d, want 3", v3.Version)
	}
	// Content must match version 1's content in every field except
	// version itself (which is always rewritten to the new version
	// number).
	for k, v := range v1Content {
		if k == "version" {
			continue
		}
		got := v3.Content[k]
		if !equalJSON(got, v) {
			t.Errorf("Reactivate: field %q = %#v, want %#v (version 1's value)", k, got, v)
		}
	}
	if v3.Content["version"].(float64) != 3 {
		t.Errorf("Reactivate: version field = %v, want 3", v3.Content["version"])
	}

	versions, err := e.Versions(ctx, "loan_servicing")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("Versions() has %d entries, want 3", len(versions))
	}
	if !strings.HasPrefix(versions[2].ChangeNote, "reactivated version 1:") {
		t.Errorf("version 3 ChangeNote = %q, want prefix %q", versions[2].ChangeNote, "reactivated version 1:")
	}
}

func TestEvaluate_LogsApplicationRetrievableByEntity(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	decision, err := e.Evaluate(ctx, "loan_servicing", "loan", "loan-42", map[string]any{
		"days_past_due":          15.0,
		"missed_payments_so_far": 2.0,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.RuleID != "loan_servicing" || decision.Version != 1 {
		t.Errorf("Decision = %+v, want RuleID=loan_servicing Version=1", decision)
	}
	if decision.ApplicationID == "" {
		t.Error("Decision.ApplicationID is empty")
	}
	shouldDefault, _ := decision.Output["should_default"].(bool)
	if !shouldDefault {
		t.Errorf("Decision.Output[should_default] = %v, want true", decision.Output["should_default"])
	}

	apps, err := e.store.ListRuleApplications(ctx, "", "loan-42", 0)
	if err != nil {
		t.Fatalf("ListRuleApplications: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("ListRuleApplications(entity_id=loan-42) returned %d rows, want 1", len(apps))
	}
	if apps[0].ID != decision.ApplicationID {
		t.Errorf("logged application ID = %q, want %q", apps[0].ID, decision.ApplicationID)
	}
	if apps[0].RuleID != "loan_servicing" || apps[0].EntityType != "loan" {
		t.Errorf("logged application = %+v", apps[0])
	}
}

func TestEvaluate_NoEvaluatorRegisteredErrors(t *testing.T) {
	e := newTestEngine(t)
	_, err := e.Evaluate(context.Background(), "fx_policy", "tx", "tx-1", map[string]any{})
	if err == nil {
		t.Fatal("Evaluate() with no registered evaluator should error")
	}
}

func equalJSON(a, b any) bool {
	na, _ := normalizeJSON(a)
	nb, _ := normalizeJSON(b)
	ok, _ := matchSubset(na, nb, "$")
	if !ok {
		return false
	}
	ok2, _ := matchSubset(nb, na, "$")
	return ok2
}

// sanity: make sure store.ErrNotFound is exported the way we expect.
var _ = store.ErrNotFound
