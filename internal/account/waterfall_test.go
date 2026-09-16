package account_test

import (
	"testing"

	"github.com/Raven020/stableBank/internal/account"
)

// defaultContent mirrors rules/account/spend_waterfall.yaml's default
// shape (as canonicalized JSON-compatible types: []any of map[string]any).
func defaultContent() map[string]any {
	return map[string]any{
		"conditions_evaluated_in_order": []any{
			map[string]any{"rule": "prefer_fiat_if_sufficient"},
			map[string]any{"rule": "prefer_stablecoin_if_sufficient"},
			map[string]any{"rule": "split_fiat_then_stablecoin"},
			map[string]any{"rule": "decline"},
		},
		"allow_split": true,
	}
}

func TestEvaluateWaterfallFiatSufficient(t *testing.T) {
	out, err := account.EvaluateWaterfall(defaultContent(), map[string]any{
		"amount_cents": 5000, "usd_balance_cents": 10000, "usdc_balance_usd_equivalent_cents": 0,
	})
	if err != nil {
		t.Fatalf("EvaluateWaterfall: %v", err)
	}
	if out["decision"] != "approved" || out["matched_rule"] != "prefer_fiat_if_sufficient" {
		t.Fatalf("unexpected output: %#v", out)
	}
	funding := out["funding"].([]any)
	if len(funding) != 1 {
		t.Fatalf("expected 1 funding leg, got %d", len(funding))
	}
	leg := funding[0].(map[string]any)
	if leg["currency"] != "USD" || leg["usd_equivalent_cents"] != int64(5000) || leg["share_pct"] != 100 {
		t.Fatalf("unexpected leg: %#v", leg)
	}
}

func TestEvaluateWaterfallSplitStablecoinThenFiat(t *testing.T) {
	content := map[string]any{
		"conditions_evaluated_in_order": []any{
			map[string]any{"rule": "split_stablecoin_then_fiat"},
			map[string]any{"rule": "decline"},
		},
		"allow_split": true,
	}
	out, err := account.EvaluateWaterfall(content, map[string]any{
		"amount_cents": 5000, "usd_balance_cents": 4000, "usdc_balance_usd_equivalent_cents": 2000,
	})
	if err != nil {
		t.Fatalf("EvaluateWaterfall: %v", err)
	}
	if out["decision"] != "approved" || out["matched_rule"] != "split_stablecoin_then_fiat" {
		t.Fatalf("unexpected output: %#v", out)
	}
	funding := out["funding"].([]any)
	if len(funding) != 2 {
		t.Fatalf("expected 2 funding legs, got %#v", funding)
	}
	first := funding[0].(map[string]any)
	second := funding[1].(map[string]any)
	if first["currency"] != "USDC" || first["usd_equivalent_cents"] != int64(2000) || first["share_pct"] != 40 {
		t.Fatalf("unexpected first leg: %#v", first)
	}
	if second["currency"] != "USD" || second["usd_equivalent_cents"] != int64(3000) || second["share_pct"] != 60 {
		t.Fatalf("unexpected second leg: %#v", second)
	}
}

func TestEvaluateWaterfallSplitDisallowedFallsThroughToDecline(t *testing.T) {
	content := map[string]any{
		"conditions_evaluated_in_order": []any{
			map[string]any{"rule": "prefer_fiat_if_sufficient"},
			map[string]any{"rule": "split_fiat_then_stablecoin"},
			map[string]any{"rule": "decline"},
		},
		"allow_split": false,
	}
	out, err := account.EvaluateWaterfall(content, map[string]any{
		"amount_cents": 5000, "usd_balance_cents": 2000, "usdc_balance_usd_equivalent_cents": 4000,
	})
	if err != nil {
		t.Fatalf("EvaluateWaterfall: %v", err)
	}
	if out["decision"] != "declined" || out["matched_rule"] != "decline" {
		t.Fatalf("expected decline when allow_split is false, got %#v", out)
	}
}

func TestEvaluateWaterfallUnknownRuleErrors(t *testing.T) {
	content := map[string]any{
		"conditions_evaluated_in_order": []any{
			map[string]any{"rule": "not_a_real_rule"},
		},
		"allow_split": true,
	}
	_, err := account.EvaluateWaterfall(content, map[string]any{
		"amount_cents": 100, "usd_balance_cents": 0, "usdc_balance_usd_equivalent_cents": 0,
	})
	if err == nil {
		t.Fatal("expected an error for an unknown rule name")
	}
}

func TestEvaluateWaterfallDeclineWhenInsufficient(t *testing.T) {
	out, err := account.EvaluateWaterfall(defaultContent(), map[string]any{
		"amount_cents": 5000, "usd_balance_cents": 1000, "usdc_balance_usd_equivalent_cents": 1000,
	})
	if err != nil {
		t.Fatalf("EvaluateWaterfall: %v", err)
	}
	if out["decision"] != "declined" {
		t.Fatalf("expected decline, got %#v", out)
	}
	funding := out["funding"].([]any)
	if len(funding) != 0 {
		t.Fatalf("expected no funding legs on decline, got %#v", funding)
	}
}

func TestEvaluateWaterfallToleratesStringAndFloatInputs(t *testing.T) {
	out, err := account.EvaluateWaterfall(defaultContent(), map[string]any{
		"amount_cents": float64(5000), "usd_balance_cents": "10000", "usdc_balance_usd_equivalent_cents": int(0),
	})
	if err != nil {
		t.Fatalf("EvaluateWaterfall: %v", err)
	}
	if out["decision"] != "approved" || out["matched_rule"] != "prefer_fiat_if_sufficient" {
		t.Fatalf("unexpected output: %#v", out)
	}
}
