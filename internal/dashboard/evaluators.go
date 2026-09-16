package dashboard

import (
	"fmt"
	"math/big"
)

// EvaluateThresholds implements the dashboard_thresholds rule_type. Given
// content.targets and an input of the four illustrative ratios (as
// percentages), it reports "ok"/"below_target" for the three
// minimum-style targets (lcr, nsfr, capital_adequacy) and
// "ok"/"above_maximum" for the maximum-style target (loan_to_deposit).
// Comparisons are done with math/big.Rat so fractional targets (e.g. 10.5
// for capital adequacy) are compared exactly, never via float64.
func EvaluateThresholds(content, input map[string]any) (map[string]any, error) {
	targets, ok := content["targets"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dashboard: dashboard_thresholds missing targets")
	}

	checks := []struct {
		inputKey  string
		targetKey string
		outputKey string
		minimum   bool // true: ok if value >= target; false: ok if value <= target
		okBelow   string
		okAbove   string
	}{
		{"lcr_pct", "lcr_minimum_pct", "lcr", true, "below_target", "ok"},
		{"nsfr_pct", "nsfr_minimum_pct", "nsfr", true, "below_target", "ok"},
		{"capital_adequacy_pct", "capital_adequacy_minimum_pct", "capital_adequacy", true, "below_target", "ok"},
		{"loan_to_deposit_pct", "loan_to_deposit_maximum_pct", "loan_to_deposit", false, "ok", "above_maximum"},
	}

	out := make(map[string]any, len(checks))
	for _, c := range checks {
		value, err := requireRat(input, c.inputKey)
		if err != nil {
			return nil, err
		}
		target, err := requireRat(targets, c.targetKey)
		if err != nil {
			return nil, fmt.Errorf("dashboard: dashboard_thresholds targets.%s: %w", c.targetKey, err)
		}
		cmp := value.Cmp(target)
		var status string
		if c.minimum {
			if cmp >= 0 {
				status = c.okAbove
			} else {
				status = c.okBelow
			}
		} else {
			if cmp <= 0 {
				status = c.okBelow
			} else {
				status = c.okAbove
			}
		}
		out[c.outputKey] = status
	}
	return out, nil
}

// EvaluateRiskWeights implements the risk_weights rule_type. It picks the
// applicable weight (defaulted_weight_pct for a defaulted exposure, else
// weights_by_tier_pct[tier], else default_weight_pct as a fallback for an
// unrecognised tier) and computes rwa_cents = round_half_up(ead_cents *
// weight_pct / 100).
func EvaluateRiskWeights(content, input map[string]any) (map[string]any, error) {
	tier, err := requireString(input, "tier")
	if err != nil {
		return nil, err
	}
	status, err := requireString(input, "status")
	if err != nil {
		return nil, err
	}
	ead, err := requireRat(input, "ead_cents")
	if err != nil {
		return nil, err
	}

	var weight *big.Rat
	switch {
	case status == "defaulted":
		weight, err = requireRat(content, "defaulted_weight_pct")
		if err != nil {
			return nil, fmt.Errorf("dashboard: risk_weights defaulted_weight_pct: %w", err)
		}
	default:
		tiers, _ := content["weights_by_tier_pct"].(map[string]any)
		if v, ok := tiers[tier]; ok {
			weight, err = toRat(v)
			if err != nil {
				return nil, fmt.Errorf("dashboard: risk_weights weights_by_tier_pct.%s: %w", tier, err)
			}
		} else {
			weight, err = requireRat(content, "default_weight_pct")
			if err != nil {
				return nil, fmt.Errorf("dashboard: risk_weights default_weight_pct: %w", err)
			}
		}
	}

	rwa := new(big.Rat).Mul(ead, weight)
	rwa.Quo(rwa, big.NewRat(100, 1))
	rwaCents := roundHalfUp(rwa)

	return map[string]any{
		"risk_weight_pct": ratToFloatDP(weight, 4),
		"rwa_cents":       rwaCents,
	}, nil
}

// EvaluateLiquidityStress implements the liquidity_stress rule_type's LCR
// calculation: net_outflows_cents = deposits_cents * deposit_runoff_pct /
// 100 (rounded half up), and lcr_pct = hqla_cents / net_outflows_cents *
// 100 computed as an exact big.Rat and rendered to 2 decimal places at the
// JSON boundary via Rat.FloatString(2). Callers (the dashboard service) are
// expected to have already applied the per-currency hqla_haircut_pct
// discount when computing hqla_cents; this evaluator only applies the
// deposit run-off assumption, matching the rule's worked examples.
//
// If deposits_cents (and therefore net_outflows_cents) is zero, there is no
// meaningful coverage ratio to report: lcr_pct is reported as 0 with
// note: "no_outflows" rather than dividing by zero or reporting an
// infinite/sentinel value.
func EvaluateLiquidityStress(content, input map[string]any) (map[string]any, error) {
	hqla, err := requireRat(input, "hqla_cents")
	if err != nil {
		return nil, err
	}
	deposits, err := requireRat(input, "deposits_cents")
	if err != nil {
		return nil, err
	}
	runoff, err := requireRat(content, "deposit_runoff_pct")
	if err != nil {
		return nil, fmt.Errorf("dashboard: liquidity_stress deposit_runoff_pct: %w", err)
	}

	netOutflows := new(big.Rat).Mul(deposits, runoff)
	netOutflows.Quo(netOutflows, big.NewRat(100, 1))
	netOutflowsCents := roundHalfUp(netOutflows)

	out := map[string]any{
		"net_outflows_cents": netOutflowsCents,
	}
	if netOutflowsCents == 0 {
		out["lcr_pct"] = 0.0
		out["note"] = "no_outflows"
		return out, nil
	}

	lcr := new(big.Rat).Quo(hqla, big.NewRat(netOutflowsCents, 1))
	lcr.Mul(lcr, big.NewRat(100, 1))
	out["lcr_pct"] = ratToFloatDP(lcr, 2)
	return out, nil
}
