package simulate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/loan"
)

// Step type vocabulary, exactly as listed in CONTRACTS.md's D7 section.
const (
	StepAdvanceTime     = "advance_time"
	StepRepay           = "repay"
	StepMissPayment     = "miss_payment"
	StepRandomPurchases = "random_purchases"
	StepOriginate       = "originate"
	StepDeposit         = "deposit"
	StepTriggerDefault  = "trigger_default"
)

// Step is one instruction in a Scenario. It is decoded with a single
// struct (rather than one type per step) so that a scenario can be
// authored as a flat list of JSON objects; every field beyond Type is
// optional and only meaningful for certain step types (see the Step*
// constants above and validateStep below).
type Step struct {
	Type string `json:"type"`

	// advance_time
	Days int `json:"days,omitempty"`

	// repay / miss_payment / trigger_default share loan_id.
	LoanID string `json:"loan_id,omitempty"`

	// repay
	Count       int   `json:"count,omitempty"`
	AmountCents int64 `json:"amount_cents,omitempty"`

	// random_purchases (count is shared with repay above)
	AccountID string `json:"account_id,omitempty"`
	MinCents  int64  `json:"min_cents,omitempty"`
	MaxCents  int64  `json:"max_cents,omitempty"`
	Seed      int64  `json:"seed,omitempty"`

	// originate
	ApplicantID    string `json:"applicant_id,omitempty"`
	PrincipalCents int64  `json:"principal_cents,omitempty"`
	TermMonths     int    `json:"term_months,omitempty"`

	// deposit (account_id shared with random_purchases above)
	Currency string `json:"currency,omitempty"`
	Amount   string `json:"amount,omitempty"`

	// trigger_default (loan_id shared above)
	Reason string `json:"reason,omitempty"`
}

// Scenario is the POST /simulate/run-scenario request body.
type Scenario struct {
	Name  string `json:"name,omitempty"`
	Steps []Step `json:"steps"`
}

// StepResult is one entry of ScenarioResult.Results.
type StepResult struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	OK     bool   `json:"ok"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ScenarioSummary tallies a scenario run.
type ScenarioSummary struct {
	Steps       int       `json:"steps"`
	Succeeded   int       `json:"succeeded"`
	Failed      int       `json:"failed"`
	ClockBefore time.Time `json:"clock_before"`
	ClockAfter  time.Time `json:"clock_after"`
}

// ScenarioResult is the POST /simulate/run-scenario response shape.
type ScenarioResult struct {
	Name       string          `json:"name,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	Results    []StepResult    `json:"results"`
	Summary    ScenarioSummary `json:"summary"`
}

// validateStep checks step i's required fields for its declared Type,
// returning zero or more human-readable problems.
func validateStep(i int, st Step) []string {
	label := fmt.Sprintf("step %d (%s)", i, st.Type)
	var errs []string

	switch st.Type {
	case StepAdvanceTime:
		if st.Days <= 0 {
			errs = append(errs, label+": days must be a positive integer")
		}
	case StepRepay:
		if st.LoanID == "" {
			errs = append(errs, label+": loan_id is required")
		}
		if st.Count < 0 {
			errs = append(errs, label+": count must not be negative")
		}
		if st.AmountCents < 0 {
			errs = append(errs, label+": amount_cents must not be negative")
		}
	case StepMissPayment:
		if st.LoanID == "" {
			errs = append(errs, label+": loan_id is required")
		}
	case StepRandomPurchases:
		if st.AccountID == "" {
			errs = append(errs, label+": account_id is required")
		}
		if st.Count <= 0 {
			errs = append(errs, label+": count must be positive")
		}
		if st.MinCents <= 0 {
			errs = append(errs, label+": min_cents must be positive")
		}
		if st.MaxCents <= 0 || st.MaxCents < st.MinCents {
			errs = append(errs, label+": max_cents must be positive and >= min_cents")
		}
	case StepOriginate:
		if st.ApplicantID == "" {
			errs = append(errs, label+": applicant_id is required")
		}
	case StepDeposit:
		if st.AccountID == "" {
			errs = append(errs, label+": account_id is required")
		}
		if st.Currency == "" {
			errs = append(errs, label+": currency is required")
		}
		if st.Amount == "" {
			errs = append(errs, label+": amount is required")
		}
	case StepTriggerDefault:
		if st.LoanID == "" {
			errs = append(errs, label+": loan_id is required")
		}
	case "":
		errs = append(errs, label+": type is required")
	default:
		errs = append(errs, label+fmt.Sprintf(": unknown step type %q", st.Type))
	}
	return errs
}

// validateScenario validates every step before any of them run, so a
// malformed scenario never partially executes.
func validateScenario(sc Scenario) error {
	var problems []string
	if len(sc.Steps) == 0 {
		problems = append(problems, "steps must contain at least one step")
	}
	for i, st := range sc.Steps {
		problems = append(problems, validateStep(i, st)...)
	}
	if len(problems) > 0 {
		return &ValidationError{Errors: problems}
	}
	return nil
}

// RunScenario validates every step of sc up front (returning a
// *ValidationError and running nothing if any step is malformed), then
// runs the steps in order. A failing step is recorded in Results with
// OK:false and Error set; execution continues with the next step
// regardless (the summary's Failed count is how a caller notices).
func (s *Service) RunScenario(ctx context.Context, sc Scenario) (ScenarioResult, error) {
	if err := validateScenario(sc); err != nil {
		return ScenarioResult{}, err
	}

	startedAt := s.clock.Now()
	results := make([]StepResult, 0, len(sc.Steps))
	succeeded, failed := 0, 0

	for i, st := range sc.Steps {
		out, err := s.runStep(ctx, st)
		res := StepResult{Index: i, Type: st.Type, OK: err == nil}
		if err != nil {
			res.Error = err.Error()
			failed++
		} else {
			res.Output = out
			succeeded++
		}
		results = append(results, res)
	}

	finishedAt := s.clock.Now()

	return ScenarioResult{
		Name:       sc.Name,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Results:    results,
		Summary: ScenarioSummary{
			Steps:       len(sc.Steps),
			Succeeded:   succeeded,
			Failed:      failed,
			ClockBefore: startedAt,
			ClockAfter:  finishedAt,
		},
	}, nil
}

// runStep executes one already-validated step, resolving the
// $first_loan placeholder for step types that take a loan_id.
func (s *Service) runStep(ctx context.Context, st Step) (any, error) {
	switch st.Type {
	case StepAdvanceTime:
		return s.AdvanceTime(ctx, AdvanceTimeRequest{Days: st.Days})

	case StepRepay:
		loanID, err := s.resolveLoanID(ctx, st.LoanID)
		if err != nil {
			return nil, err
		}
		count := st.Count
		if count <= 0 {
			count = 1
		}
		var last loan.Loan
		for n := 0; n < count; n++ {
			last, err = s.loans.Repay(ctx, loanID, st.AmountCents)
			if err != nil {
				return nil, err
			}
		}
		return last, nil

	case StepMissPayment:
		loanID, err := s.resolveLoanID(ctx, st.LoanID)
		if err != nil {
			return nil, err
		}
		return s.loans.MissPayment(ctx, loanID)

	case StepRandomPurchases:
		return s.accounts.BulkRandomPurchases(ctx, account.BulkRequest{
			AccountID: st.AccountID,
			Count:     st.Count,
			MinCents:  st.MinCents,
			MaxCents:  st.MaxCents,
			Seed:      st.Seed,
		})

	case StepOriginate:
		return s.loans.Originate(ctx, loan.OriginateRequest{
			ApplicantID:    st.ApplicantID,
			PrincipalCents: st.PrincipalCents,
			TermMonths:     st.TermMonths,
		})

	case StepDeposit:
		amt, err := ledger.ParseAmount(st.Currency, st.Amount)
		if err != nil {
			return nil, err
		}
		return s.accounts.Deposit(ctx, st.AccountID, amt)

	case StepTriggerDefault:
		loanID, err := s.resolveLoanID(ctx, st.LoanID)
		if err != nil {
			return nil, err
		}
		return s.TriggerDefault(ctx, loanID, st.Reason)

	default:
		return nil, errors.New("simulate: unknown step type " + st.Type)
	}
}

// --- presets -------------------------------------------------------------

// Preset is one canned scenario body returned by GET /simulate/presets.
// Scenario is a ready-to-POST /simulate/run-scenario request body.
type Preset struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scenario    Scenario `json:"scenario"`
}

// Presets returns the fixed set of canned scenarios that work against the
// seeded demo data (account demo-account-1, applicants demo-applicant-1/2/3
// and the baseline loan loan.Service.Seed originates for demo-applicant-2).
// Steps that need a loan_id use the literal placeholder "$first_loan",
// which RunScenario resolves to the oldest still-active loan at the moment
// the step runs — so these presets keep working even though the seeded
// loan's real ID is generated at startup and unknown ahead of time.
func Presets() []Preset {
	return []Preset{
		{
			Name:        "month-of-activity",
			Description: "Advance a month, make the seeded loan's next payment, and run a light month of card activity.",
			Scenario: Scenario{
				Name: "month-of-activity",
				Steps: []Step{
					{Type: StepAdvanceTime, Days: 31},
					{Type: StepRepay, LoanID: firstLoanPlaceholder, Count: 1},
					{Type: StepRandomPurchases, AccountID: account.DemoAccountID, Count: 8, MinCents: 500, MaxCents: 9000},
				},
			},
		},
		{
			Name:        "stress-defaults",
			Description: "Originate a new loan, then miss three consecutive installments so the loan_servicing rule triggers an automatic default.",
			Scenario: Scenario{
				Name: "stress-defaults",
				Steps: []Step{
					{Type: StepOriginate, ApplicantID: "demo-applicant-1", PrincipalCents: 800000, TermMonths: 36},
					{Type: StepAdvanceTime, Days: 40},
					{Type: StepMissPayment, LoanID: firstLoanPlaceholder},
					{Type: StepAdvanceTime, Days: 31},
					{Type: StepMissPayment, LoanID: firstLoanPlaceholder},
					{Type: StepAdvanceTime, Days: 31},
					{Type: StepMissPayment, LoanID: firstLoanPlaceholder},
				},
			},
		},
		{
			Name:        "stablecoin-drain",
			Description: "Run enough large purchases against demo-account-1 to exhaust its USD balance so the spend_waterfall rule starts funding from USDC.",
			Scenario: Scenario{
				Name: "stablecoin-drain",
				Steps: []Step{
					{Type: StepRandomPurchases, AccountID: account.DemoAccountID, Count: 10, MinCents: 10000, MaxCents: 40000, Seed: 99},
				},
			},
		},
	}
}
