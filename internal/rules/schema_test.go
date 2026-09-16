package rules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

const validLoanServicingYAML = `
rule_id: loan_servicing
rule_type: loan_servicing
version: 1
domain: loan
description: test rule
rationale: test rationale
owner: collections@stablebank.demo
last_reviewed: "2026-01-01"
grace_period_days: 5
missed_payments_to_default: 3
late_fee_cents: 2500
examples:
  - name: ok
    input: { days_past_due: 1, missed_payments_so_far: 0 }
    expected: { is_missed: false }
  - name: ok2
    input: { days_past_due: 10, missed_payments_so_far: 0 }
    expected: { is_missed: true }
`

func testEngineForSchema(t *testing.T) *Engine {
	t.Helper()
	st := memstore.New()
	clock := simclock.Fixed(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return NewEngine(st, clock, testRulesDir)
}

func TestValidate_AcceptsValidDocument(t *testing.T) {
	e := testEngineForSchema(t)
	rule, err := e.Validate([]byte(validLoanServicingYAML))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if rule.RuleID != "loan_servicing" {
		t.Errorf("RuleID = %q, want loan_servicing", rule.RuleID)
	}
	if len(rule.Examples) != 2 {
		t.Errorf("Examples = %d, want 2", len(rule.Examples))
	}
}

func TestValidate_RejectsUnknownKey(t *testing.T) {
	e := testEngineForSchema(t)
	withTypo := strings.Replace(validLoanServicingYAML, "grace_period_days: 5", "grace_period_dyas: 5\ngrace_period_days: 5", 1)
	_, err := e.Validate([]byte(withTypo))
	if err == nil {
		t.Fatal("Validate() should reject an unknown top-level key")
	}
	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("error is not a *ValidationError: %v", err)
	}
	if len(ve.Problems) == 0 {
		t.Error("ValidationError.Problems is empty")
	}
}

func TestValidate_RejectsMissingRequiredKey(t *testing.T) {
	e := testEngineForSchema(t)
	missing := strings.Replace(validLoanServicingYAML, "late_fee_cents: 2500\n", "", 1)
	_, err := e.Validate([]byte(missing))
	if err == nil {
		t.Fatal("Validate() should reject a document missing a required type-specific key")
	}
	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("error is not a *ValidationError: %v", err)
	}
}

func TestValidate_RejectsUnknownRuleType(t *testing.T) {
	e := testEngineForSchema(t)
	bogus := strings.Replace(validLoanServicingYAML, "rule_type: loan_servicing", "rule_type: not_a_real_type", 1)
	_, err := e.Validate([]byte(bogus))
	if err == nil {
		t.Fatal("Validate() should reject an unknown rule_type")
	}
}

func asValidationError(err error, target **ValidationError) bool {
	if ve, ok := err.(*ValidationError); ok {
		*target = ve
		return true
	}
	return false
}

func TestSchemasCompileForEveryRegisteredRuleType(t *testing.T) {
	e := testEngineForSchema(t)
	if e.schemaErr != nil {
		t.Fatalf("schema load error: %v", e.schemaErr)
	}
	if err := e.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}
}
