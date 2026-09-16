package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/dashboard"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/portfolio"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

// ruleReaderAdapter satisfies ledger.RuleReader by delegating to a
// *rules.Engine, exactly like cmd/stablebank/main.go's own adapter.
type ruleReaderAdapter struct{ e *rules.Engine }

func (r ruleReaderAdapter) Get(ruleID string) (map[string]any, int, error) {
	return r.e.GetContent(ruleID)
}

// fakePortfolioSource is a portfolio.Source stub the dashboard test controls
// directly, standing in for the (not-yet-built) loan package.
type fakePortfolioSource struct {
	exposures []portfolio.LoanExposure
}

func (f fakePortfolioSource) PortfolioSnapshot(ctx context.Context) ([]portfolio.LoanExposure, error) {
	return f.exposures, nil
}

// testHarness bundles everything needed to exercise the dashboard Service
// against a real ledger and a real rules engine.
type testHarness struct {
	clock  *simclock.SimClock
	engine *rules.Engine
	ledger *ledger.Service
	dash   *dashboard.Service
}

func newHarness(t *testing.T, exposures []portfolio.LoanExposure) *testHarness {
	t.Helper()
	st := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	engine := rules.NewEngine(st, clock, testRulesDir)
	engine.RegisterEvaluator("dashboard_thresholds", dashboard.EvaluateThresholds)
	engine.RegisterEvaluator("risk_weights", dashboard.EvaluateRiskWeights)
	engine.RegisterEvaluator("liquidity_stress", dashboard.EvaluateLiquidityStress)
	if err := engine.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}

	rates := ledger.NewRuleRateProvider(ruleReaderAdapter{engine})
	rates.Clock = clock
	l := ledger.New(st, clock, rates)

	src := fakePortfolioSource{exposures: exposures}
	dash := dashboard.New(l, engine, src, clock)

	return &testHarness{clock: clock, engine: engine, ledger: l, dash: dash}
}

// seedBaseline opens system accounts, seeds capital, opens one customer
// deposit account and one loan receivable account, then posts a deposit and
// a loan disbursement so the ledger has a small, hand-computable state:
//
//	equity_capital (USD)  = 1,000,000 cents ($10,000.00)
//	treasury_cash (USD)   = 1,500,000 cents (1,000,000 capital + 500,000 deposit)
//	customer_deposit (USD)=   700,000 cents (500,000 deposit + 200,000 loan proceeds)
//	loan_receivable (USD) =   200,000 cents
func seedBaseline(t *testing.T, h *testHarness) {
	t.Helper()
	ctx := context.Background()

	if err := h.ledger.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if err := h.ledger.SeedCapital(ctx, 1_000_000); err != nil {
		t.Fatalf("SeedCapital: %v", err)
	}
	if _, err := h.ledger.OpenAccount(ctx, ledger.Account{
		ID: "demo-account-1-usd", OwnerID: "demo-user", Type: ledger.CustomerDeposit, Currency: "USD", Name: "Demo customer (USD)",
	}); err != nil {
		t.Fatalf("OpenAccount customer: %v", err)
	}
	if _, err := h.ledger.OpenAccount(ctx, ledger.Account{
		ID: "loan-L1-receivable", OwnerID: "system", Type: ledger.LoanReceivable, Currency: "USD", Name: "Loan L1 receivable",
	}); err != nil {
		t.Fatalf("OpenAccount loan: %v", err)
	}

	if _, err := h.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind: "deposit", Description: "customer deposit", Currency: "USD",
		Entries: []ledger.Entry{
			{AccountID: "treasury-cash-usd", Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: 500_000}},
			{AccountID: "demo-account-1-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: 500_000}},
		},
	}); err != nil {
		t.Fatalf("post deposit: %v", err)
	}

	if _, err := h.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind: "loan.disbursement", Description: "loan L1 disbursement", Currency: "USD",
		Entries: []ledger.Entry{
			{AccountID: "loan-L1-receivable", Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: 200_000}},
			{AccountID: "demo-account-1-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: 200_000}},
		},
	}); err != nil {
		t.Fatalf("post loan disbursement: %v", err)
	}
}

func TestLedgerBook_Totals(t *testing.T) {
	h := newHarness(t, nil)
	seedBaseline(t, h)

	book, err := h.dash.LedgerBook(context.Background())
	if err != nil {
		t.Fatalf("LedgerBook: %v", err)
	}

	// treasury_cash (1,500,000) + customer_deposit (700,000) = 2,200,000
	if book.Totals.TotalFiatCents != 2_200_000 {
		t.Errorf("TotalFiatCents = %d, want %d", book.Totals.TotalFiatCents, 2_200_000)
	}
	if book.Totals.TotalUsdcMicro != 0 {
		t.Errorf("TotalUsdcMicro = %d, want 0", book.Totals.TotalUsdcMicro)
	}
	if book.ByType["customer_deposit"]["USD"] != 700_000 {
		t.Errorf("customer_deposit USD = %d, want 700000", book.ByType["customer_deposit"]["USD"])
	}
	if book.ByType["treasury_cash"]["USD"] != 1_500_000 {
		t.Errorf("treasury_cash USD = %d, want 1500000", book.ByType["treasury_cash"]["USD"])
	}
	if book.ByType["loan_receivable"]["USD"] != 200_000 {
		t.Errorf("loan_receivable USD = %d, want 200000", book.ByType["loan_receivable"]["USD"])
	}
	if book.AsOf.IsZero() {
		t.Error("AsOf should not be zero")
	}
}

func TestFlows_ClassificationAndWindow(t *testing.T) {
	h := newHarness(t, nil)
	seedBaseline(t, h) // posts one "deposit" tx of 500,000 at t0

	ctx := context.Background()
	t0 := h.clock.Now()

	if _, err := h.clock.AdvanceDays(40); err != nil {
		t.Fatalf("AdvanceDays: %v", err)
	}
	tPurchase := h.clock.Now()

	if _, err := h.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind: "purchase", Description: "coffee", Currency: "USD",
		Entries: []ledger.Entry{
			{AccountID: "demo-account-1-usd", Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: 5_000}},
			{AccountID: "merchant-settlement-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: 5_000}},
		},
	}); err != nil {
		t.Fatalf("post purchase: %v", err)
	}

	if _, err := h.clock.AdvanceDays(1); err != nil {
		t.Fatalf("AdvanceDays: %v", err)
	}
	now := h.clock.Now()

	// Wide window: the deposit, the loan disbursement (also seeded at t0)
	// and the purchase are all visible. loan.disbursement (200,000) is an
	// outflow kind alongside purchase (5,000), so wide outflows = 205,000.
	wide, err := h.dash.Flows(ctx, t0, now)
	if err != nil {
		t.Fatalf("Flows (wide): %v", err)
	}
	if wide.InflowsCents != 500_000 {
		t.Errorf("wide InflowsCents = %d, want 500000", wide.InflowsCents)
	}
	if wide.OutflowsCents != 205_000 {
		t.Errorf("wide OutflowsCents = %d, want 205000", wide.OutflowsCents)
	}
	if wide.NetCents != 295_000 {
		t.Errorf("wide NetCents = %d, want 295000", wide.NetCents)
	}
	foundDeposit, foundPurchase, foundDisbursement := false, false, false
	for _, bd := range wide.ByKind {
		switch bd.Kind {
		case "deposit":
			foundDeposit = true
			if bd.UsdEquivalentCents != 500_000 || bd.Count != 1 {
				t.Errorf("deposit breakdown = %+v", bd)
			}
		case "purchase":
			foundPurchase = true
			if bd.UsdEquivalentCents != 5_000 || bd.Count != 1 {
				t.Errorf("purchase breakdown = %+v", bd)
			}
		case "loan.disbursement":
			foundDisbursement = true
			if bd.UsdEquivalentCents != 200_000 || bd.Count != 1 {
				t.Errorf("loan.disbursement breakdown = %+v", bd)
			}
		}
	}
	if !foundDeposit || !foundPurchase || !foundDisbursement {
		t.Errorf("expected deposit, purchase and loan.disbursement in by_kind, got %+v", wide.ByKind)
	}

	// Trailing 30-day window from `now` (t0+41d): the deposit at t0 falls
	// outside [now-30d, now), only the purchase at t0+40d remains.
	narrow, err := h.dash.Flows(ctx, now.AddDate(0, 0, -30), now)
	if err != nil {
		t.Fatalf("Flows (narrow): %v", err)
	}
	if narrow.InflowsCents != 0 {
		t.Errorf("narrow InflowsCents = %d, want 0 (deposit should be out of window)", narrow.InflowsCents)
	}
	if narrow.OutflowsCents != 5_000 {
		t.Errorf("narrow OutflowsCents = %d, want 5000", narrow.OutflowsCents)
	}
	if tPurchase.Before(t0) {
		t.Fatal("test setup bug: tPurchase should be after t0")
	}
}

// TestRatios_HandComputed pins the exact expected values for LTD, LCR,
// NSFR and CAR against the seeded baseline state plus one active tier-B
// loan exposure, worked out by hand in the comments below.
func TestRatios_HandComputed(t *testing.T) {
	exposures := []portfolio.LoanExposure{
		{
			LoanID: "L1", ApplicantID: "demo-applicant-1", Tier: "B", Status: portfolio.StatusActive,
			OutstandingPrincipalCents: 200_000,
		},
	}
	h := newHarness(t, exposures)
	seedBaseline(t, h)

	ratios, err := h.dash.Ratios(context.Background())
	if err != nil {
		t.Fatalf("Ratios: %v", err)
	}

	// loans=200,000 deposits=700,000 -> 200000/700000*100 = 28.571428... -> 28.57
	if ratios.LoanToDeposit.ValuePct != 28.57 {
		t.Errorf("LoanToDeposit.ValuePct = %v, want 28.57", ratios.LoanToDeposit.ValuePct)
	}
	if ratios.LoanToDeposit.Status != "ok" {
		t.Errorf("LoanToDeposit.Status = %q, want ok", ratios.LoanToDeposit.Status)
	}
	if ratios.LoanToDeposit.NumeratorCents != 200_000 || ratios.LoanToDeposit.DenominatorCents != 700_000 {
		t.Errorf("LoanToDeposit numerator/denominator = %d/%d, want 200000/700000",
			ratios.LoanToDeposit.NumeratorCents, ratios.LoanToDeposit.DenominatorCents)
	}

	// hqla = treasury_cash USD (haircut 0%) = 1,500,000; net_outflows =
	// deposits(700,000)*10% = 70,000; lcr = 1500000/70000*100 = 2142.857...
	// -> 2142.86
	if ratios.LCR.ValuePct != 2142.86 {
		t.Errorf("LCR.ValuePct = %v, want 2142.86", ratios.LCR.ValuePct)
	}
	if ratios.LCR.DenominatorCents != 70_000 {
		t.Errorf("LCR.DenominatorCents = %d, want 70000", ratios.LCR.DenominatorCents)
	}
	if ratios.LCR.Status != "ok" {
		t.Errorf("LCR.Status = %q, want ok", ratios.LCR.Status)
	}

	// asf = deposits(700,000)*90% + equity(1,000,000)*100% = 630,000+1,000,000=1,630,000
	// rsf = loans(200,000)*85% + treasury_cash(1,500,000)*5% + treasury_fx(0)*50% = 170,000+75,000=245,000
	// nsfr = 1630000/245000*100 = 665.306122... -> 665.31
	if ratios.NSFR.ValuePct != 665.31 {
		t.Errorf("NSFR.ValuePct = %v, want 665.31", ratios.NSFR.ValuePct)
	}
	if ratios.NSFR.NumeratorCents != 1_630_000 || ratios.NSFR.DenominatorCents != 245_000 {
		t.Errorf("NSFR numerator/denominator = %d/%d, want 1630000/245000",
			ratios.NSFR.NumeratorCents, ratios.NSFR.DenominatorCents)
	}

	// capital = equity(1,000,000)+interest(0)+fee(0)-loanloss(0) = 1,000,000
	// rwa = ead(200,000) * tier-B weight(75%) = 150,000
	// car = 1000000/150000*100 = 666.666... -> 666.67
	if ratios.CapitalAdequacy.ValuePct != 666.67 {
		t.Errorf("CapitalAdequacy.ValuePct = %v, want 666.67", ratios.CapitalAdequacy.ValuePct)
	}
	if ratios.CapitalAdequacy.DenominatorCents != 150_000 {
		t.Errorf("CapitalAdequacy.DenominatorCents = %d, want 150000", ratios.CapitalAdequacy.DenominatorCents)
	}
	if ratios.CapitalAdequacy.Status != "ok" {
		t.Errorf("CapitalAdequacy.Status = %q, want ok", ratios.CapitalAdequacy.Status)
	}

	for _, m := range []dashboard.Metric{ratios.LoanToDeposit, ratios.LCR, ratios.NSFR, ratios.CapitalAdequacy} {
		if m.Disclaimer == "" {
			t.Error("expected every metric to carry a disclaimer")
		}
		if m.ModeledOn == "" {
			t.Error("expected every metric to carry modeled_on")
		}
		if m.RuleApplicationID == "" {
			t.Error("expected every metric to carry a rule_application_id")
		}
	}
}

// TestRatios_ThresholdHotReload proves that editing dashboard_thresholds
// through rules.Engine.Save takes effect immediately (hot reload), by
// raising lcr_minimum_pct above the computed LCR and observing the status
// flip from "ok" to "below_target".
func TestRatios_ThresholdHotReload(t *testing.T) {
	exposures := []portfolio.LoanExposure{
		{LoanID: "L1", Tier: "B", Status: portfolio.StatusActive, OutstandingPrincipalCents: 200_000},
	}
	h := newHarness(t, exposures)
	seedBaseline(t, h)

	ctx := context.Background()
	before, err := h.dash.Ratios(ctx)
	if err != nil {
		t.Fatalf("Ratios (before): %v", err)
	}
	if before.LCR.Status != "ok" {
		t.Fatalf("precondition failed: LCR.Status = %q, want ok (value_pct=%v)", before.LCR.Status, before.LCR.ValuePct)
	}

	newYAML := []byte(`
rule_id: dashboard_thresholds
rule_type: dashboard_thresholds
version: 1
domain: dashboard
description: raised lcr target for hot-reload test
rationale: test
owner: finance@stablebank.demo
last_reviewed: "2026-08-01"

targets:
  lcr_minimum_pct: 5000
  nsfr_minimum_pct: 100
  capital_adequacy_minimum_pct: 10.5
  loan_to_deposit_maximum_pct: 90

examples:
  - name: all ratios within target
    input: { lcr_pct: 120, nsfr_pct: 110, capital_adequacy_pct: 12.0, loan_to_deposit_pct: 70 }
    expected: { lcr: ok, nsfr: ok, capital_adequacy: ok, loan_to_deposit: ok }
  - name: all ratios breach their target
    input: { lcr_pct: 90, nsfr_pct: 95, capital_adequacy_pct: 9.0, loan_to_deposit_pct: 95 }
    expected: { lcr: below_target, nsfr: below_target, capital_adequacy: below_target, loan_to_deposit: above_maximum }
`)
	if _, err := h.engine.Save(ctx, "dashboard_thresholds", newYAML, rules.SaveOptions{
		Author: "test", ChangeNote: "raise lcr target", AllowFailingExamples: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after, err := h.dash.Ratios(ctx)
	if err != nil {
		t.Fatalf("Ratios (after): %v", err)
	}
	if after.LCR.Status != "below_target" {
		t.Errorf("after raising lcr_minimum_pct to 5000, LCR.Status = %q, want below_target (value_pct=%v)", after.LCR.Status, after.LCR.ValuePct)
	}
	if after.LCR.TargetPct != 5000 {
		t.Errorf("LCR.TargetPct = %v, want 5000", after.LCR.TargetPct)
	}
}

func TestRatios_ZeroDenominatorIsNA(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.ledger.EnsureSystemAccounts(context.Background()); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	// No capital, no deposits, no loans seeded: every denominator is zero.
	ratios, err := h.dash.Ratios(context.Background())
	if err != nil {
		t.Fatalf("Ratios: %v", err)
	}
	if ratios.LoanToDeposit.Status != "n/a" || ratios.LoanToDeposit.ValuePct != 0 {
		t.Errorf("LoanToDeposit = %+v, want status n/a value 0", ratios.LoanToDeposit)
	}
	if ratios.LCR.Status != "n/a" || ratios.LCR.ValuePct != 0 {
		t.Errorf("LCR = %+v, want status n/a value 0", ratios.LCR)
	}
	if ratios.CapitalAdequacy.Status != "n/a" || ratios.CapitalAdequacy.ValuePct != 0 {
		t.Errorf("CapitalAdequacy = %+v, want status n/a value 0", ratios.CapitalAdequacy)
	}
}

func TestHTTPRoutes(t *testing.T) {
	exposures := []portfolio.LoanExposure{
		{LoanID: "L1", Tier: "B", Status: portfolio.StatusActive, OutstandingPrincipalCents: 200_000},
	}
	h := newHarness(t, exposures)
	seedBaseline(t, h)

	mux := http.NewServeMux()
	h.dash.RegisterRoutes(mux)

	for _, path := range []string{
		"/dashboard/summary",
		"/dashboard/ledger-book",
		"/dashboard/flows",
		"/dashboard/flows?window_days=7",
		"/dashboard/ratios",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
}
