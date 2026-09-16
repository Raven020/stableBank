package simulate_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/dashboard"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/loan"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/simulate"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

// testRulesDir mirrors cmd/stablebank/main.go's RULES_DIR default, resolved
// relative to this package's directory.
const testRulesDir = "../../rules"

// ruleReaderAdapter satisfies ledger.RuleReader by delegating to a
// *rules.Engine, exactly like cmd/stablebank/main.go's own adapter.
type ruleReaderAdapter struct{ e *rules.Engine }

func (r ruleReaderAdapter) Get(ruleID string) (map[string]any, int, error) {
	return r.e.GetContent(ruleID)
}

// harness builds the full stack the way cmd/stablebank/main.go wires it
// (memstore + SimClock + rules.Engine with every evaluator registered +
// ledger + account + loan), then wraps it with a simulate.Service and a
// mux carrying every route so HTTP-level wiring (in particular: no
// duplicate-pattern panic between account's and simulate's routes) is
// exercised too.
type harness struct {
	clock    *simclock.SimClock
	engine   *rules.Engine
	ledger   *ledger.Service
	accounts *account.Service
	loans    *loan.Service
	sim      *simulate.Service
	mux      *http.ServeMux
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	st := memstore.New()
	clock := simclock.New(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC))

	engine := rules.NewEngine(st, clock, testRulesDir)
	engine.RegisterEvaluator("fx_policy", ledger.EvaluateFXPolicy)
	engine.RegisterEvaluator("depeg_policy", ledger.EvaluateDepegPolicy)
	engine.RegisterEvaluator("spend_waterfall", account.EvaluateWaterfall)
	engine.RegisterEvaluator("risk_scoring", loan.EvaluateRiskScore)
	engine.RegisterEvaluator("loan_underwriting", loan.EvaluateUnderwriting)
	engine.RegisterEvaluator("loan_servicing", loan.EvaluateServicing)
	engine.RegisterEvaluator("dashboard_thresholds", dashboard.EvaluateThresholds)
	engine.RegisterEvaluator("risk_weights", dashboard.EvaluateRiskWeights)
	engine.RegisterEvaluator("liquidity_stress", dashboard.EvaluateLiquidityStress)
	if err := engine.LoadFromDisk(ctx); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}

	rates := ledger.NewRuleRateProvider(ruleReaderAdapter{engine})
	rates.Clock = clock
	book := ledger.New(st, clock, rates)
	if err := book.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if err := book.SeedCapital(ctx, 25_000_000); err != nil {
		t.Fatalf("SeedCapital: %v", err)
	}

	accounts := account.New(book, engine, clock)
	if err := accounts.Seed(ctx); err != nil {
		t.Fatalf("account seed: %v", err)
	}
	loans := loan.New(book, engine, st, clock)
	if err := loans.Seed(ctx); err != nil {
		t.Fatalf("loan seed: %v", err)
	}

	sim := simulate.New(clock, loans, accounts, book)

	mux := http.NewServeMux()
	accounts.RegisterRoutes(mux)
	loans.RegisterRoutes(mux)
	sim.RegisterRoutes(mux)

	return &harness{clock: clock, engine: engine, ledger: book, accounts: accounts, loans: loans, sim: sim, mux: mux}
}

// firstLoanID returns the ID of the baseline loan loan.Service.Seed
// originates for demo-applicant-2.
func firstLoanID(t *testing.T, h *harness) string {
	t.Helper()
	loans, err := h.loans.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(loans) == 0 {
		t.Fatalf("expected the seeded baseline loan, found none")
	}
	return loans[0].ID
}

func TestAdvanceTime_By40Days_MissesSeededLoanPaymentAndPostsLateFee(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	loanID := firstLoanID(t, h)

	before := h.clock.Now()
	result, err := h.sim.AdvanceTime(ctx, simulate.AdvanceTimeRequest{Days: 40})
	if err != nil {
		t.Fatalf("AdvanceTime: %v", err)
	}
	if !result.Previous.Equal(before) {
		t.Fatalf("Previous = %v, want %v", result.Previous, before)
	}
	if result.DaysAdvanced != 40 {
		t.Fatalf("DaysAdvanced = %v, want 40", result.DaysAdvanced)
	}

	found := false
	for _, id := range result.Tick.MissedPayments {
		if id == loanID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected loan %s in tick.missed_payments, got %v", loanID, result.Tick.MissedPayments)
	}

	ln, err := h.loans.Get(ctx, loanID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ln.MissedPayments == 0 {
		t.Fatalf("expected the loan to record at least one missed payment")
	}

	feeTxs, err := h.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: ln.ReceivableAccountID, Kind: "loan.late_fee"})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(feeTxs) == 0 {
		t.Fatalf("expected a loan.late_fee ledger transaction on %s", ln.ReceivableAccountID)
	}
}

func TestAdvanceTime_RejectsMalformedRequest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	before := h.clock.Now()

	if _, err := h.sim.AdvanceTime(ctx, simulate.AdvanceTimeRequest{}); err == nil {
		t.Fatalf("expected an error when neither days nor to is set")
	}
	if _, err := h.sim.AdvanceTime(ctx, simulate.AdvanceTimeRequest{Days: 5, To: "2026-10-01T00:00:00Z"}); err == nil {
		t.Fatalf("expected an error when both days and to are set")
	}
	if !h.clock.Now().Equal(before) {
		t.Fatalf("clock moved despite invalid requests: before=%v now=%v", before, h.clock.Now())
	}
}

func TestTriggerDefault_WritesOffTheLoan(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	loanID := firstLoanID(t, h)

	ln, err := h.sim.TriggerDefault(ctx, loanID, "")
	if err != nil {
		t.Fatalf("TriggerDefault: %v", err)
	}
	if ln.Status != loan.LoanDefaulted {
		t.Fatalf("Status = %q, want %q", ln.Status, loan.LoanDefaulted)
	}
	if ln.DefaultReason != simulate.DefaultTriggerReason {
		t.Fatalf("DefaultReason = %q, want %q", ln.DefaultReason, simulate.DefaultTriggerReason)
	}

	writeoffs, err := h.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: ln.ReceivableAccountID, Kind: "loan.writeoff"})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(writeoffs) == 0 {
		t.Fatalf("expected a loan.writeoff ledger transaction on %s", ln.ReceivableAccountID)
	}
}

func TestTriggerDefault_RequiresLoanID(t *testing.T) {
	h := newHarness(t)
	_, err := h.sim.TriggerDefault(context.Background(), "", "")
	var verr *simulate.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *simulate.ValidationError, got %T: %v", err, err)
	}
}
