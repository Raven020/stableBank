// Package portfolio holds the small shared types that let the dashboard read
// loan exposures without importing the loan package (which avoids a cycle and
// lets both packages be developed independently).
package portfolio

import (
	"context"
	"time"
)

// LoanStatus mirrors the loan lifecycle derived from ledger events.
type LoanStatus string

const (
	StatusActive    LoanStatus = "active"
	StatusRepaid    LoanStatus = "repaid"
	StatusDefaulted LoanStatus = "defaulted"
)

// LoanExposure is the bank-facing view of one loan used for expected loss,
// risk-weighted assets and cohort views.
type LoanExposure struct {
	LoanID                    string     `json:"loan_id"`
	ApplicantID               string     `json:"applicant_id"`
	Tier                      string     `json:"tier"` // "A".."D" from the risk_scoring rule
	RiskScore                 int        `json:"risk_score"`
	PDbps                     int64      `json:"pd_bps"`  // probability of default, basis points
	LGDbps                    int64      `json:"lgd_bps"` // loss given default, basis points
	OriginalPrincipalCents    int64      `json:"original_principal_cents"`
	OutstandingPrincipalCents int64      `json:"outstanding_principal_cents"` // EAD
	AnnualRateBps             int64      `json:"annual_rate_bps"`
	TermMonths                int        `json:"term_months"`
	Status                    LoanStatus `json:"status"`
	MissedPayments            int        `json:"missed_payments"`
	OriginatedAt              time.Time  `json:"originated_at"`
	OriginationMonth          string     `json:"origination_month"` // "2026-09"
	InterestPaidCents         int64      `json:"interest_paid_cents"`
	PrincipalPaidCents        int64      `json:"principal_paid_cents"`
}

// Source is implemented by the loan service and consumed by the dashboard.
type Source interface {
	PortfolioSnapshot(ctx context.Context) ([]LoanExposure, error)
}
