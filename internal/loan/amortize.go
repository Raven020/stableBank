package loan

import (
	"fmt"
	"math/big"
	"time"
)

// InstallmentStatus is the lifecycle of one scheduled payment.
type InstallmentStatus string

const (
	StatusScheduled InstallmentStatus = "scheduled"
	StatusPaid      InstallmentStatus = "paid"
	StatusMissed    InstallmentStatus = "missed"
)

// Installment is one row of an amortisation schedule.
type Installment struct {
	No                      int               `json:"no"`
	DueDate                 time.Time         `json:"due_date"`
	PaymentCents            int64             `json:"payment_cents"`
	PrincipalCents          int64             `json:"principal_cents"`
	InterestCents           int64             `json:"interest_cents"`
	RemainingPrincipalCents int64             `json:"remaining_principal_cents"`
	Status                  InstallmentStatus `json:"status"`
}

// monthlyRate returns the exact monthly interest rate for an annual rate
// expressed in basis points: annual_rate_bps / 120000 (bps -> decimal is
// /10000, then /12 for the monthly rate: 10000*12 = 120000).
func monthlyRate(annualRateBps int64) *big.Rat {
	return big.NewRat(annualRateBps, 120000)
}

// ratPow raises base to the n-th power using exact big.Rat arithmetic.
func ratPow(base *big.Rat, n int) *big.Rat {
	result := big.NewRat(1, 1)
	for i := 0; i < n; i++ {
		result = new(big.Rat).Mul(result, base)
	}
	return result
}

// roundHalfUpRat rounds a big.Rat to the nearest int64 using half-up
// rounding (ties round away from zero).
func roundHalfUpRat(r *big.Rat) int64 {
	neg := r.Sign() < 0
	n := new(big.Int).Abs(r.Num())
	d := new(big.Int).Abs(r.Denom())
	q, rem := new(big.Int), new(big.Int)
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

// roundHalfUpBigInt rounds the exact fraction num/den to the nearest int64
// using half-up rounding.
func roundHalfUpBigInt(num, den *big.Int) int64 {
	if den.Sign() == 0 {
		return 0
	}
	return roundHalfUpRat(new(big.Rat).SetFrac(num, den))
}

// formatHundredthsPct renders a value expressed in hundredths of a percent
// (e.g. 1723 -> "17.23") as a fixed 2dp decimal string.
func formatHundredthsPct(hundredths int64) string {
	neg := hundredths < 0
	if neg {
		hundredths = -hundredths
	}
	s := fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100)
	if neg {
		s = "-" + s
	}
	return s
}

// pctHundredths computes round_half_up(numerator/denominator * 10000),
// i.e. a percentage expressed in hundredths of a point, using exact
// big.Int arithmetic. denominator <= 0 returns 0.
func pctHundredths(numerator, denominator int64) int64 {
	if denominator <= 0 {
		return 0
	}
	num := new(big.Int).Mul(big.NewInt(numerator), big.NewInt(10000))
	return roundHalfUpBigInt(num, big.NewInt(denominator))
}

// MonthlyPayment computes the level monthly payment for amortising
// principalCents over termMonths at annualRateBps, rounded half-up to the
// nearest cent: P*r/(1-(1+r)^-n).
func MonthlyPayment(principalCents, annualRateBps int64, termMonths int) int64 {
	if termMonths <= 0 || principalCents <= 0 {
		return 0
	}
	r := monthlyRate(annualRateBps)
	if r.Sign() == 0 {
		return roundHalfUpRat(new(big.Rat).SetFrac64(principalCents, int64(termMonths)))
	}
	onePlusR := new(big.Rat).Add(big.NewRat(1, 1), r)
	pow := ratPow(onePlusR, termMonths)
	num := new(big.Rat).Mul(new(big.Rat).SetInt64(principalCents), r)
	num.Mul(num, pow)
	den := new(big.Rat).Sub(pow, big.NewRat(1, 1))
	payment := new(big.Rat).Quo(num, den)
	return roundHalfUpRat(payment)
}

// Schedule builds the full amortisation schedule for a loan. The last
// installment absorbs any rounding so the outstanding balance closes at
// exactly zero.
func Schedule(principalCents, annualRateBps int64, termMonths int, originatedAt time.Time) []Installment {
	if termMonths <= 0 {
		return nil
	}
	r := monthlyRate(annualRateBps)
	payment := MonthlyPayment(principalCents, annualRateBps, termMonths)
	out := make([]Installment, 0, termMonths)
	outstanding := principalCents
	for no := 1; no <= termMonths; no++ {
		interest := roundHalfUpRat(new(big.Rat).Mul(new(big.Rat).SetInt64(outstanding), r))
		var principal, pay int64
		if no == termMonths {
			principal = outstanding
			pay = principal + interest
		} else {
			pay = payment
			principal = pay - interest
		}
		outstanding -= principal
		out = append(out, Installment{
			No:                      no,
			DueDate:                 originatedAt.AddDate(0, no, 0),
			PaymentCents:            pay,
			PrincipalCents:          principal,
			InterestCents:           interest,
			RemainingPrincipalCents: outstanding,
			Status:                  StatusScheduled,
		})
	}
	return out
}

// WhatIfRequest describes a hypothetical extra-payment scenario.
type WhatIfRequest struct {
	ExtraMonthlyCents    int64 `json:"extra_monthly_cents,omitempty"`
	LumpSumCents         int64 `json:"lump_sum_cents,omitempty"`
	LumpSumAtInstallment int   `json:"lump_sum_at_installment,omitempty"`
}

// WhatIfTotals summarises one schedule (baseline or scenario).
type WhatIfTotals struct {
	TotalInterestCents int64 `json:"total_interest_cents"`
	PayoffInstallments int   `json:"payoff_installments"`
}

// WhatIfResult compares a baseline amortisation schedule against a
// what-if scenario with extra monthly payments and/or a lump sum applied
// at a given installment.
type WhatIfResult struct {
	Baseline           WhatIfTotals `json:"baseline"`
	Scenario           WhatIfTotals `json:"scenario"`
	InterestSavedCents int64        `json:"interest_saved_cents"`
	MonthsSaved        int          `json:"months_saved"`
}

func totalsOf(sched []Installment) WhatIfTotals {
	var interest int64
	for _, in := range sched {
		interest += in.InterestCents
	}
	return WhatIfTotals{TotalInterestCents: interest, PayoffInstallments: len(sched)}
}

// scheduleWithExtra recomputes the amortisation schedule from scratch,
// adding extraMonthly to every payment and applying lumpSum (if any) right
// after installment number lumpSumAt is paid. It stops as soon as the
// outstanding balance reaches zero, so the returned schedule may be
// shorter than the baseline term.
func scheduleWithExtra(principalCents, annualRateBps, basePayment, extraMonthly, lumpSum int64, lumpSumAt int, originatedAt time.Time, maxInstallments int) []Installment {
	r := monthlyRate(annualRateBps)
	outstanding := principalCents
	out := make([]Installment, 0, maxInstallments)
	for no := 1; outstanding > 0 && no <= maxInstallments; no++ {
		interest := roundHalfUpRat(new(big.Rat).Mul(new(big.Rat).SetInt64(outstanding), r))
		pay := basePayment + extraMonthly
		principal := pay - interest
		if principal >= outstanding {
			principal = outstanding
			pay = principal + interest
		}
		outstanding -= principal
		if no == lumpSumAt && lumpSum > 0 {
			if lumpSum >= outstanding {
				outstanding = 0
			} else {
				outstanding -= lumpSum
			}
		}
		out = append(out, Installment{
			No:                      no,
			DueDate:                 originatedAt.AddDate(0, no, 0),
			PaymentCents:            pay,
			PrincipalCents:          principal,
			InterestCents:           interest,
			RemainingPrincipalCents: outstanding,
			Status:                  StatusScheduled,
		})
	}
	return out
}

// RunWhatIf computes the baseline schedule for a loan and a scenario
// schedule with the requested extra monthly payment and/or lump sum,
// returning the comparison the HTTP contract expects.
func RunWhatIf(principalCents, annualRateBps int64, termMonths int, originatedAt time.Time, req WhatIfRequest) WhatIfResult {
	baseline := Schedule(principalCents, annualRateBps, termMonths, originatedAt)
	baseTotals := totalsOf(baseline)
	payment := MonthlyPayment(principalCents, annualRateBps, termMonths)
	maxInstallments := termMonths*4 + 12
	scenario := scheduleWithExtra(principalCents, annualRateBps, payment, req.ExtraMonthlyCents, req.LumpSumCents, req.LumpSumAtInstallment, originatedAt, maxInstallments)
	scenTotals := totalsOf(scenario)
	return WhatIfResult{
		Baseline:           baseTotals,
		Scenario:           scenTotals,
		InterestSavedCents: baseTotals.TotalInterestCents - scenTotals.TotalInterestCents,
		MonthsSaved:        baseTotals.PayoffInstallments - scenTotals.PayoffInstallments,
	}
}
