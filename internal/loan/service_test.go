package loan

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

type testFixture struct {
	svc    *Service
	clock  *simclock.SimClock
	ledger *ledger.Service
	rules  *rules.Engine
	store  store.EventStore
}

func newFixture(t *testing.T) (*testFixture, context.Context) {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))

	engine := rules.NewEngine(st, clock, testRulesDir)
	engine.RegisterEvaluator("risk_scoring", EvaluateRiskScore)
	engine.RegisterEvaluator("loan_underwriting", EvaluateUnderwriting)
	engine.RegisterEvaluator("loan_servicing", EvaluateServicing)
	if err := engine.LoadFromDisk(ctx); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}

	book := ledger.New(st, clock, nil)
	if err := book.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if _, err := book.OpenAccount(ctx, ledger.Account{
		ID: "demo-account-1-usd", OwnerID: "demo-user", Type: ledger.CustomerDeposit, Currency: "USD",
	}); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	// Fund the shared demo deposit account so Originate/Repay have money to
	// move.
	if _, err := book.PostAndFinalize(ctx, ledger.Transaction{
		Kind:     "deposit",
		Currency: "USD",
		Entries: []ledger.Entry{
			{AccountID: "treasury-cash-usd", Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: 10_000_000}},
			{AccountID: "demo-account-1-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: 10_000_000}},
		},
	}); err != nil {
		t.Fatalf("fund deposit: %v", err)
	}

	svc := New(book, engine, st, clock)
	return &testFixture{svc: svc, clock: clock, ledger: book, rules: engine, store: st}, ctx
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestSeed_RegistersApplicantsAndBaselineLoan(t *testing.T) {
	f, ctx := newFixture(t)
	if err := f.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	applicants := f.svc.Applicants()
	if len(applicants) != 3 {
		t.Fatalf("expected 3 applicants, got %d", len(applicants))
	}
	loans, err := f.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(loans) != 1 {
		t.Fatalf("expected 1 baseline loan, got %d", len(loans))
	}
	if loans[0].ApplicantID != "demo-applicant-2" {
		t.Errorf("baseline loan applicant = %q, want demo-applicant-2", loans[0].ApplicantID)
	}

	// Seed must be idempotent: calling it again must not add a second loan.
	if err := f.svc.Seed(ctx); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	loans2, err := f.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loans2) != 1 {
		t.Fatalf("Seed not idempotent: got %d loans after second call", len(loans2))
	}
}

func TestUnderwrite_ApprovedThenDeclinedAfterHotReload(t *testing.T) {
	f, ctx := newFixture(t)
	if err := f.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	before, err := f.svc.Underwrite(ctx, UnderwriteRequest{ApplicantID: "demo-applicant-1"})
	if err != nil {
		t.Fatalf("Underwrite (before): %v", err)
	}
	if before.Decision != "approved" {
		t.Fatalf("expected approved before threshold change, got %q (reasons=%v)", before.Decision, before.Reasons)
	}

	raw, err := os.ReadFile(testRulesDir + "/loan/loan_underwriting.yaml")
	if err != nil {
		t.Fatalf("reading loan_underwriting.yaml: %v", err)
	}
	newRaw := strings.Replace(string(raw), "min_credit_score: 620", "min_credit_score: 700", 1)
	if newRaw == string(raw) {
		t.Fatalf("strings.Replace did not find min_credit_score: 620 in the raw YAML")
	}

	if _, err := f.rules.Save(ctx, "loan_underwriting", []byte(newRaw), rules.SaveOptions{
		Author:               "test",
		ChangeNote:           "demo: raise minimum credit score",
		AllowFailingExamples: true, // example 1 (credit_score 650) now fails against the new threshold
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after, err := f.svc.Underwrite(ctx, UnderwriteRequest{ApplicantID: "demo-applicant-1"})
	if err != nil {
		t.Fatalf("Underwrite (after): %v", err)
	}
	if after.Decision != "declined" {
		t.Fatalf("expected declined after hot reload, got %q", after.Decision)
	}
	if !contains(after.Reasons, ReasonCreditScoreBelowMinimum) {
		t.Errorf("expected reasons to contain %q, got %v", ReasonCreditScoreBelowMinimum, after.Reasons)
	}
}

func TestUnderwrite_Demoapplicant3Declined(t *testing.T) {
	f, ctx := newFixture(t)
	if err := f.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	result, err := f.svc.Underwrite(ctx, UnderwriteRequest{ApplicantID: "demo-applicant-3"})
	if err != nil {
		t.Fatalf("Underwrite: %v", err)
	}
	if result.Decision != "declined" {
		t.Fatalf("expected demo-applicant-3 declined, got %q", result.Decision)
	}
}

func TestOriginate_DeclinedApplicantReturnsErrNotApproved(t *testing.T) {
	f, ctx := newFixture(t)
	if err := f.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	_, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-3"})
	if err == nil {
		t.Fatal("expected an error originating a declined applicant's loan")
	}
}

func TestOriginateRepayTwice_OutstandingMatchesSchedule(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0]) // demo-applicant-1, tier B

	loan, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if loan.Status != LoanActive {
		t.Fatalf("expected active loan, got %q", loan.Status)
	}

	if _, err := f.svc.Repay(ctx, loan.ID, 0); err != nil {
		t.Fatalf("Repay 1: %v", err)
	}
	loan2, err := f.svc.Repay(ctx, loan.ID, 0)
	if err != nil {
		t.Fatalf("Repay 2: %v", err)
	}

	sched, err := f.svc.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	want := sched[1].RemainingPrincipalCents
	if loan2.OutstandingPrincipalCents != want {
		t.Errorf("outstanding = %d, want %d (schedule installment 2 remaining)", loan2.OutstandingPrincipalCents, want)
	}
	if loan2.Schedule[0].Status != StatusPaid || loan2.Schedule[1].Status != StatusPaid {
		t.Errorf("expected first two installments paid, got %v / %v", loan2.Schedule[0].Status, loan2.Schedule[1].Status)
	}
	if loan2.Schedule[2].Status != StatusScheduled {
		t.Errorf("expected third installment still scheduled, got %v", loan2.Schedule[2].Status)
	}
}

func TestTick_MissedPaymentPostsLateFee(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0])

	loan, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}

	if _, err := f.clock.AdvanceDays(40); err != nil { // past installment 1 due (+1mo) + grace (5d)
		t.Fatalf("AdvanceDays: %v", err)
	}

	report, err := f.svc.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !contains(report.MissedPayments, loan.ID) {
		t.Fatalf("expected loan %s in MissedPayments, got %v", loan.ID, report.MissedPayments)
	}
	if len(report.Defaults) != 0 {
		t.Fatalf("expected no defaults yet, got %v", report.Defaults)
	}

	updated, err := f.svc.Get(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.MissedPayments != 1 {
		t.Errorf("MissedPayments = %d, want 1", updated.MissedPayments)
	}
	if updated.Schedule[0].Status != StatusMissed {
		t.Errorf("installment 1 status = %v, want missed", updated.Schedule[0].Status)
	}

	feeBal, err := f.ledger.Balance(ctx, "fee-income-usd")
	if err != nil {
		t.Fatalf("Balance(fee-income-usd): %v", err)
	}
	if feeBal.Minor != 2500 {
		t.Errorf("fee-income-usd balance = %d, want 2500", feeBal.Minor)
	}
}

func TestTick_ThreeMissesDefaultsAndWritesOff(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0])

	loan, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}

	advanceAndTick := func(days int) TickReport {
		t.Helper()
		if _, err := f.clock.AdvanceDays(days); err != nil {
			t.Fatalf("AdvanceDays(%d): %v", days, err)
		}
		report, err := f.svc.Tick(ctx)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		return report
	}

	r1 := advanceAndTick(40) // installment 1 missed
	if !contains(r1.MissedPayments, loan.ID) || len(r1.Defaults) != 0 {
		t.Fatalf("tick1: unexpected report %+v", r1)
	}

	r2 := advanceAndTick(35) // installment 2 missed
	if !contains(r2.MissedPayments, loan.ID) || len(r2.Defaults) != 0 {
		t.Fatalf("tick2: unexpected report %+v", r2)
	}

	balanceBeforeThirdMiss, err := f.ledger.Balance(ctx, loan.ReceivableAccountID)
	if err != nil {
		t.Fatalf("Balance before tick3: %v", err)
	}

	r3 := advanceAndTick(35) // installment 3 missed -> should_default
	if !contains(r3.MissedPayments, loan.ID) {
		t.Fatalf("tick3: expected %s in MissedPayments, got %+v", loan.ID, r3)
	}
	if !contains(r3.Defaults, loan.ID) {
		t.Fatalf("tick3: expected %s in Defaults, got %+v", loan.ID, r3)
	}

	final, err := f.svc.Get(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.Status != LoanDefaulted {
		t.Fatalf("status = %q, want defaulted", final.Status)
	}
	if final.OutstandingPrincipalCents != 0 {
		t.Errorf("outstanding after writeoff = %d, want 0", final.OutstandingPrincipalCents)
	}

	const lateFeeCents = 2500 // rules/loan/loan_servicing.yaml
	wantWrittenOff := balanceBeforeThirdMiss.Minor + lateFeeCents

	lossBal, err := f.ledger.Balance(ctx, "loan-loss-expense-usd")
	if err != nil {
		t.Fatalf("Balance(loan-loss-expense-usd): %v", err)
	}
	if lossBal.Minor != wantWrittenOff {
		t.Errorf("loan-loss-expense-usd balance = %d, want %d", lossBal.Minor, wantWrittenOff)
	}
}

func TestBankForecast_MatchesHandComputation(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0]) // tier B profile
	f.svc.addApplicant(seedApplicants[1]) // tier A profile

	loan1, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate 1: %v", err)
	}
	loan2, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-2", PrincipalCents: 500_000, TermMonths: 24})
	if err != nil {
		t.Fatalf("Originate 2: %v", err)
	}

	if loan1.Tier != "B" || loan1.AnnualRateBps != 999 || loan1.PDbps != 400 || loan1.LGDbps != 4500 {
		t.Fatalf("loan1 pricing mismatch: %+v", loan1)
	}
	if loan2.Tier != "A" || loan2.AnnualRateBps != 699 || loan2.PDbps != 150 || loan2.LGDbps != 4500 {
		t.Fatalf("loan2 pricing mismatch: %+v", loan2)
	}

	fc, err := f.svc.BankForecast(ctx)
	if err != nil {
		t.Fatalf("BankForecast: %v", err)
	}

	// Hand computation: EL_i = round_half_up(pd*lgd*ead/1e8).
	el1 := int64(400) * 4500 * 300000 / 100_000_000 // exact: 5400
	el2 := int64(150) * 4500 * 500000 / 100_000_000 // exact: 3375
	wantEL := el1 + el2
	if fc.ExpectedLossCents != wantEL {
		t.Errorf("ExpectedLossCents = %d, want %d", fc.ExpectedLossCents, wantEL)
	}

	// Weighted yield: (999*300000 + 699*500000) / 800000 = 811.5 -> 812 (half-up).
	wantYield := int64(812)
	if fc.PortfolioYieldBps != wantYield {
		t.Errorf("PortfolioYieldBps = %d, want %d", fc.PortfolioYieldBps, wantYield)
	}

	if len(fc.Cohorts) != 1 {
		t.Fatalf("expected a single origination-month cohort, got %d", len(fc.Cohorts))
	}
	c := fc.Cohorts[0]
	if c.Loans != 2 {
		t.Errorf("cohort loans = %d, want 2", c.Loans)
	}
	if c.OriginatedCents != 800_000 {
		t.Errorf("cohort originated = %d, want 800000", c.OriginatedCents)
	}
	if c.DefaultRatePct != "0.00" {
		t.Errorf("cohort default rate = %q, want 0.00", c.DefaultRatePct)
	}
}

func TestPortfolioSnapshot_ImplementsPortfolioSource(t *testing.T) {
	f, ctx := newFixture(t)
	if err := f.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	exposures, err := f.svc.PortfolioSnapshot(ctx)
	if err != nil {
		t.Fatalf("PortfolioSnapshot: %v", err)
	}
	if len(exposures) != 1 {
		t.Fatalf("expected 1 exposure, got %d", len(exposures))
	}
	if exposures[0].ApplicantID != "demo-applicant-2" {
		t.Errorf("exposure applicant = %q, want demo-applicant-2", exposures[0].ApplicantID)
	}
}

func TestCustomerForecast(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0])
	loan, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	fc, err := f.svc.CustomerForecast(ctx, loan.ID)
	if err != nil {
		t.Fatalf("CustomerForecast: %v", err)
	}
	if fc.NextDue == nil || fc.NextDue.No != 1 {
		t.Errorf("expected next due installment 1, got %+v", fc.NextDue)
	}
	if fc.WhatIf.Scenario.PayoffInstallments > fc.WhatIf.Baseline.PayoffInstallments {
		t.Errorf("expected default what-if scenario to not take longer than baseline")
	}
	if fc.ScheduleTotals.TotalPrincipalCents != 300_000 {
		t.Errorf("schedule totals principal = %d, want 300000", fc.ScheduleTotals.TotalPrincipalCents)
	}
}

func TestRepay_InsufficientFunds(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0])
	loan, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	// Drain the deposit account below the next installment amount.
	sched, err := f.svc.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatal(err)
	}
	bal, err := f.ledger.Balance(ctx, "demo-account-1-usd")
	if err != nil {
		t.Fatal(err)
	}
	drain := bal.Minor - sched[0].PaymentCents + 1 // leave one cent short
	if _, err := f.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind:     "withdrawal",
		Currency: "USD",
		Entries: []ledger.Entry{
			{AccountID: "demo-account-1-usd", Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: drain}},
			{AccountID: "treasury-cash-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: drain}},
		},
	}); err != nil {
		t.Fatalf("draining deposit: %v", err)
	}

	if _, err := f.svc.Repay(ctx, loan.ID, 0); err == nil {
		t.Fatal("expected insufficient funds error")
	}
}

// sanity check that strconv is used (installment_no metadata round trips as
// a decimal string on the ledger transaction).
func TestRepay_MetadataInstallmentNo(t *testing.T) {
	f, ctx := newFixture(t)
	f.svc.addApplicant(seedApplicants[0])
	loan, err := f.svc.Originate(ctx, OriginateRequest{ApplicantID: "demo-applicant-1", PrincipalCents: 300_000, TermMonths: 12})
	if err != nil {
		t.Fatalf("Originate: %v", err)
	}
	if _, err := f.svc.Repay(ctx, loan.ID, 0); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	txs, err := f.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: loan.ReceivableAccountID, Kind: "loan.repayment"})
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 {
		t.Fatalf("expected 1 repayment tx, got %d", len(txs))
	}
	no, err := strconv.Atoi(txs[0].Metadata["installment_no"])
	if err != nil || no != 1 {
		t.Errorf("installment_no metadata = %q, want 1", txs[0].Metadata["installment_no"])
	}
}
