package loan

import (
	"context"
	"math/big"
	"sort"

	"github.com/Raven020/stableBank/internal/portfolio"
)

// Cohort summarises loans originated in the same calendar month.
type Cohort struct {
	Month            string `json:"month"` // "2026-09"
	Loans            int    `json:"loans"`
	OriginatedCents  int64  `json:"originated_cents"`
	OutstandingCents int64  `json:"outstanding_cents"`
	RepaidCents      int64  `json:"repaid_cents"`
	Defaulted        int    `json:"defaulted"`
	DefaultRatePct   string `json:"default_rate_pct"`
}

// BankForecast is the bank-facing portfolio forecast.
type BankForecast struct {
	ExpectedLossCents int64    `json:"expected_loss_cents"`
	PortfolioYieldBps int64    `json:"portfolio_yield_bps"`
	Cohorts           []Cohort `json:"cohorts"`
}

// PortfolioSnapshot implements portfolio.Source for the dashboard package.
func (s *Service) PortfolioSnapshot(ctx context.Context) ([]portfolio.LoanExposure, error) {
	loans, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]portfolio.LoanExposure, 0, len(loans))
	for _, l := range loans {
		status := portfolio.StatusActive
		switch l.Status {
		case LoanRepaid:
			status = portfolio.StatusRepaid
		case LoanDefaulted:
			status = portfolio.StatusDefaulted
		}
		out = append(out, portfolio.LoanExposure{
			LoanID:                    l.ID,
			ApplicantID:               l.ApplicantID,
			Tier:                      l.Tier,
			RiskScore:                 l.RiskScore,
			PDbps:                     l.PDbps,
			LGDbps:                    l.LGDbps,
			OriginalPrincipalCents:    l.PrincipalCents,
			OutstandingPrincipalCents: l.OutstandingPrincipalCents,
			AnnualRateBps:             l.AnnualRateBps,
			TermMonths:                l.TermMonths,
			Status:                    status,
			MissedPayments:            l.MissedPayments,
			OriginatedAt:              l.OriginatedAt,
			OriginationMonth:          l.OriginatedAt.Format("2006-01"),
			InterestPaidCents:         l.InterestPaidCents,
			PrincipalPaidCents:        l.PrincipalPaidCents,
		})
	}
	return out, nil
}

// expectedLossCents computes round_half_up(pdBps * lgdBps * eadCents / 1e8)
// using exact big.Int arithmetic (pdBps/10000 * lgdBps/10000 * ead).
func expectedLossCents(pdBps, lgdBps, eadCents int64) int64 {
	num := new(big.Int).Mul(big.NewInt(pdBps), big.NewInt(lgdBps))
	num.Mul(num, big.NewInt(eadCents))
	return roundHalfUpBigInt(num, big.NewInt(100_000_000))
}

// BankForecast aggregates the loan portfolio into an expected-loss figure,
// a weighted-average portfolio yield and per-origination-month cohorts.
func (s *Service) BankForecast(ctx context.Context) (BankForecast, error) {
	exposures, err := s.PortfolioSnapshot(ctx)
	if err != nil {
		return BankForecast{}, err
	}

	elTotal := big.NewInt(0)
	weightedRate := big.NewInt(0)
	totalOutstanding := big.NewInt(0)

	cohorts := map[string]*Cohort{}
	var months []string

	for _, e := range exposures {
		if e.Status == portfolio.StatusActive {
			el := expectedLossCents(e.PDbps, e.LGDbps, e.OutstandingPrincipalCents)
			elTotal.Add(elTotal, big.NewInt(el))
		}
		weightedRate.Add(weightedRate, new(big.Int).Mul(big.NewInt(e.AnnualRateBps), big.NewInt(e.OutstandingPrincipalCents)))
		totalOutstanding.Add(totalOutstanding, big.NewInt(e.OutstandingPrincipalCents))

		c, ok := cohorts[e.OriginationMonth]
		if !ok {
			c = &Cohort{Month: e.OriginationMonth}
			cohorts[e.OriginationMonth] = c
			months = append(months, e.OriginationMonth)
		}
		c.Loans++
		c.OriginatedCents += e.OriginalPrincipalCents
		c.OutstandingCents += e.OutstandingPrincipalCents
		c.RepaidCents += e.PrincipalPaidCents
		if e.Status == portfolio.StatusDefaulted {
			c.Defaulted++
		}
	}

	sort.Strings(months)
	outCohorts := make([]Cohort, 0, len(months))
	for _, m := range months {
		c := cohorts[m]
		c.DefaultRatePct = formatHundredthsPct(pctHundredths(int64(c.Defaulted), int64(c.Loans)))
		outCohorts = append(outCohorts, *c)
	}

	var yieldBps int64
	if totalOutstanding.Sign() > 0 {
		yieldBps = roundHalfUpBigInt(weightedRate, totalOutstanding)
	}

	return BankForecast{
		ExpectedLossCents: elTotal.Int64(),
		PortfolioYieldBps: yieldBps,
		Cohorts:           outCohorts,
	}, nil
}

// ScheduleTotals summarises the principal/interest/payment totals of a
// schedule.
type ScheduleTotals struct {
	TotalPrincipalCents int64 `json:"total_principal_cents"`
	TotalInterestCents  int64 `json:"total_interest_cents"`
	TotalPaymentCents   int64 `json:"total_payment_cents"`
}

// CustomerForecast is the customer-facing view returned by
// GET /loans/{id}/forecast: loan summary, schedule totals, the next due
// installment, affordability and a default +5,000 cents/month what-if.
type CustomerForecast struct {
	Loan           Loan              `json:"loan"`
	ScheduleTotals ScheduleTotals    `json:"schedule_totals"`
	NextDue        *Installment      `json:"next_due,omitempty"`
	Affordability  AffordabilityView `json:"affordability"`
	WhatIf         WhatIfResult      `json:"what_if"`
}

// defaultWhatIfExtraCents is the illustrative extra-payment amount shown by
// GET /loans/{id}/forecast.
const defaultWhatIfExtraCents = 5000

// CustomerForecast builds the customer-facing forecast for one loan.
func (s *Service) CustomerForecast(ctx context.Context, id string) (CustomerForecast, error) {
	loan, err := s.Get(ctx, id)
	if err != nil {
		return CustomerForecast{}, err
	}

	var totals ScheduleTotals
	var next *Installment
	for i := range loan.Schedule {
		inst := loan.Schedule[i]
		totals.TotalPrincipalCents += inst.PrincipalCents
		totals.TotalInterestCents += inst.InterestCents
		totals.TotalPaymentCents += inst.PaymentCents
		if next == nil && inst.Status == StatusScheduled {
			cp := inst
			next = &cp
		}
	}

	applicant, _ := s.getApplicant(loan.ApplicantID)
	var installmentCents int64
	if len(loan.Schedule) > 0 {
		installmentCents = loan.Schedule[0].PaymentCents
	}
	pctStr, flag := affordability(installmentCents, applicant.MonthlyIncomeCents)

	whatIf, err := s.WhatIf(ctx, id, WhatIfRequest{ExtraMonthlyCents: defaultWhatIfExtraCents})
	if err != nil {
		return CustomerForecast{}, err
	}

	return CustomerForecast{
		Loan:           loan,
		ScheduleTotals: totals,
		NextDue:        next,
		Affordability:  AffordabilityView{InstallmentToIncomePct: pctStr, Flag: flag},
		WhatIf:         whatIf,
	}, nil
}
