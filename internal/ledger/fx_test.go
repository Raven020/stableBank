package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

// fakeRuleReader is a minimal RuleReader for tests, standing in for
// rules.Engine.GetContent until D2 lands.
type fakeRuleReader struct {
	content map[string]any
	version int
}

func (f fakeRuleReader) Get(ruleID string) (map[string]any, int, error) {
	return f.content, f.version, nil
}

func fxPolicyContent(rounding string) map[string]any {
	return map[string]any{
		"pairs": []any{
			map[string]any{
				"base":      "USDC",
				"quote":     "USD",
				"mock_rate": "0.99850000",
				"source":    "mock",
			},
		},
		"rounding": rounding,
	}
}

func TestRuleRateProviderInversion(t *testing.T) {
	rp := NewRuleRateProvider(fakeRuleReader{content: fxPolicyContent("half_up"), version: 1})

	direct, err := rp.Rate("USDC", "USD")
	if err != nil {
		t.Fatalf("Rate(USDC,USD): %v", err)
	}
	if direct.Scaled != 99850000 {
		t.Fatalf("USDC->USD Scaled = %d, want 99850000", direct.Scaled)
	}
	if direct.Source != "mock" {
		t.Fatalf("Source = %q, want mock", direct.Source)
	}

	inverse, err := rp.Rate("USD", "USDC")
	if err != nil {
		t.Fatalf("Rate(USD,USDC): %v", err)
	}
	// Exact inversion of 1e16/99850000, half_up rounded (see arithmetic
	// worked out in the test comment for TestQuoteContractExample).
	if inverse.Scaled != 100150225 {
		t.Fatalf("USD->USDC Scaled = %d, want 100150225", inverse.Scaled)
	}
}

// TestQuoteContractExample reproduces the exact worked example from
// CONTRACTS.md: 10000 USD cents (100.00 USD) at rate 0.99850000 USD per
// USDC, half_up rounding, converts to 100150225 USDC micro units
// (100.150225 USDC). 100.00 / 0.9985 = 100.150225... exactly divides at 6
// decimal places here, so rounding doesn't even come into play.
func TestQuoteContractExample(t *testing.T) {
	es := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rp := NewRuleRateProvider(fakeRuleReader{content: fxPolicyContent("half_up"), version: 1})
	s := New(es, clock, rp)

	quote, err := s.Quote("USD", "USDC", 10000)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if quote.To.Minor != 100150225 {
		t.Fatalf("to_amount_minor = %d, want 100150225", quote.To.Minor)
	}
	if quote.To.Currency != "USDC" {
		t.Fatalf("to currency = %q, want USDC", quote.To.Currency)
	}
	if quote.RoundingMode != "half_up" {
		t.Fatalf("RoundingMode = %q, want half_up", quote.RoundingMode)
	}
}

// TestQuoteOneCentBoundary exercises the scale boundary the task
// description calls out: 1 cent USD (fromMinor=1) converted to USDC micro
// units. Exact math: 0.01 / 0.9985 = 0.01001502... USDC = 10015.0225 micro
// units before rounding, which rounds down (half_up) to 10015 since the
// fractional remainder is well under half a micro-unit.
func TestQuoteOneCentBoundary(t *testing.T) {
	es := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rp := NewRuleRateProvider(fakeRuleReader{content: fxPolicyContent("half_up"), version: 1})
	s := New(es, clock, rp)

	quote, err := s.Quote("USD", "USDC", 1)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if quote.To.Minor != 10015 {
		t.Fatalf("to_amount_minor = %d, want 10015", quote.To.Minor)
	}
}

// TestQuoteRoundingModeBoundary verifies half_up vs down actually diverge at
// a boundary where the fractional remainder is >= 0.5. Using the same
// USD->USDC scaled rate (100150225), fromMinor=23 (23 cents) yields an exact
// remainder of 0.5175 of a micro-unit: half_up rounds up to 230346, down
// truncates to 230345.
func TestQuoteRoundingModeBoundary(t *testing.T) {
	es := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rate := Rate{From: "USD", To: "USDC", Scaled: 100150225, Source: "mock"}

	half := New(es, clock, StaticRateProvider{Rates: map[string]Rate{"USD->USDC": rate}, Mode: "half_up"})
	q, err := half.Quote("USD", "USDC", 23)
	if err != nil {
		t.Fatalf("Quote (half_up): %v", err)
	}
	if q.To.Minor != 230346 {
		t.Fatalf("half_up to_amount_minor = %d, want 230346", q.To.Minor)
	}

	down := New(memstore.New(), clock, StaticRateProvider{Rates: map[string]Rate{"USD->USDC": rate}, Mode: "down"})
	q2, err := down.Quote("USD", "USDC", 23)
	if err != nil {
		t.Fatalf("Quote (down): %v", err)
	}
	if q2.To.Minor != 230345 {
		t.Fatalf("down to_amount_minor = %d, want 230345", q2.To.Minor)
	}
}

func TestConvertLeavesTreasuryFXExposureAndLinksTransactions(t *testing.T) {
	ctx := context.Background()
	es := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rp := NewRuleRateProvider(fakeRuleReader{content: fxPolicyContent("half_up"), version: 1})
	s := New(es, clock, rp)

	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if _, err := s.OpenAccount(ctx, Account{ID: "cust-usd", OwnerID: "u1", Type: CustomerDeposit, Currency: "USD"}); err != nil {
		t.Fatalf("OpenAccount cust-usd: %v", err)
	}
	if _, err := s.OpenAccount(ctx, Account{ID: "cust-usdc", OwnerID: "u1", Type: CustomerDeposit, Currency: "USDC"}); err != nil {
		t.Fatalf("OpenAccount cust-usdc: %v", err)
	}
	if err := s.SeedCapital(ctx, 10_000_00); err != nil {
		t.Fatalf("SeedCapital: %v", err)
	}
	if _, err := s.PostAndFinalize(ctx, Transaction{
		Kind:     "deposit",
		Currency: "USD",
		Entries: []Entry{
			{AccountID: "treasury-cash-usd", Direction: Debit, Amount: Amount{"USD", 100000}},
			{AccountID: "cust-usd", Direction: Credit, Amount: Amount{"USD", 100000}},
		},
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}

	result, err := s.Convert(ctx, FXRequest{
		FromAccountID: "cust-usd",
		ToAccountID:   "cust-usdc",
		From:          Amount{Currency: "USD", Minor: 10000},
		Metadata:      map[string]string{"note": "customer fx"},
	})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if result.Quote.To.Minor != 100150225 {
		t.Fatalf("quote to_amount_minor = %d, want 100150225", result.Quote.To.Minor)
	}
	if result.FromTx.Kind != "fx" || result.ToTx.Kind != "fx" {
		t.Fatalf("expected default Kind fx, got %q / %q", result.FromTx.Kind, result.ToTx.Kind)
	}
	if result.FromTx.Status != StatusFinal || result.ToTx.Status != StatusFinal {
		t.Fatalf("expected both txs final, got %q / %q", result.FromTx.Status, result.ToTx.Status)
	}
	if result.FromTx.LinkedTxID != result.ToTx.ID {
		t.Fatalf("FromTx.LinkedTxID = %q, want %q", result.FromTx.LinkedTxID, result.ToTx.ID)
	}
	if result.ToTx.LinkedTxID != result.FromTx.ID {
		t.Fatalf("ToTx.LinkedTxID = %q, want %q", result.ToTx.LinkedTxID, result.FromTx.ID)
	}
	if result.FromTx.Metadata["fx_rate"] != result.ToTx.Metadata["fx_rate"] {
		t.Fatalf("fx_rate metadata mismatch: %q vs %q", result.FromTx.Metadata["fx_rate"], result.ToTx.Metadata["fx_rate"])
	}
	if result.FromTx.Metadata["fx_source"] != "mock" || result.ToTx.Metadata["fx_source"] != "mock" {
		t.Fatalf("fx_source metadata not propagated: %q / %q", result.FromTx.Metadata["fx_source"], result.ToTx.Metadata["fx_source"])
	}
	if result.FromTx.Metadata["note"] != "customer fx" {
		t.Fatalf("caller metadata not preserved: %+v", result.FromTx.Metadata)
	}

	custUSD, err := s.Balance(ctx, "cust-usd")
	if err != nil {
		t.Fatalf("Balance cust-usd: %v", err)
	}
	if custUSD.Minor != 100000-10000 {
		t.Fatalf("cust-usd balance = %d, want %d", custUSD.Minor, 100000-10000)
	}
	custUSDC, err := s.Balance(ctx, "cust-usdc")
	if err != nil {
		t.Fatalf("Balance cust-usdc: %v", err)
	}
	if custUSDC.Minor != 100150225 {
		t.Fatalf("cust-usdc balance = %d, want 100150225", custUSDC.Minor)
	}

	// Visible exposure on the treasury-fx accounts: the USD leg was
	// credited (natural-sign balance goes negative for a debit-normal
	// account) and the USDC leg was debited (positive), because the two
	// legs are different currencies and don't net to zero.
	fxUSD, err := s.Balance(ctx, "treasury-fx-usd")
	if err != nil {
		t.Fatalf("Balance treasury-fx-usd: %v", err)
	}
	if fxUSD.Minor != -10000 {
		t.Fatalf("treasury-fx-usd balance = %d, want -10000", fxUSD.Minor)
	}
	fxUSDC, err := s.Balance(ctx, "treasury-fx-usdc")
	if err != nil {
		t.Fatalf("Balance treasury-fx-usdc: %v", err)
	}
	if fxUSDC.Minor != 100150225 {
		t.Fatalf("treasury-fx-usdc balance = %d, want 100150225", fxUSDC.Minor)
	}
	if fxUSD.Minor == 0 || fxUSDC.Minor == 0 {
		t.Fatal("expected nonzero FX exposure on both treasury-fx accounts")
	}
}
