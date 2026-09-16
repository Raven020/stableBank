// Package loan implements the D5 deliverable: personal loan origination,
// servicing and dual (customer + bank) forecasting on top of the D1 ledger
// and D2 rules engine.
package loan

import (
	"fmt"
	"sort"
	"strconv"
)

// numField tolerantly reads a number out of a YAML/JSON-decoded value.
// Rule content and example inputs are always JSON-normalised (numbers
// arrive as float64), but Service builds some inputs directly in Go using
// int/int64, and callers may also hand in decimal strings.
func numField(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func requireNum(m map[string]any, key string) (float64, error) {
	v, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("loan: missing field %q", key)
	}
	f, ok := numField(v)
	if !ok {
		return 0, fmt.Errorf("loan: field %q must be a number, got %T", key, v)
	}
	return f, nil
}

func requireStr(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("loan: missing field %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("loan: field %q must be a string, got %T", key, v)
	}
	return s, nil
}

func requireMap(m map[string]any, key string) (map[string]any, error) {
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("loan: missing field %q", key)
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("loan: field %q must be an object, got %T", key, v)
	}
	return sub, nil
}

func requireSlice(m map[string]any, key string) ([]any, error) {
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("loan: missing field %q", key)
	}
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("loan: field %q must be an array, got %T", key, v)
	}
	return s, nil
}

// clampFloat clamps v to [min, max].
func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// matchBand finds the first band in bandsRaw whose range covers val: a band
// matches when (min absent or val >= min) and (max absent or val <= max).
func matchBand(bandsRaw []any, val float64) (float64, error) {
	for _, bRaw := range bandsRaw {
		b, ok := bRaw.(map[string]any)
		if !ok {
			continue
		}
		matches := true
		if minV, present := b["min"]; present {
			mv, ok2 := numField(minV)
			if !ok2 {
				return 0, fmt.Errorf("loan: band min must be a number")
			}
			if val < mv {
				matches = false
			}
		}
		if matches {
			if maxV, present := b["max"]; present {
				mv, ok2 := numField(maxV)
				if !ok2 {
					return 0, fmt.Errorf("loan: band max must be a number")
				}
				if val > mv {
					matches = false
				}
			}
		}
		if matches {
			return requireNum(b, "points")
		}
	}
	return 0, fmt.Errorf("loan: no band matched value %v", val)
}

// sortedTiers sorts a raw tier list descending by the numeric field named
// thresholdKey (e.g. "min_score" or "min_risk_score"), so that the first
// element to satisfy value >= threshold is the correct match.
func sortedTiers(raw []any, thresholdKey string) ([]map[string]any, error) {
	type entry struct {
		m         map[string]any
		threshold float64
	}
	entries := make([]entry, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("loan: tier entry must be an object, got %T", r)
		}
		th, err := requireNum(m, thresholdKey)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{m: m, threshold: th})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].threshold > entries[j].threshold })
	out := make([]map[string]any, len(entries))
	for i, e := range entries {
		out[i] = e.m
	}
	return out, nil
}

// firstMatchingTier returns the first tier (from a slice already sorted
// descending by thresholdKey) whose threshold is <= value.
func firstMatchingTier(sorted []map[string]any, thresholdKey string, value float64) (map[string]any, bool, error) {
	for _, t := range sorted {
		th, err := requireNum(t, thresholdKey)
		if err != nil {
			return nil, false, err
		}
		if value >= th {
			return t, true, nil
		}
	}
	return nil, false, nil
}

// highestRateTier returns the tier with the largest annual_rate_bps, used
// when no pricing tier matches the applicant's risk score (so pricing can
// still be reported/estimated).
func highestRateTier(sorted []map[string]any) (map[string]any, error) {
	if len(sorted) == 0 {
		return nil, fmt.Errorf("loan: no pricing tiers configured")
	}
	best := sorted[0]
	bestRate, err := requireNum(best, "annual_rate_bps")
	if err != nil {
		return nil, err
	}
	for _, t := range sorted[1:] {
		rate, err := requireNum(t, "annual_rate_bps")
		if err != nil {
			return nil, err
		}
		if rate > bestRate {
			best = t
			bestRate = rate
		}
	}
	return best, nil
}

// EvaluateRiskScore implements the risk_scoring rule type.
func EvaluateRiskScore(content, input map[string]any) (map[string]any, error) {
	base, err := requireNum(content, "base_score")
	if err != nil {
		return nil, err
	}
	factorsRaw, err := requireSlice(content, "factors")
	if err != nil {
		return nil, err
	}

	total := base
	factorPoints := map[string]any{}
	for _, fRaw := range factorsRaw {
		f, ok := fRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("loan: risk_scoring factor must be an object")
		}
		name, err := requireStr(f, "name")
		if err != nil {
			return nil, err
		}
		inputKey, err := requireStr(f, "input")
		if err != nil {
			return nil, err
		}
		val, err := requireNum(input, inputKey)
		if err != nil {
			return nil, err
		}
		bandsRaw, err := requireSlice(f, "bands")
		if err != nil {
			return nil, err
		}
		points, err := matchBand(bandsRaw, val)
		if err != nil {
			return nil, fmt.Errorf("loan: risk_scoring factor %q: %w", name, err)
		}
		total += points
		factorPoints[name] = int64(points)
	}

	clamp, err := requireMap(content, "clamp")
	if err != nil {
		return nil, err
	}
	clampMin, err := requireNum(clamp, "min")
	if err != nil {
		return nil, err
	}
	clampMax, err := requireNum(clamp, "max")
	if err != nil {
		return nil, err
	}
	score := clampFloat(total, clampMin, clampMax)

	tiersRaw, err := requireSlice(content, "tiers")
	if err != nil {
		return nil, err
	}
	sorted, err := sortedTiers(tiersRaw, "min_score")
	if err != nil {
		return nil, err
	}
	tier, matched, err := firstMatchingTier(sorted, "min_score", score)
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, fmt.Errorf("loan: risk_scoring: no tier matched score %v", score)
	}
	tierName, err := requireStr(tier, "tier")
	if err != nil {
		return nil, err
	}
	pdBps, err := requireNum(tier, "pd_bps")
	if err != nil {
		return nil, err
	}
	lgdBps, err := requireNum(content, "lgd_bps")
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"risk_score":    int64(score),
		"tier":          tierName,
		"pd_bps":        int64(pdBps),
		"lgd_bps":       int64(lgdBps),
		"factor_points": factorPoints,
	}, nil
}

// underwritingReasons vocabulary, checked (and reported) in this order.
const (
	ReasonCreditScoreBelowMinimum   = "credit_score_below_minimum"
	ReasonIncomeBelowMinimum        = "income_below_minimum"
	ReasonDebtToIncomeAboveMaximum  = "debt_to_income_above_maximum"
	ReasonAccountAgeBelowMinimum    = "account_age_below_minimum"
	ReasonRiskScoreBelowMinimum     = "risk_score_below_minimum"
	ReasonTermNotAllowed            = "term_not_allowed"
	ReasonPrincipalAboveTierMaximum = "principal_above_tier_maximum"
)

// EvaluateUnderwriting implements the loan_underwriting rule type.
func EvaluateUnderwriting(content, input map[string]any) (map[string]any, error) {
	creditScore, err := requireNum(input, "credit_score")
	if err != nil {
		return nil, err
	}
	income, err := requireNum(input, "monthly_income_cents")
	if err != nil {
		return nil, err
	}
	existingDebt, err := requireNum(input, "existing_debt_monthly_cents")
	if err != nil {
		return nil, err
	}
	accountAge, err := requireNum(input, "account_age_days")
	if err != nil {
		return nil, err
	}
	riskScore, err := requireNum(input, "risk_score")
	if err != nil {
		return nil, err
	}
	requestedPrincipal, err := requireNum(input, "requested_principal_cents")
	if err != nil {
		return nil, err
	}
	termMonths, err := requireNum(input, "term_months")
	if err != nil {
		return nil, err
	}
	proposedInstallment, err := requireNum(input, "proposed_installment_cents")
	if err != nil {
		return nil, err
	}

	elig, err := requireMap(content, "eligibility")
	if err != nil {
		return nil, err
	}
	minCredit, err := requireNum(elig, "min_credit_score")
	if err != nil {
		return nil, err
	}
	minIncome, err := requireNum(elig, "min_monthly_income_cents")
	if err != nil {
		return nil, err
	}
	maxDTI, err := requireNum(elig, "max_debt_to_income_pct")
	if err != nil {
		return nil, err
	}
	minAge, err := requireNum(elig, "min_account_age_days")
	if err != nil {
		return nil, err
	}
	minRisk, err := requireNum(elig, "min_risk_score")
	if err != nil {
		return nil, err
	}

	reasons := make([]string, 0, 7)
	if creditScore < minCredit {
		reasons = append(reasons, ReasonCreditScoreBelowMinimum)
	}
	if income < minIncome {
		reasons = append(reasons, ReasonIncomeBelowMinimum)
	}
	var dtiPct float64
	if income > 0 {
		dtiPct = (existingDebt + proposedInstallment) / income * 100
	} else {
		dtiPct = maxDTI + 1 // undefined income always fails the DTI gate
	}
	if dtiPct > maxDTI {
		reasons = append(reasons, ReasonDebtToIncomeAboveMaximum)
	}
	if accountAge < minAge {
		reasons = append(reasons, ReasonAccountAgeBelowMinimum)
	}
	if riskScore < minRisk {
		reasons = append(reasons, ReasonRiskScoreBelowMinimum)
	}

	termsRaw, err := requireSlice(content, "allowed_term_months")
	if err != nil {
		return nil, err
	}
	termAllowed := false
	for _, t := range termsRaw {
		tf, ok := numField(t)
		if ok && tf == termMonths {
			termAllowed = true
			break
		}
	}
	if !termAllowed {
		reasons = append(reasons, ReasonTermNotAllowed)
	}

	tiersRaw, err := requireSlice(content, "pricing_tiers")
	if err != nil {
		return nil, err
	}
	sorted, err := sortedTiers(tiersRaw, "min_risk_score")
	if err != nil {
		return nil, err
	}
	tier, matched, err := firstMatchingTier(sorted, "min_risk_score", riskScore)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"approved": false,
		"reasons":  reasons,
	}
	if matched {
		tierName, err := requireStr(tier, "tier")
		if err != nil {
			return nil, err
		}
		annualRateBps, err := requireNum(tier, "annual_rate_bps")
		if err != nil {
			return nil, err
		}
		maxPrincipal, err := requireNum(tier, "max_principal_cents")
		if err != nil {
			return nil, err
		}
		if requestedPrincipal > maxPrincipal {
			reasons = append(reasons, ReasonPrincipalAboveTierMaximum)
		}
		out["tier"] = tierName
		out["annual_rate_bps"] = int64(annualRateBps)
	}
	out["reasons"] = reasons
	out["approved"] = len(reasons) == 0
	return out, nil
}

// EvaluateServicing implements the loan_servicing rule type.
func EvaluateServicing(content, input map[string]any) (map[string]any, error) {
	grace, err := requireNum(content, "grace_period_days")
	if err != nil {
		return nil, err
	}
	threshold, err := requireNum(content, "missed_payments_to_default")
	if err != nil {
		return nil, err
	}
	fee, err := requireNum(content, "late_fee_cents")
	if err != nil {
		return nil, err
	}
	daysPastDue, err := requireNum(input, "days_past_due")
	if err != nil {
		return nil, err
	}
	missedSoFar, err := requireNum(input, "missed_payments_so_far")
	if err != nil {
		return nil, err
	}

	isMissed := daysPastDue > grace
	var lateFee float64
	if isMissed {
		lateFee = fee
	}
	shouldDefault := isMissed && (missedSoFar+1) >= threshold

	return map[string]any{
		"is_missed":      isMissed,
		"late_fee_cents": int64(lateFee),
		"should_default": shouldDefault,
	}, nil
}
