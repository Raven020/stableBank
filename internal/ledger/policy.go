package ledger

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
)

// stringField reads a string value out of a YAML/JSON-decoded map, which may
// have been produced by yaml.v3 (map[string]any / string) or by a test
// literal.
func stringField(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expected string, got %T", v)
	}
	return s, nil
}

// intField tolerantly reads an integer out of a YAML/JSON-decoded value:
// yaml.v3 may hand back int, int64 or float64 depending on the source, and
// examples/inputs authored as JSON numbers decode as float64 too. Decimal
// strings are also accepted.
func intField(v any) (int64, error) {
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

// EvaluateFXPolicy implements the fx_policy rule type. Given the rule's YAML
// content and an input {from, to, amount_minor}, it returns
// {to_amount_minor, rate_scaled, rate_display, source}. rate_scaled and
// rate_display always describe the pair's canonical (base->quote) mock_rate,
// even when the requested conversion is the inverse direction; the
// inversion is applied (via exact math/big.Rat arithmetic) only to compute
// to_amount_minor.
func EvaluateFXPolicy(content, input map[string]any) (map[string]any, error) {
	from, err := stringField(input["from"])
	if err != nil {
		return nil, fmt.Errorf("ledger: fx_policy input.from: %w", err)
	}
	to, err := stringField(input["to"])
	if err != nil {
		return nil, fmt.Errorf("ledger: fx_policy input.to: %w", err)
	}
	amountMinor, err := intField(input["amount_minor"])
	if err != nil {
		return nil, fmt.Errorf("ledger: fx_policy input.amount_minor: %w", err)
	}

	mode, err := roundingModeFromContent(content)
	if err != nil {
		return nil, err
	}

	pairScaled, source, inverted, err := findFXPair(content, from, to)
	if err != nil {
		return nil, err
	}

	fromCur, ok := Currencies[from]
	if !ok {
		return nil, fmt.Errorf("ledger: unknown currency %q", from)
	}
	toCur, ok := Currencies[to]
	if !ok {
		return nil, fmt.Errorf("ledger: unknown currency %q", to)
	}

	effective := pairScaled
	if inverted {
		effective, err = invertScaled(pairScaled, mode)
		if err != nil {
			return nil, err
		}
	}

	toMinor, err := convertMinor(amountMinor, fromCur.Decimals, toCur.Decimals, effective, mode)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"to_amount_minor": toMinor,
		"rate_scaled":     pairScaled,
		"rate_display":    formatMinor(pairScaled, 8),
		"source":          source,
	}, nil
}

// EvaluateDepegPolicy implements the depeg_policy rule type (documentation
// only; not enforced elsewhere in the ledger). Given content with
// thresholds.watch_below_usd / thresholds.depeg_below_usd (decimal strings)
// and input {usdc_price_usd}, it returns {status: "normal"|"watch"|"depeg"}.
// Comparisons use math/big.Rat so mismatched decimal precision between the
// input and the thresholds never loses accuracy.
func EvaluateDepegPolicy(content, input map[string]any) (map[string]any, error) {
	priceStr, err := stringField(input["usdc_price_usd"])
	if err != nil {
		return nil, fmt.Errorf("ledger: depeg_policy input.usdc_price_usd: %w", err)
	}
	price, ok := new(big.Rat).SetString(priceStr)
	if !ok {
		return nil, fmt.Errorf("ledger: invalid usdc_price_usd %q", priceStr)
	}

	thresholds, ok := content["thresholds"].(map[string]any)
	if !ok {
		return nil, errors.New("ledger: depeg_policy missing thresholds")
	}
	watchStr, err := stringField(thresholds["watch_below_usd"])
	if err != nil {
		return nil, fmt.Errorf("ledger: depeg_policy thresholds.watch_below_usd: %w", err)
	}
	depegStr, err := stringField(thresholds["depeg_below_usd"])
	if err != nil {
		return nil, fmt.Errorf("ledger: depeg_policy thresholds.depeg_below_usd: %w", err)
	}
	watch, ok := new(big.Rat).SetString(watchStr)
	if !ok {
		return nil, fmt.Errorf("ledger: invalid watch_below_usd %q", watchStr)
	}
	depeg, ok := new(big.Rat).SetString(depegStr)
	if !ok {
		return nil, fmt.Errorf("ledger: invalid depeg_below_usd %q", depegStr)
	}

	status := "normal"
	switch {
	case price.Cmp(depeg) < 0:
		status = "depeg"
	case price.Cmp(watch) < 0:
		status = "watch"
	}
	return map[string]any{"status": status}, nil
}
