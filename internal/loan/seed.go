package loan

import "context"

// seedApplicants is the fixed demo applicant roster (CONTRACTS.md D5).
var seedApplicants = []Applicant{
	{
		ID:                       "demo-applicant-1",
		Name:                     "Demo Applicant One",
		DepositAccountID:         "demo-account-1-usd",
		CreditScore:              650,
		MonthlyIncomeCents:       650000,
		ExistingDebtMonthlyCents: 80000,
		ExistingBalanceCents:     150000,
		AccountAgeDays:           400,
	},
	{
		ID:                       "demo-applicant-2",
		Name:                     "Demo Applicant Two",
		DepositAccountID:         "demo-account-1-usd",
		CreditScore:              760,
		MonthlyIncomeCents:       900000,
		ExistingDebtMonthlyCents: 50000,
		ExistingBalanceCents:     500000,
		AccountAgeDays:           900,
	},
	{
		ID:                       "demo-applicant-3",
		Name:                     "Demo Applicant Three",
		DepositAccountID:         "demo-account-1-usd",
		CreditScore:              590,
		MonthlyIncomeCents:       300000,
		ExistingDebtMonthlyCents: 120000,
		ExistingBalanceCents:     20000,
		AccountAgeDays:           60,
	},
}

// Seed idempotently registers the demo applicants and, if no loan exists
// yet anywhere in the system, originates a baseline loan for
// demo-applicant-2 so dashboards/forecasts are non-empty out of the box.
func (s *Service) Seed(ctx context.Context) error {
	for _, a := range seedApplicants {
		s.addApplicant(a)
	}

	if err := s.refresh(ctx); err != nil {
		return err
	}
	s.proj.mu.Lock()
	hasLoan := len(s.proj.order) > 0
	s.proj.mu.Unlock()
	if hasLoan {
		return nil
	}

	_, err := s.Originate(ctx, OriginateRequest{
		ApplicantID:    "demo-applicant-2",
		PrincipalCents: 500_000,
		TermMonths:     24,
	})
	return err
}
