package loan

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Raven020/stableBank/internal/store"
)

// Applicant is a loan applicant's underwriting-relevant profile.
type Applicant struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	DepositAccountID         string `json:"deposit_account_id"`
	CreditScore              int    `json:"credit_score"`
	MonthlyIncomeCents       int64  `json:"monthly_income_cents"`
	ExistingDebtMonthlyCents int64  `json:"existing_debt_monthly_cents"`
	ExistingBalanceCents     int64  `json:"existing_balance_cents"`
	AccountAgeDays           int    `json:"account_age_days"`
}

// Loan is the read model returned by Get/List/Originate: base decision
// data plus everything derived from replaying loan.* events and the
// ledger.
type Loan struct {
	ID                        string        `json:"id"`
	ApplicantID               string        `json:"applicant_id"`
	PrincipalCents            int64         `json:"principal_cents"`
	TermMonths                int           `json:"term_months"`
	AnnualRateBps             int64         `json:"annual_rate_bps"`
	Tier                      string        `json:"tier"`
	RiskScore                 int           `json:"risk_score"`
	PDbps                     int64         `json:"pd_bps"`
	LGDbps                    int64         `json:"lgd_bps"`
	OriginatedAt              time.Time     `json:"originated_at"`
	ReceivableAccountID       string        `json:"receivable_account_id"`
	DepositAccountID          string        `json:"deposit_account_id"`
	Status                    string        `json:"status"` // active|repaid|defaulted
	MissedPayments            int           `json:"missed_payments"`
	OutstandingPrincipalCents int64         `json:"outstanding_principal_cents"`
	InterestPaidCents         int64         `json:"interest_paid_cents"`
	PrincipalPaidCents        int64         `json:"principal_paid_cents"`
	Schedule                  []Installment `json:"schedule"`
	DefaultReason             string        `json:"default_reason,omitempty"`
}

// Loan lifecycle statuses.
const (
	LoanActive    = "active"
	LoanRepaid    = "repaid"
	LoanDefaulted = "defaulted"
)

// --- events owned by this package -----------------------------------

const (
	eventLoanOriginated    = "loan.originated"
	eventLoanPaymentMissed = "loan.payment_missed"
	eventLoanDefaulted     = "loan.defaulted"
	eventLoanRepaid        = "loan.repaid"
)

var loanEventTypes = []string{eventLoanOriginated, eventLoanPaymentMissed, eventLoanDefaulted, eventLoanRepaid}

// loanRecord is the immutable data decided at origination time.
type loanRecord struct {
	ID                  string    `json:"id"`
	ApplicantID         string    `json:"applicant_id"`
	PrincipalCents      int64     `json:"principal_cents"`
	TermMonths          int       `json:"term_months"`
	AnnualRateBps       int64     `json:"annual_rate_bps"`
	Tier                string    `json:"tier"`
	RiskScore           int64     `json:"risk_score"`
	PDbps               int64     `json:"pd_bps"`
	LGDbps              int64     `json:"lgd_bps"`
	OriginatedAt        time.Time `json:"originated_at"`
	ReceivableAccountID string    `json:"receivable_account_id"`
	DepositAccountID    string    `json:"deposit_account_id"`
}

// originatedPayload is the loan.originated event payload: the loan record
// plus the baseline schedule computed at origination, per contract.
type originatedPayload struct {
	loanRecord
	Schedule []Installment `json:"schedule"`
}

type paymentMissedPayload struct {
	LoanID              string    `json:"loan_id"`
	InstallmentNo       int       `json:"installment_no"`
	DaysPastDue         int       `json:"days_past_due"`
	MissedPaymentsSoFar int       `json:"missed_payments_so_far"`
	LateFeeCents        int64     `json:"late_fee_cents"`
	OccurredAt          time.Time `json:"occurred_at"`
}

type defaultedPayload struct {
	LoanID          string    `json:"loan_id"`
	Reason          string    `json:"reason"`
	WrittenOffCents int64     `json:"written_off_cents"`
	OccurredAt      time.Time `json:"occurred_at"`
}

type repaidPayload struct {
	LoanID     string    `json:"loan_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

// loanState is the in-memory projection of one loan's own events (not
// counting ledger transactions, which are read live from *ledger.Service).
type loanState struct {
	record        loanRecord
	missed        map[int]bool
	defaulted     bool
	defaultReason string
	repaid        bool
}

// loanProjection is the in-memory read model for every loan, rebuilt by
// replaying loan.* events, refreshed incrementally by Seq (mirrors the
// pattern used by internal/ledger's projection).
type loanProjection struct {
	mu      sync.Mutex
	lastSeq int64
	loans   map[string]*loanState
	order   []string
}

func newLoanProjection() *loanProjection {
	return &loanProjection{loans: map[string]*loanState{}}
}

// apply folds one event into the projection. Callers must hold p.mu.
func (p *loanProjection) apply(e store.Event) error {
	switch e.Type {
	case eventLoanOriginated:
		var op originatedPayload
		if err := json.Unmarshal(e.Payload, &op); err != nil {
			return fmt.Errorf("loan: decode %s: %w", e.Type, err)
		}
		p.loans[op.ID] = &loanState{record: op.loanRecord, missed: map[int]bool{}}
		p.order = append(p.order, op.ID)
	case eventLoanPaymentMissed:
		var pm paymentMissedPayload
		if err := json.Unmarshal(e.Payload, &pm); err != nil {
			return fmt.Errorf("loan: decode %s: %w", e.Type, err)
		}
		if st, ok := p.loans[pm.LoanID]; ok {
			st.missed[pm.InstallmentNo] = true
		}
	case eventLoanDefaulted:
		var d defaultedPayload
		if err := json.Unmarshal(e.Payload, &d); err != nil {
			return fmt.Errorf("loan: decode %s: %w", e.Type, err)
		}
		if st, ok := p.loans[d.LoanID]; ok {
			st.defaulted = true
			st.defaultReason = d.Reason
		}
	case eventLoanRepaid:
		var rp repaidPayload
		if err := json.Unmarshal(e.Payload, &rp); err != nil {
			return fmt.Errorf("loan: decode %s: %w", e.Type, err)
		}
		if st, ok := p.loans[rp.LoanID]; ok {
			st.repaid = true
		}
	}
	if e.Seq > p.lastSeq {
		p.lastSeq = e.Seq
	}
	return nil
}

// refresh replays any events with Seq greater than the last one seen.
func (s *Service) refresh(ctx context.Context) error {
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	events, err := s.store.List(ctx, store.EventFilter{
		Types:    loanEventTypes,
		AfterSeq: s.proj.lastSeq,
	})
	if err != nil {
		return err
	}
	for _, e := range events {
		if err := s.proj.apply(e); err != nil {
			return err
		}
	}
	return nil
}

// applyAppended folds freshly appended events directly into the
// projection without a round trip through the store.
func (s *Service) applyAppended(events []store.Event) {
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	for _, e := range events {
		_ = s.proj.apply(e)
	}
}
