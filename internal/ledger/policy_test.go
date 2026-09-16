package ledger

import "testing"

// TestEvaluateFXPolicyContractExample reproduces the exact worked example
// from CONTRACTS.md's fx_policy YAML shape.
func TestEvaluateFXPolicyContractExample(t *testing.T) {
	content := fxPolicyContent("half_up")
	input := map[string]any{
		"from":         "USD",
		"to":           "USDC",
		"amount_minor": int64(10000),
	}
	out, err := EvaluateFXPolicy(content, input)
	if err != nil {
		t.Fatalf("EvaluateFXPolicy: %v", err)
	}
	if out["to_amount_minor"] != int64(100150225) {
		t.Fatalf("to_amount_minor = %v, want 100150225", out["to_amount_minor"])
	}
	if out["rate_scaled"] != int64(99850000) {
		t.Fatalf("rate_scaled = %v, want 99850000", out["rate_scaled"])
	}
	if out["rate_display"] != "0.99850000" {
		t.Fatalf("rate_display = %v, want 0.99850000", out["rate_display"])
	}
	if out["source"] != "mock" {
		t.Fatalf("source = %v, want mock", out["source"])
	}
}

// TestEvaluateFXPolicyDirectDirection checks the non-inverted (base->quote)
// direction, where the canonical mock_rate applies directly.
func TestEvaluateFXPolicyDirectDirection(t *testing.T) {
	content := fxPolicyContent("half_up")
	input := map[string]any{
		"from":         "USDC",
		"to":           "USD",
		"amount_minor": int64(1_000_000), // 1.000000 USDC
	}
	out, err := EvaluateFXPolicy(content, input)
	if err != nil {
		t.Fatalf("EvaluateFXPolicy: %v", err)
	}
	// 1 USDC * 0.9985 = 0.9985 USD = 99.85 cents, half_up rounds to 100.
	if out["to_amount_minor"] != int64(100) {
		t.Fatalf("to_amount_minor = %v, want 100 (1.00 USD, half_up of 99.85 cents)", out["to_amount_minor"])
	}
}

// TestEvaluateFXPolicyToleratesYAMLNumberTypes checks the tolerant numeric
// helper used to read amount_minor, since yaml.v3 (and JSON round-trips in
// examples) may hand back int, int64, float64, or a decimal string.
func TestEvaluateFXPolicyToleratesYAMLNumberTypes(t *testing.T) {
	content := fxPolicyContent("half_up")
	for _, v := range []any{int(10000), int64(10000), float64(10000), "10000"} {
		input := map[string]any{"from": "USD", "to": "USDC", "amount_minor": v}
		out, err := EvaluateFXPolicy(content, input)
		if err != nil {
			t.Fatalf("EvaluateFXPolicy with amount_minor=%T(%v): %v", v, v, err)
		}
		if out["to_amount_minor"] != int64(100150225) {
			t.Fatalf("amount_minor=%T(%v): to_amount_minor = %v, want 100150225", v, v, out["to_amount_minor"])
		}
	}
}

func depegPolicyContent() map[string]any {
	return map[string]any{
		"enforced":           false,
		"reference_currency": "USD",
		"thresholds": map[string]any{
			"watch_below_usd": "0.99500000",
			"depeg_below_usd": "0.97000000",
		},
		"actions_on_depeg": []any{"halt_usdc_funding_in_waterfall", "halt_fx_conversions", "notify_treasury"},
	}
}

func TestEvaluateDepegPolicy(t *testing.T) {
	content := depegPolicyContent()
	cases := []struct {
		price  string
		status string
	}{
		{"1.0000", "normal"},
		{"0.9960", "normal"},
		{"0.9950", "normal"}, // exactly at the watch threshold: not strictly below, still normal
		{"0.9800", "watch"},  // matches the CONTRACTS.md worked example
		{"0.9700", "watch"},  // exactly at the depeg threshold: not strictly below, still watch
		{"0.9500", "depeg"},
	}
	for _, c := range cases {
		out, err := EvaluateDepegPolicy(content, map[string]any{"usdc_price_usd": c.price})
		if err != nil {
			t.Fatalf("EvaluateDepegPolicy(%q): %v", c.price, err)
		}
		if out["status"] != c.status {
			t.Errorf("EvaluateDepegPolicy(%q) status = %v, want %q", c.price, out["status"], c.status)
		}
	}
}
