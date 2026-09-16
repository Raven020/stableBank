// Package ledger implements the event-sourced double-entry ledger (D1). All
// money is represented as int64 minor units; float64 is never used anywhere
// near money. Every write is an append to the shared store.EventStore and
// every read is derived by replaying those events through an in-memory
// projection cache.
package ledger

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Currency describes a supported currency's minor-unit scale.
type Currency struct {
	Code     string
	Decimals int
}

// Currencies is the fixed set of currencies known to the ledger.
var Currencies = map[string]Currency{
	"USD":  {"USD", 2},
	"USDC": {"USDC", 6},
}

// Amount is a signed quantity of minor units of a currency.
type Amount struct {
	Currency string `json:"currency"`
	Minor    int64  `json:"minor"`
}

// Display renders the amount as a fixed-point decimal string followed by the
// currency code, e.g. "12.34 USD" or "800.000000 USDC".
func (a Amount) Display() string {
	cur, ok := Currencies[a.Currency]
	if !ok {
		return fmt.Sprintf("%d %s", a.Minor, a.Currency)
	}
	return fmt.Sprintf("%s %s", formatMinor(a.Minor, cur.Decimals), a.Currency)
}

// formatMinor renders minor as a fixed-point decimal string with exactly
// decimals fractional digits, using only integer arithmetic.
func formatMinor(minor int64, decimals int) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := strconv.FormatInt(minor, 10)
	if decimals == 0 {
		if neg {
			return "-" + s
		}
		return s
	}
	for len(s) <= decimals {
		s = "0" + s
	}
	intPart := s[:len(s)-decimals]
	fracPart := s[len(s)-decimals:]
	out := intPart + "." + fracPart
	if neg {
		out = "-" + out
	}
	return out
}

// ParseAmount parses an exact decimal string (e.g. "100.00", "0.998500")
// into minor units for the given currency code. It rejects strings with more
// fractional digits than the currency supports, and any non-decimal input.
// No float64 is used; the conversion is exact integer arithmetic.
func ParseAmount(code, decimal string) (Amount, error) {
	cur, ok := Currencies[code]
	if !ok {
		return Amount{}, fmt.Errorf("ledger: unknown currency %q", code)
	}
	minor, err := parseDecimalToMinor(decimal, cur.Decimals)
	if err != nil {
		return Amount{}, err
	}
	return Amount{Currency: code, Minor: minor}, nil
}

// parseDecimalToMinor parses a decimal string into minor units at the given
// scale using only integer arithmetic (no float64).
func parseDecimalToMinor(decimal string, decimals int) (int64, error) {
	s := strings.TrimSpace(decimal)
	if s == "" {
		return 0, errors.New("ledger: empty amount")
	}
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("ledger: invalid amount %q", decimal)
	}
	intPart := s
	fracPart := ""
	if idx := strings.IndexByte(s, '.'); idx >= 0 {
		intPart = s[:idx]
		fracPart = s[idx+1:]
	}
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("ledger: invalid amount %q", decimal)
	}
	if intPart == "" {
		intPart = "0"
	}
	if !isDigits(intPart) || (fracPart != "" && !isDigits(fracPart)) {
		return 0, fmt.Errorf("ledger: invalid amount %q", decimal)
	}
	if len(fracPart) > decimals {
		return 0, fmt.Errorf("ledger: amount %q has more than %d decimal places", decimal, decimals)
	}
	for len(fracPart) < decimals {
		fracPart += "0"
	}
	combined := strings.TrimLeft(intPart+fracPart, "0")
	if combined == "" {
		combined = "0"
	}
	v, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ledger: amount %q out of range: %w", decimal, err)
	}
	if neg {
		v = -v
	}
	return v, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
