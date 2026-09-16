// Package simulate implements the D7 deliverable: the "/simulate/*" control
// surface that lets a demo operator drive the whole platform forward in
// time and force specific outcomes without waiting on real wall-clock time
// or manually issuing dozens of requests. It is the "play button" behind
// the Simulation tab of the web UI.
//
// This entire package is PoC-only scaffolding: it exposes actions (jumping
// the simulated clock, forcing a loan to default, firing arbitrary bulk
// purchases) that no real bank would ever put behind an API. Per
// cmd/stablebank/main.go's own top-of-file note, every "/simulate/*" (and
// "/demo/*") route must be excluded from any production build of this
// service.
package simulate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/loan"
	"github.com/Raven020/stableBank/internal/simclock"
)

// DefaultTriggerReason is used by TriggerDefault (and the trigger_default
// scenario step) when the caller does not supply one.
const DefaultTriggerReason = "manual_demo_trigger"

// maxTickIterations bounds how many times AdvanceTime re-runs loan.Tick
// after a single clock jump. loan.Service.Tick only misses (at most) one
// installment per active loan per call (it stops scanning a loan's
// schedule the moment it finds the first newly-overdue installment, see
// internal/loan/service.go Tick's inner "break"). A single
// /simulate/advance-time call can jump the clock by many days at once
// (e.g. 90), which can push a loan's *next* installment past its
// due+grace window too. Looping Tick here (until it reports nothing new,
// or this cap is hit) is what makes one big time jump behave like many
// small ones from the servicing rule's point of view: every installment
// that became overdue in the jump gets its own MissPayment/Default pass.
const maxTickIterations = 12

// Service is the D7 control-surface service: a thin orchestration layer
// over the shared simulated clock and the loan/account/ledger services it
// drives. It owns no persistent state of its own.
type Service struct {
	clock    *simclock.SimClock
	loans    *loan.Service
	accounts *account.Service
	book     *ledger.Service

	start time.Time
}

// New constructs a simulate Service. start is captured from clock.Now() at
// construction time and reported back by GET /simulate/clock so the UI can
// show "how far we've advanced" without keeping its own bookkeeping.
func New(clock *simclock.SimClock, loans *loan.Service, accounts *account.Service, book *ledger.Service) *Service {
	return &Service{
		clock:    clock,
		loans:    loans,
		accounts: accounts,
		book:     book,
		start:    clock.Now(),
	}
}

// ValidationError marks a request-shaped problem (HTTP 400): a malformed
// advance-time request, or a scenario that failed validation before any
// step ran. Errors holds one human-readable problem per issue found.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 1 {
		return "simulate: " + e.Errors[0]
	}
	return fmt.Sprintf("simulate: %d validation problems: %v", len(e.Errors), e.Errors)
}

// --- clock -----------------------------------------------------------

// ClockView is the GET /simulate/clock response shape.
type ClockView struct {
	Now   time.Time `json:"now"`
	Start time.Time `json:"start"`
}

// Clock returns the current simulated time alongside the time the Service
// (and therefore the simulation) started.
func (s *Service) Clock() ClockView {
	return ClockView{Now: s.clock.Now(), Start: s.start}
}

// --- advance-time -----------------------------------------------------

// AdvanceTimeRequest is the POST /simulate/advance-time request body:
// exactly one of Days (a positive integer) or To (an RFC3339 timestamp
// strictly after the current simulated time) must be set.
type AdvanceTimeRequest struct {
	Days int    `json:"days,omitempty"`
	To   string `json:"to,omitempty"`
}

// AdvanceTimeResult is the POST /simulate/advance-time response shape.
type AdvanceTimeResult struct {
	Previous     time.Time       `json:"previous"`
	Now          time.Time       `json:"now"`
	DaysAdvanced float64         `json:"days_advanced"`
	Tick         loan.TickReport `json:"tick"`
}

func validateAdvanceTimeRequest(req AdvanceTimeRequest) error {
	switch {
	case req.Days <= 0 && req.To == "":
		return &ValidationError{Errors: []string{"either days (> 0) or to (RFC3339 timestamp) is required"}}
	case req.Days > 0 && req.To != "":
		return &ValidationError{Errors: []string{"specify only one of days or to, not both"}}
	}
	return nil
}

// AdvanceTime moves the shared simulated clock forward (either by a whole
// number of days, or to an explicit RFC3339 instant), then runs
// loan.Service.Tick until it reports nothing new (see maxTickIterations)
// so every installment that fell into (or past) its grace window during
// the jump is serviced.
func (s *Service) AdvanceTime(ctx context.Context, req AdvanceTimeRequest) (AdvanceTimeResult, error) {
	if err := validateAdvanceTimeRequest(req); err != nil {
		return AdvanceTimeResult{}, err
	}

	previous := s.clock.Now()

	if req.Days > 0 {
		if _, err := s.clock.AdvanceDays(req.Days); err != nil {
			return AdvanceTimeResult{}, fmt.Errorf("simulate: %w", err)
		}
	} else {
		target, err := time.Parse(time.RFC3339, req.To)
		if err != nil {
			return AdvanceTimeResult{}, &ValidationError{Errors: []string{fmt.Sprintf("to: %v", err)}}
		}
		if _, err := s.clock.SetTo(target); err != nil {
			return AdvanceTimeResult{}, &ValidationError{Errors: []string{err.Error()}}
		}
	}

	report, err := s.tickUntilStable(ctx)
	if err != nil {
		return AdvanceTimeResult{}, err
	}

	now := s.clock.Now()
	days := float64(now.Sub(previous)) / float64(24*time.Hour)

	return AdvanceTimeResult{Previous: previous, Now: now, DaysAdvanced: days, Tick: report}, nil
}

// tickUntilStable repeatedly calls loans.Tick, accumulating every reported
// missed payment / default, stopping as soon as a call reports nothing new
// or maxTickIterations is reached.
func (s *Service) tickUntilStable(ctx context.Context) (loan.TickReport, error) {
	total := loan.TickReport{MissedPayments: []string{}, Defaults: []string{}}
	for i := 0; i < maxTickIterations; i++ {
		report, err := s.loans.Tick(ctx)
		if err != nil {
			return loan.TickReport{}, err
		}
		if len(report.MissedPayments) == 0 && len(report.Defaults) == 0 {
			break
		}
		total.MissedPayments = append(total.MissedPayments, report.MissedPayments...)
		total.Defaults = append(total.Defaults, report.Defaults...)
	}
	return total, nil
}

// --- trigger-default ---------------------------------------------------

// TriggerDefault forces loan id to default (writing off its outstanding
// receivable), independent of the servicing rule's missed-payment
// threshold. reason defaults to DefaultTriggerReason when blank.
func (s *Service) TriggerDefault(ctx context.Context, loanID string, reason string) (loan.Loan, error) {
	if loanID == "" {
		return loan.Loan{}, &ValidationError{Errors: []string{"loan_id is required"}}
	}
	if reason == "" {
		reason = DefaultTriggerReason
	}
	return s.loans.Default(ctx, loanID, reason)
}

// --- $first_loan resolution ---------------------------------------------

// firstLoanPlaceholder is the literal loan_id value scenario steps (and
// the canned Presets) may use in place of a real, generated loan ID. It
// resolves to the oldest active loan at the moment the step runs.
const firstLoanPlaceholder = "$first_loan"

// resolveLoanID resolves the $first_loan placeholder to a concrete loan
// ID; any other value passes through unchanged.
func (s *Service) resolveLoanID(ctx context.Context, id string) (string, error) {
	if id != firstLoanPlaceholder {
		return id, nil
	}
	loans, err := s.loans.List(ctx)
	if err != nil {
		return "", err
	}
	for _, l := range loans {
		if l.Status == loan.LoanActive {
			return l.ID, nil
		}
	}
	return "", errors.New("simulate: $first_loan: no active loan found")
}
