package account

import (
	"errors"
	"fmt"
	"strconv"
)

// Known spend_waterfall rule names (rules/account/spend_waterfall.yaml,
// rules/schemas/spend_waterfall.schema.json).
const (
	ruleFiatFirst  = "prefer_fiat_if_sufficient"
	ruleCoinFirst  = "prefer_stablecoin_if_sufficient"
	ruleSplitFiat  = "split_fiat_then_stablecoin"
	ruleSplitCoin  = "split_stablecoin_then_fiat"
	ruleDeclineAll = "decline"
)

// EvaluateWaterfall implements the spend_waterfall rule_type: given the
// rule's YAML content (conditions_evaluated_in_order, allow_split) and an
// input {amount_cents, usd_balance_cents, usdc_balance_usd_equivalent_cents},
// it walks the conditions in order and returns the first one that applies:
// {decision: "approved"|"declined", matched_rule, funding: [{currency,
// usd_equivalent_cents, share_pct}]}. All money in this evaluator is
// expressed in USD-equivalent cents (both the USD and USDC legs); the
// caller (account.Service.Purchase) is responsible for converting the
// USDC leg's USD-equivalent portion into actual USDC minor units via
// ledger.Quote before posting.
func EvaluateWaterfall(content, input map[string]any) (map[string]any, error) {
	amount, err := numField(input["amount_cents"])
	if err != nil {
		return nil, fmt.Errorf("account: spend_waterfall input.amount_cents: %w", err)
	}
	if amount <= 0 {
		return nil, errors.New("account: spend_waterfall input.amount_cents must be positive")
	}
	usdBal, err := numField(input["usd_balance_cents"])
	if err != nil {
		return nil, fmt.Errorf("account: spend_waterfall input.usd_balance_cents: %w", err)
	}
	usdcBal, err := numField(input["usdc_balance_usd_equivalent_cents"])
	if err != nil {
		return nil, fmt.Errorf("account: spend_waterfall input.usdc_balance_usd_equivalent_cents: %w", err)
	}

	allowSplit, _ := content["allow_split"].(bool)

	condsRaw, ok := content["conditions_evaluated_in_order"].([]any)
	if !ok {
		return nil, errors.New("account: spend_waterfall content missing conditions_evaluated_in_order")
	}

	for _, c := range condsRaw {
		cm, ok := c.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("account: spend_waterfall malformed condition %v", c)
		}
		name, _ := cm["rule"].(string)
		switch name {
		case ruleFiatFirst:
			if usdBal >= amount {
				return approvedSingle(name, "USD", amount), nil
			}
		case ruleCoinFirst:
			if usdcBal >= amount {
				return approvedSingle(name, "USDC", amount), nil
			}
		case ruleSplitFiat:
			if allowSplit && usdBal+usdcBal >= amount {
				return approvedSplit(name, "USD", usdBal, "USDC", amount), nil
			}
		case ruleSplitCoin:
			if allowSplit && usdBal+usdcBal >= amount {
				return approvedSplit(name, "USDC", usdcBal, "USD", amount), nil
			}
		case ruleDeclineAll:
			return map[string]any{
				"decision":     "declined",
				"matched_rule": ruleDeclineAll,
				"funding":      []any{},
			}, nil
		default:
			return nil, fmt.Errorf("account: spend_waterfall unknown rule %q", name)
		}
	}
	return nil, errors.New("account: spend_waterfall exhausted conditions_evaluated_in_order without a decision (missing a terminal decline rule?)")
}

// approvedSingle builds the output for a rule that funds the whole amount
// from one currency.
func approvedSingle(rule, currency string, amount int64) map[string]any {
	return map[string]any{
		"decision":     "approved",
		"matched_rule": rule,
		"funding": []any{
			map[string]any{"currency": currency, "usd_equivalent_cents": amount, "share_pct": 100},
		},
	}
}

// approvedSplit builds the output for a split rule: firstBal is the balance
// (USD-equivalent cents) available in the currency preferred first; the
// remainder (if any) is funded from secondCcy. share_pct are integers
// summing to 100, with the remainder given to the last (second) leg. Legs
// with a zero portion are omitted.
func approvedSplit(rule, firstCcy string, firstBal int64, secondCcy string, amount int64) map[string]any {
	firstPortion := firstBal
	if firstPortion > amount {
		firstPortion = amount
	}
	if firstPortion < 0 {
		firstPortion = 0
	}
	secondPortion := amount - firstPortion

	var funding []any
	switch {
	case firstPortion > 0 && secondPortion > 0:
		firstPct := int(firstPortion * 100 / amount)
		secondPct := 100 - firstPct
		funding = []any{
			map[string]any{"currency": firstCcy, "usd_equivalent_cents": firstPortion, "share_pct": firstPct},
			map[string]any{"currency": secondCcy, "usd_equivalent_cents": secondPortion, "share_pct": secondPct},
		}
	case firstPortion > 0:
		funding = []any{
			map[string]any{"currency": firstCcy, "usd_equivalent_cents": firstPortion, "share_pct": 100},
		}
	default:
		funding = []any{
			map[string]any{"currency": secondCcy, "usd_equivalent_cents": secondPortion, "share_pct": 100},
		}
	}
	return map[string]any{
		"decision":     "approved",
		"matched_rule": rule,
		"funding":      funding,
	}
}

// numField tolerantly reads an integer out of a YAML/JSON-decoded value:
// yaml.v3 may hand back int, int64 or float64 depending on the source, and
// examples/inputs authored as JSON numbers decode as float64 too. Decimal
// strings are also accepted.
func numField(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case float64:
		return int64(t), nil
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid integer %q: %w", t, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}
