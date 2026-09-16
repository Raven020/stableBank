package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Raven020/stableBank/internal/simclock"
)

// scaleFactor is the fixed-point scale used by Rate.Scaled (8 decimals).
const scaleFactor = int64(100_000_000)

// RateProvider supplies FX rates. to-per-from × 1e8, see Rate.
type RateProvider interface {
	Rate(from, to string) (Rate, error)
}

// Rate is one FX rate quote.
type Rate struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Scaled int64     `json:"scaled"` // to-per-from × 1e8
	Source string    `json:"source"`
	AsOf   time.Time `json:"as_of"`
}

// roundingModeProvider is an optional capability a RateProvider may
// implement so Quote/Convert can pick up the policy's rounding mode
// (half_up | down). RateProvider itself only requires Rate, per contract;
// this is an internal extension point used by RuleRateProvider and
// StaticRateProvider.
type roundingModeProvider interface {
	RoundingMode() (string, error)
}

// FXRequest is a request to convert money between two ledger accounts.
type FXRequest struct {
	FromAccountID string            `json:"from_account_id"`
	ToAccountID   string            `json:"to_account_id"`
	From          Amount            `json:"from"`
	Kind          string            `json:"kind"`
	Description   string            `json:"description"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// FXQuote is a pure (no ledger writes) conversion quote.
type FXQuote struct {
	From         Amount `json:"from"`
	To           Amount `json:"to"`
	Rate         Rate   `json:"rate"`
	RoundingMode string `json:"rounding_mode"`
}

// FXResult is the outcome of Convert: the quote plus both linked, finalized
// transactions.
type FXResult struct {
	Quote  FXQuote     `json:"quote"`
	FromTx Transaction `json:"from_tx"`
	ToTx   Transaction `json:"to_tx"`
}

// RuleReader is the minimal interface the ledger needs from the rules
// engine, avoiding an import cycle (rules imports ledger to register
// EvaluateFXPolicy/EvaluateDepegPolicy).
type RuleReader interface {
	Get(ruleID string) (map[string]any, int, error)
}

// RuleRateProvider is a RateProvider backed by the fx_policy rule.
type RuleRateProvider struct {
	Rules  RuleReader
	RuleID string         // defaults to "fx_policy"
	Clock  simclock.Clock // optional; used to populate Rate.AsOf
}

// NewRuleRateProvider returns a RuleRateProvider reading the fx_policy rule
// through r.
func NewRuleRateProvider(r RuleReader) *RuleRateProvider {
	return &RuleRateProvider{Rules: r, RuleID: "fx_policy"}
}

func (p *RuleRateProvider) ruleID() string {
	if p.RuleID == "" {
		return "fx_policy"
	}
	return p.RuleID
}

func (p *RuleRateProvider) content() (map[string]any, error) {
	content, _, err := p.Rules.Get(p.ruleID())
	if err != nil {
		return nil, err
	}
	return content, nil
}

// Rate implements RateProvider. USD->USDC is derived from the USDC->USD
// mock_rate pair by exact inversion using math/big.Rat.
func (p *RuleRateProvider) Rate(from, to string) (Rate, error) {
	content, err := p.content()
	if err != nil {
		return Rate{}, err
	}
	mode, err := roundingModeFromContent(content)
	if err != nil {
		return Rate{}, err
	}
	scaled, source, err := rateFromPolicy(content, from, to, mode)
	if err != nil {
		return Rate{}, err
	}
	r := Rate{From: from, To: to, Scaled: scaled, Source: source}
	if p.Clock != nil {
		r.AsOf = p.Clock.Now()
	}
	return r, nil
}

// RoundingMode implements roundingModeProvider.
func (p *RuleRateProvider) RoundingMode() (string, error) {
	content, err := p.content()
	if err != nil {
		return "", err
	}
	return roundingModeFromContent(content)
}

// StaticRateProvider is a fixed RateProvider for tests.
type StaticRateProvider struct {
	Rates map[string]Rate // key "FROM->TO"
	Mode  string          // half_up | down; defaults to half_up
}

// Rate implements RateProvider.
func (p StaticRateProvider) Rate(from, to string) (Rate, error) {
	r, ok := p.Rates[from+"->"+to]
	if !ok {
		return Rate{}, fmt.Errorf("ledger: no static rate for %s->%s", from, to)
	}
	return r, nil
}

// RoundingMode implements roundingModeProvider.
func (p StaticRateProvider) RoundingMode() (string, error) {
	if p.Mode == "" {
		return "half_up", nil
	}
	return p.Mode, nil
}

// Quote is pure integer/big.Rat math: no ledger writes. It converts
// fromMinor units of currency `from` into currency `to` using the
// RateProvider and the rounding mode the policy specifies (default
// half_up).
func (s *Service) Quote(from, to string, fromMinor int64) (FXQuote, error) {
	if s.rates == nil {
		return FXQuote{}, errors.New("ledger: no rate provider configured")
	}
	fromCur, ok := Currencies[from]
	if !ok {
		return FXQuote{}, fmt.Errorf("ledger: unknown currency %q", from)
	}
	toCur, ok := Currencies[to]
	if !ok {
		return FXQuote{}, fmt.Errorf("ledger: unknown currency %q", to)
	}
	rate, err := s.rates.Rate(from, to)
	if err != nil {
		return FXQuote{}, err
	}
	mode := "half_up"
	if rp, ok := s.rates.(roundingModeProvider); ok {
		if m, err := rp.RoundingMode(); err == nil && m != "" {
			mode = m
		}
	}
	toMinor, err := convertMinor(fromMinor, fromCur.Decimals, toCur.Decimals, rate.Scaled, mode)
	if err != nil {
		return FXQuote{}, err
	}
	return FXQuote{
		From:         Amount{Currency: from, Minor: fromMinor},
		To:           Amount{Currency: to, Minor: toMinor},
		Rate:         rate,
		RoundingMode: mode,
	}, nil
}

// Convert posts two linked, finalized transactions through the
// treasury-fx-<ccy> accounts: tx1 (From currency) debits FromAccountID and
// credits treasury-fx-<from>; tx2 (To currency) debits treasury-fx-<to> and
// credits ToAccountID. Both carry the other's ID in LinkedTxID and share
// Metadata["fx_rate"]/["fx_source"].
func (s *Service) Convert(ctx context.Context, req FXRequest) (FXResult, error) {
	if req.FromAccountID == "" || req.ToAccountID == "" {
		return FXResult{}, errors.New("ledger: from/to account required")
	}
	if err := s.refresh(ctx); err != nil {
		return FXResult{}, err
	}
	s.proj.mu.Lock()
	fromAcc, ok := s.proj.accounts[req.FromAccountID]
	if !ok {
		s.proj.mu.Unlock()
		return FXResult{}, fmt.Errorf("ledger: account %q not found", req.FromAccountID)
	}
	toAcc, ok := s.proj.accounts[req.ToAccountID]
	if !ok {
		s.proj.mu.Unlock()
		return FXResult{}, fmt.Errorf("ledger: account %q not found", req.ToAccountID)
	}
	s.proj.mu.Unlock()

	if fromAcc.Currency != req.From.Currency {
		return FXResult{}, fmt.Errorf("ledger: from account currency %q does not match amount currency %q", fromAcc.Currency, req.From.Currency)
	}
	toCurrency := toAcc.Currency

	quote, err := s.Quote(req.From.Currency, toCurrency, req.From.Minor)
	if err != nil {
		return FXResult{}, err
	}

	kind := req.Kind
	if kind == "" {
		kind = "fx"
	}

	fromFXAccount := "treasury-fx-" + strings.ToLower(req.From.Currency)
	toFXAccount := "treasury-fx-" + strings.ToLower(toCurrency)

	fromTxID := newID()
	toTxID := newID()

	buildMeta := func() map[string]string {
		m := map[string]string{}
		for k, v := range req.Metadata {
			m[k] = v
		}
		m["fx_rate"] = formatMinor(quote.Rate.Scaled, 8)
		m["fx_source"] = quote.Rate.Source
		return m
	}

	fromTx := Transaction{
		ID:          fromTxID,
		Kind:        kind,
		Description: req.Description,
		Currency:    req.From.Currency,
		Entries: []Entry{
			{AccountID: req.FromAccountID, Direction: Debit, Amount: req.From},
			{AccountID: fromFXAccount, Direction: Credit, Amount: req.From},
		},
		LinkedTxID: toTxID,
		Metadata:   buildMeta(),
	}
	toTx := Transaction{
		ID:          toTxID,
		Kind:        kind,
		Description: req.Description,
		Currency:    toCurrency,
		Entries: []Entry{
			{AccountID: toFXAccount, Direction: Debit, Amount: quote.To},
			{AccountID: req.ToAccountID, Direction: Credit, Amount: quote.To},
		},
		LinkedTxID: fromTxID,
		Metadata:   buildMeta(),
	}

	postedFrom, err := s.Post(ctx, fromTx)
	if err != nil {
		return FXResult{}, err
	}
	postedTo, err := s.Post(ctx, toTx)
	if err != nil {
		return FXResult{}, err
	}

	finalFrom, err := s.Settle(ctx, postedFrom.ID)
	if err != nil {
		return FXResult{}, err
	}
	finalFrom, err = s.Finalize(ctx, finalFrom.ID)
	if err != nil {
		return FXResult{}, err
	}

	finalTo, err := s.Settle(ctx, postedTo.ID)
	if err != nil {
		return FXResult{}, err
	}
	finalTo, err = s.Finalize(ctx, finalTo.ID)
	if err != nil {
		return FXResult{}, err
	}

	return FXResult{Quote: quote, FromTx: finalFrom, ToTx: finalTo}, nil
}

// --- pure math helpers ---------------------------------------------------

// convertMinor converts fromMinor units at fromDecimals scale into a minor
// amount at toDecimals scale using the exact rate scaled × 1e8
// (to-per-from), rounding per mode. All arithmetic is exact big.Int/big.Rat;
// no float64 is used anywhere.
func convertMinor(fromMinor int64, fromDecimals, toDecimals int, scaled int64, mode string) (int64, error) {
	num := new(big.Int).Mul(big.NewInt(fromMinor), big.NewInt(scaled))
	num.Mul(num, pow10(toDecimals))
	den := new(big.Int).Mul(big.NewInt(scaleFactor), pow10(fromDecimals))
	r := new(big.Rat).SetFrac(num, den)
	return roundRat(r, mode)
}

// invertScaled computes the exact inverse of a to-per-from ×1e8 scaled rate
// (i.e. from-per-to ×1e8), rounding per mode.
func invertScaled(scaled int64, mode string) (int64, error) {
	if scaled == 0 {
		return 0, errors.New("ledger: cannot invert a zero rate")
	}
	num := new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil)
	r := new(big.Rat).SetFrac(num, big.NewInt(scaled))
	return roundRat(r, mode)
}

// roundRat rounds a non-negative-denominator big.Rat to the nearest integer
// using the given rounding mode.
func roundRat(r *big.Rat, mode string) (int64, error) {
	neg := r.Sign() < 0
	n := new(big.Int).Abs(r.Num())
	d := new(big.Int).Abs(r.Denom())
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(n, d, rem)
	switch mode {
	case "down", "":
		// truncation toward zero is already what QuoRem gives for the
		// absolute value.
	case "half_up":
		twice := new(big.Int).Mul(rem, big.NewInt(2))
		if twice.Cmp(d) >= 0 {
			q.Add(q, big.NewInt(1))
		}
	default:
		return 0, fmt.Errorf("ledger: unknown rounding mode %q", mode)
	}
	if !q.IsInt64() {
		return 0, errors.New("ledger: amount overflow")
	}
	v := q.Int64()
	if neg {
		v = -v
	}
	return v, nil
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// decimalStringToScaled parses an exact decimal string (as authored in
// fx_policy YAML, e.g. "0.99850000") into an integer scaled by 10^decimals,
// erroring if the string carries more precision than that scale using
// math/big.Rat (never float64).
func decimalStringToScaled(s string, decimals int) (int64, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok {
		return 0, fmt.Errorf("ledger: invalid decimal %q", s)
	}
	scale := pow10(decimals)
	scaledRat := new(big.Rat).Mul(r, new(big.Rat).SetInt(scale))
	if !scaledRat.IsInt() {
		return 0, fmt.Errorf("ledger: decimal %q has more than %d decimal places", s, decimals)
	}
	v := scaledRat.Num()
	if !v.IsInt64() {
		return 0, fmt.Errorf("ledger: decimal %q out of range", s)
	}
	return v.Int64(), nil
}

// findFXPair looks up the fx_policy pair covering (from, to), returning the
// pair's canonical mock_rate scaled ×1e8 (quote-per-base), its source, and
// whether the requested direction is the inverse of the pair's canonical
// base->quote direction.
func findFXPair(content map[string]any, from, to string) (pairScaled int64, source string, inverted bool, err error) {
	pairsRaw, ok := content["pairs"]
	if !ok {
		return 0, "", false, errors.New("ledger: fx_policy missing pairs")
	}
	pairs, ok := pairsRaw.([]any)
	if !ok {
		return 0, "", false, errors.New("ledger: fx_policy pairs malformed")
	}
	for _, p := range pairs {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		base, _ := m["base"].(string)
		quote, _ := m["quote"].(string)
		mockRateStr, ok := m["mock_rate"].(string)
		if !ok {
			continue
		}
		src, _ := m["source"].(string)
		switch {
		case base == from && quote == to:
			scaled, err2 := decimalStringToScaled(mockRateStr, 8)
			if err2 != nil {
				return 0, "", false, err2
			}
			return scaled, src, false, nil
		case base == to && quote == from:
			scaled, err2 := decimalStringToScaled(mockRateStr, 8)
			if err2 != nil {
				return 0, "", false, err2
			}
			return scaled, src, true, nil
		}
	}
	return 0, "", false, fmt.Errorf("ledger: no fx pair for %s/%s", from, to)
}

// rateFromPolicy returns the effective to-per-from ×1e8 scaled rate and
// source for (from, to), inverting the canonical pair rate by exact
// big.Rat arithmetic when necessary.
func rateFromPolicy(content map[string]any, from, to, mode string) (scaled int64, source string, err error) {
	pairScaled, source, inverted, err := findFXPair(content, from, to)
	if err != nil {
		return 0, "", err
	}
	if !inverted {
		return pairScaled, source, nil
	}
	scaled, err = invertScaled(pairScaled, mode)
	return scaled, source, err
}

// roundingModeFromContent reads fx_policy's rounding field, defaulting to
// half_up when absent.
func roundingModeFromContent(content map[string]any) (string, error) {
	v, ok := content["rounding"]
	if !ok {
		return "half_up", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("ledger: fx_policy rounding must be a string")
	}
	switch s {
	case "half_up", "down":
		return s, nil
	default:
		return "", fmt.Errorf("ledger: unknown rounding mode %q", s)
	}
}
