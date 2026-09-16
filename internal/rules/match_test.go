package rules

import (
	"strings"
	"testing"
)

func TestMatchExpected_SubsetOfKeys(t *testing.T) {
	expected := map[string]any{"approved": true, "tier": "B"}
	actual := map[string]any{"approved": true, "tier": "B", "annual_rate_bps": 999, "reasons": []any{}}
	ok, msg := MatchExpected(expected, actual)
	if !ok {
		t.Fatalf("expected subset match, got mismatch: %s", msg)
	}
}

func TestMatchExpected_NestedMapsRecurse(t *testing.T) {
	expected := map[string]any{"factor_points": map[string]any{"credit_score": 0.0}}
	actual := map[string]any{"factor_points": map[string]any{"credit_score": 0, "income": 10}, "extra": true}
	ok, msg := MatchExpected(expected, actual)
	if !ok {
		t.Fatalf("expected subset match, got mismatch: %s", msg)
	}
}

func TestMatchExpected_ArraysCompareElementwise(t *testing.T) {
	expected := map[string]any{"reasons": []any{"a", "b"}}
	actual := map[string]any{"reasons": []any{"a", "b"}}
	if ok, msg := MatchExpected(expected, actual); !ok {
		t.Fatalf("expected match, got: %s", msg)
	}

	actualWrongOrder := map[string]any{"reasons": []any{"b", "a"}}
	if ok, _ := MatchExpected(expected, actualWrongOrder); ok {
		t.Fatal("expected mismatch on reordered array, got match")
	}

	actualWrongLength := map[string]any{"reasons": []any{"a"}}
	if ok, _ := MatchExpected(expected, actualWrongLength); ok {
		t.Fatal("expected mismatch on different array length, got match")
	}
}

func TestMatchExpected_NumbersCompareAsFloat64(t *testing.T) {
	expected := map[string]any{"risk_score": 70}
	actual := map[string]any{"risk_score": int64(70)}
	if ok, msg := MatchExpected(expected, actual); !ok {
		t.Fatalf("expected int64/int to compare equal via float64 round-trip, got: %s", msg)
	}
}

func TestMatchExpected_MissingKeyFails(t *testing.T) {
	expected := map[string]any{"approved": true}
	actual := map[string]any{"tier": "B"}
	ok, msg := MatchExpected(expected, actual)
	if ok {
		t.Fatal("expected mismatch when key is missing from actual")
	}
	if msg == "" {
		t.Error("expected a human-readable mismatch description")
	}
}

func TestLineDiff_AddedRemovedUnchanged(t *testing.T) {
	a := "one\ntwo\nthree"
	b := "one\ntwo-edited\nthree\nfour"
	diff := lineDiff(a, b)
	if diff == "" {
		t.Fatal("lineDiff returned empty string for differing input")
	}
	wantContains := []string{"  one", "- two", "+ two-edited", "  three", "+ four"}
	for _, w := range wantContains {
		if !strings.Contains(diff, w) {
			t.Errorf("lineDiff output missing %q; got:\n%s", w, diff)
		}
	}
}
