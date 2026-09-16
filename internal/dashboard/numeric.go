// Package dashboard implements the D6 deliverable: a read-only view over the
// ledger (D1) and the loan portfolio (via internal/portfolio) that renders a
// ledger book, money-in/money-out flows, and a handful of illustrative
// APRA-style prudential ratios (LCR, NSFR, capital adequacy) plus an
// internal loan-to-deposit ratio. None of these figures are certified
// regulatory metrics; every ratio the service returns carries a disclaimer
// saying so.
//
// Per CONTRACTS.md, money math never uses float64: every ratio and every
// rule-driven percentage is computed with math/big.Rat, and float64 is only
// introduced at the very end, at the JSON-boundary, via Rat.FloatString so
// the rounding behaviour is exact and auditable.
package dashboard

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// toRat tolerantly converts a JSON/YAML-decoded value into an exact
// math/big.Rat. It accepts float64 and int (the two shapes yaml.v3 and
// encoding/json produce for numbers), int64 (the shape Go callers such as
// the dashboard service pass directly) and decimal strings.
func toRat(v any) (*big.Rat, error) {
	switch t := v.(type) {
	case *big.Rat:
		return new(big.Rat).Set(t), nil
	case float64:
		r := new(big.Rat)
		if _, ok := r.SetString(strconv.FormatFloat(t, 'f', -1, 64)); !ok {
			return nil, fmt.Errorf("dashboard: cannot represent %v as a rational", t)
		}
		return r, nil
	case float32:
		return toRat(float64(t))
	case int:
		return new(big.Rat).SetInt64(int64(t)), nil
	case int64:
		return new(big.Rat).SetInt64(t), nil
	case int32:
		return new(big.Rat).SetInt64(int64(t)), nil
	case string:
		s := strings.TrimSpace(t)
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, fmt.Errorf("dashboard: invalid numeric string %q", t)
		}
		return r, nil
	case nil:
		return nil, fmt.Errorf("dashboard: missing numeric value")
	default:
		return nil, fmt.Errorf("dashboard: cannot convert %T to a number", v)
	}
}

// requireRat reads key from m and converts it with toRat, wrapping errors
// with the field name for a useful message.
func requireRat(m map[string]any, key string) (*big.Rat, error) {
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("dashboard: missing field %q", key)
	}
	r, err := toRat(v)
	if err != nil {
		return nil, fmt.Errorf("dashboard: field %q: %w", key, err)
	}
	return r, nil
}

// requireString reads a string field from m tolerantly.
func requireString(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("dashboard: missing field %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("dashboard: field %q must be a string, got %T", key, v)
	}
	return s, nil
}

// roundHalfUp rounds a big.Rat to the nearest integer, ties rounding away
// from zero (half up). Used for money (cents/micros) which are always
// whole numbers of minor units.
func roundHalfUp(r *big.Rat) int64 {
	neg := r.Sign() < 0
	n := new(big.Int).Abs(r.Num())
	d := new(big.Int).Abs(r.Denom())
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(n, d, rem)
	twice := new(big.Int).Mul(rem, big.NewInt(2))
	if twice.Cmp(d) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	v := q.Int64()
	if neg {
		v = -v
	}
	return v
}

// pctOf applies a percentage (expressed as a big.Rat out of 100, e.g. 15
// meaning 15%) to a minor-unit amount, rounding half up.
func pctOf(amountMinor int64, pct *big.Rat) int64 {
	r := new(big.Rat).Mul(big.NewRat(amountMinor, 1), pct)
	r.Quo(r, big.NewRat(100, 1))
	return roundHalfUp(r)
}

// ratioPct computes num/den * 100 as an exact big.Rat. den must be
// non-zero; callers are expected to special-case a zero denominator
// themselves (value_pct = 0, status "n/a") before calling this.
func ratioPct(numMinor, denMinor int64) *big.Rat {
	r := big.NewRat(numMinor, 1)
	r.Quo(r, big.NewRat(denMinor, 1))
	r.Mul(r, big.NewRat(100, 1))
	return r
}

// ratToFloatDP converts a big.Rat to a float64 rounded to dp decimal
// places, going through FloatString so the rounding is exact and
// deterministic rather than relying on big.Rat.Float64's binary rounding.
func ratToFloatDP(r *big.Rat, dp int) float64 {
	s := r.FloatString(dp)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// FloatString always produces a valid decimal literal; this should
		// be unreachable.
		return 0
	}
	return f
}
