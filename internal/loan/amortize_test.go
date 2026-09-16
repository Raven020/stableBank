package loan

import (
	"testing"
	"time"
)

func TestSchedule_ClosesToZeroAndReconciles(t *testing.T) {
	originatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		principal int64
		rateBps   int64
		term      int
	}{
		{1_000_000, 999, 36},
		{500_000, 1499, 24},
		{2_000_000, 699, 60},
		{300_000, 0, 12}, // zero-rate edge case
		{1_234_567, 250, 7},
	}
	for _, c := range cases {
		sched := Schedule(c.principal, c.rateBps, c.term, originatedAt)
		if len(sched) != c.term {
			t.Fatalf("principal=%d rate=%d term=%d: got %d installments, want %d", c.principal, c.rateBps, c.term, len(sched), c.term)
		}
		last := sched[len(sched)-1]
		if last.RemainingPrincipalCents != 0 {
			t.Errorf("principal=%d rate=%d term=%d: last installment leaves remaining=%d, want 0", c.principal, c.rateBps, c.term, last.RemainingPrincipalCents)
		}

		var totalPrincipal, totalInterest, totalPayment int64
		for _, inst := range sched {
			totalPrincipal += inst.PrincipalCents
			totalInterest += inst.InterestCents
			totalPayment += inst.PaymentCents
			if inst.PrincipalCents+inst.InterestCents != inst.PaymentCents {
				t.Errorf("installment %d: principal+interest (%d) != payment (%d)", inst.No, inst.PrincipalCents+inst.InterestCents, inst.PaymentCents)
			}
			if inst.DueDate.Before(originatedAt) {
				t.Errorf("installment %d due date %v before origination %v", inst.No, inst.DueDate, originatedAt)
			}
		}
		if totalPrincipal != c.principal {
			t.Errorf("principal=%d rate=%d term=%d: sum(principal)=%d, want %d", c.principal, c.rateBps, c.term, totalPrincipal, c.principal)
		}
		if totalPayment != totalPrincipal+totalInterest {
			t.Errorf("principal=%d rate=%d term=%d: total payments (%d) != principal+interest (%d)", c.principal, c.rateBps, c.term, totalPayment, totalPrincipal+totalInterest)
		}
	}
}

func TestRunWhatIf_ExtraPaymentReducesInterestAndTerm(t *testing.T) {
	originatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result := RunWhatIf(1_000_000, 999, 36, originatedAt, WhatIfRequest{ExtraMonthlyCents: 5000})

	if result.Scenario.TotalInterestCents >= result.Baseline.TotalInterestCents {
		t.Errorf("expected scenario interest (%d) < baseline interest (%d)", result.Scenario.TotalInterestCents, result.Baseline.TotalInterestCents)
	}
	if result.Scenario.PayoffInstallments >= result.Baseline.PayoffInstallments {
		t.Errorf("expected scenario payoff (%d) < baseline payoff (%d)", result.Scenario.PayoffInstallments, result.Baseline.PayoffInstallments)
	}
	if result.InterestSavedCents <= 0 {
		t.Errorf("expected positive interest saved, got %d", result.InterestSavedCents)
	}
	if result.MonthsSaved <= 0 {
		t.Errorf("expected positive months saved, got %d", result.MonthsSaved)
	}
}

func TestRunWhatIf_LumpSum(t *testing.T) {
	originatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result := RunWhatIf(1_000_000, 999, 36, originatedAt, WhatIfRequest{LumpSumCents: 200_000, LumpSumAtInstallment: 6})
	if result.Scenario.PayoffInstallments >= result.Baseline.PayoffInstallments {
		t.Errorf("expected lump sum scenario to pay off earlier: scenario=%d baseline=%d", result.Scenario.PayoffInstallments, result.Baseline.PayoffInstallments)
	}
}

func TestMonthlyPayment_ZeroRate(t *testing.T) {
	payment := MonthlyPayment(1_000_000, 0, 10)
	if payment != 100_000 {
		t.Errorf("MonthlyPayment(1_000_000, 0, 10) = %d, want 100000", payment)
	}
}
