package loan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"sync"

	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
)

// Sentinel errors surfaced over HTTP (see writeServiceError in http.go).
var (
	ErrApplicantNotFound = errors.New("loan: applicant not found")
	ErrNotApproved       = errors.New("loan: underwriting declined")
	ErrInsufficientFunds = errors.New("loan: insufficient deposit balance")
)

// Default request values per contract.
const (
	DefaultPrincipalCents = 1_000_000
	DefaultTermMonths     = 36
)

// Service is the loan domain service: origination, servicing and the dual
// (customer + bank) forecast built on top of the ledger and rules engine.
type Service struct {
	ledger *ledger.Service
	rules  *rules.Engine
	clock  simclock.Clock
	store  store.EventStore

	proj *loanProjection

	mu         sync.Mutex
	applicants map[string]Applicant
}

// New constructs a loan Service.
func New(l *ledger.Service, r *rules.Engine, es store.EventStore, clock simclock.Clock) *Service {
	return &Service{
		ledger:     l,
		rules:      r,
		clock:      clock,
		store:      es,
		proj:       newLoanProjection(),
		applicants: map[string]Applicant{},
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- applicants -------------------------------------------------------

func (s *Service) addApplicant(a Applicant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicants[a.ID] = a
}

func (s *Service) getApplicant(id string) (Applicant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.applicants[id]
	return a, ok
}

// Applicants returns every known applicant, ordered by ID.
func (s *Service) Applicants() []Applicant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Applicant, 0, len(s.applicants))
	for _, a := range s.applicants {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// --- underwriting -------------------------------------------------------

// UnderwriteRequest is the /loan/underwrite request body.
type UnderwriteRequest struct {
	ApplicantID             string `json:"applicant_id"`
	RequestedPrincipalCents int64  `json:"requested_principal_cents,omitempty"`
	TermMonths              int    `json:"term_months,omitempty"`
}

// RuleApplicationRef is one entry of UnderwriteResult.RuleApplications.
type RuleApplicationRef struct {
	RuleID        string `json:"rule_id"`
	Version       int    `json:"version"`
	ApplicationID string `json:"application_id"`
}

// RiskView is the risk_scoring outcome as reported to the caller.
type RiskView struct {
	RiskScore int    `json:"risk_score"`
	Tier      string `json:"tier"`
	PDbps     int64  `json:"pd_bps"`
	LGDbps    int64  `json:"lgd_bps"`
}

// PricingView is the resolved pricing tier as reported to the caller.
type PricingView struct {
	AnnualRateBps     int64  `json:"annual_rate_bps"`
	Tier              string `json:"tier"`
	MaxPrincipalCents int64  `json:"max_principal_cents"`
}

// AffordabilityView reports the installment-to-income ratio and its flag.
type AffordabilityView struct {
	InstallmentToIncomePct string `json:"installment_to_income_pct"`
	Flag                   string `json:"flag"` // comfortable|stretched|unaffordable
}

// UnderwriteResult is the /loan/underwrite response shape.
type UnderwriteResult struct {
	Decision                string               `json:"decision"` // approved|declined
	Applicant               Applicant            `json:"applicant"`
	Risk                    RiskView             `json:"risk"`
	Pricing                 PricingView          `json:"pricing"`
	MonthlyInstallmentCents int64                `json:"monthly_installment_cents"`
	Affordability           AffordabilityView    `json:"affordability"`
	Reasons                 []string             `json:"reasons"`
	RuleApplications        []RuleApplicationRef `json:"rule_applications"`
}

// affordability classifies installmentCents against incomeCents:
// <=15% comfortable, <=30% stretched, else unaffordable.
func affordability(installmentCents, incomeCents int64) (string, string) {
	if incomeCents <= 0 {
		return "0.00", "unaffordable"
	}
	pctStr := formatHundredthsPct(pctHundredths(installmentCents, incomeCents))
	exact := new(big.Rat).SetFrac(big.NewInt(installmentCents*100), big.NewInt(incomeCents))
	flag := "unaffordable"
	switch {
	case exact.Cmp(big.NewRat(15, 1)) <= 0:
		flag = "comfortable"
	case exact.Cmp(big.NewRat(30, 1)) <= 0:
		flag = "stretched"
	}
	return pctStr, flag
}

// Underwrite chains risk_scoring then loan_underwriting through the rules
// engine, pricing the loan from the matched tier before evaluating
// eligibility (proposed_installment_cents must be known up front).
func (s *Service) Underwrite(ctx context.Context, req UnderwriteRequest) (UnderwriteResult, error) {
	applicant, ok := s.getApplicant(req.ApplicantID)
	if !ok {
		return UnderwriteResult{}, ErrApplicantNotFound
	}

	principal := req.RequestedPrincipalCents
	if principal <= 0 {
		principal = DefaultPrincipalCents
	}
	term := req.TermMonths
	if term <= 0 {
		term = DefaultTermMonths
	}

	riskDecision, err := s.rules.Evaluate(ctx, "risk_scoring", "applicant", applicant.ID, map[string]any{
		"credit_score":           int64(applicant.CreditScore),
		"monthly_income_cents":   applicant.MonthlyIncomeCents,
		"account_age_days":       int64(applicant.AccountAgeDays),
		"existing_balance_cents": applicant.ExistingBalanceCents,
	})
	if err != nil {
		return UnderwriteResult{}, err
	}
	riskScore, _ := riskDecision.Output["risk_score"].(int64)
	riskTier, _ := riskDecision.Output["tier"].(string)
	pdBps, _ := riskDecision.Output["pd_bps"].(int64)
	lgdBps, _ := riskDecision.Output["lgd_bps"].(int64)

	underwritingRule, err := s.rules.Get("loan_underwriting")
	if err != nil {
		return UnderwriteResult{}, err
	}
	tiersRaw, err := requireSlice(underwritingRule.Content, "pricing_tiers")
	if err != nil {
		return UnderwriteResult{}, err
	}
	sorted, err := sortedTiers(tiersRaw, "min_risk_score")
	if err != nil {
		return UnderwriteResult{}, err
	}
	tier, matched, err := firstMatchingTier(sorted, "min_risk_score", float64(riskScore))
	if err != nil {
		return UnderwriteResult{}, err
	}
	if !matched {
		tier, err = highestRateTier(sorted)
		if err != nil {
			return UnderwriteResult{}, err
		}
	}
	tierName, err := requireStr(tier, "tier")
	if err != nil {
		return UnderwriteResult{}, err
	}
	annualRateBpsF, err := requireNum(tier, "annual_rate_bps")
	if err != nil {
		return UnderwriteResult{}, err
	}
	annualRateBps := int64(annualRateBpsF)
	maxPrincipalF, err := requireNum(tier, "max_principal_cents")
	if err != nil {
		return UnderwriteResult{}, err
	}
	maxPrincipal := int64(maxPrincipalF)

	proposedInstallment := MonthlyPayment(principal, annualRateBps, term)

	underwritingDecision, err := s.rules.Evaluate(ctx, "loan_underwriting", "applicant", applicant.ID, map[string]any{
		"credit_score":                int64(applicant.CreditScore),
		"monthly_income_cents":        applicant.MonthlyIncomeCents,
		"existing_debt_monthly_cents": applicant.ExistingDebtMonthlyCents,
		"account_age_days":            int64(applicant.AccountAgeDays),
		"risk_score":                  riskScore,
		"requested_principal_cents":   principal,
		"term_months":                 int64(term),
		"proposed_installment_cents":  proposedInstallment,
	})
	if err != nil {
		return UnderwriteResult{}, err
	}

	approved, _ := underwritingDecision.Output["approved"].(bool)
	reasons, _ := underwritingDecision.Output["reasons"].([]string)
	if reasons == nil {
		reasons = []string{}
	}
	if t, ok := underwritingDecision.Output["tier"].(string); ok && t != "" {
		tierName = t
	}
	if r, ok := underwritingDecision.Output["annual_rate_bps"].(int64); ok {
		annualRateBps = r
	}

	decisionStr := "declined"
	if approved {
		decisionStr = "approved"
	}

	pctStr, flag := affordability(proposedInstallment, applicant.MonthlyIncomeCents)

	return UnderwriteResult{
		Decision:                decisionStr,
		Applicant:               applicant,
		Risk:                    RiskView{RiskScore: int(riskScore), Tier: riskTier, PDbps: pdBps, LGDbps: lgdBps},
		Pricing:                 PricingView{AnnualRateBps: annualRateBps, Tier: tierName, MaxPrincipalCents: maxPrincipal},
		MonthlyInstallmentCents: proposedInstallment,
		Affordability:           AffordabilityView{InstallmentToIncomePct: pctStr, Flag: flag},
		Reasons:                 reasons,
		RuleApplications: []RuleApplicationRef{
			{RuleID: riskDecision.RuleID, Version: riskDecision.Version, ApplicationID: riskDecision.ApplicationID},
			{RuleID: underwritingDecision.RuleID, Version: underwritingDecision.Version, ApplicationID: underwritingDecision.ApplicationID},
		},
	}, nil
}

// --- origination -------------------------------------------------------

// OriginateRequest is the /loan/originate request body.
type OriginateRequest struct {
	ApplicantID    string `json:"applicant_id"`
	PrincipalCents int64  `json:"principal_cents,omitempty"`
	TermMonths     int    `json:"term_months,omitempty"`
}

// Originate re-runs Underwrite and, if approved, opens the loan's
// receivable account, posts the disbursement and appends loan.originated.
func (s *Service) Originate(ctx context.Context, req OriginateRequest) (Loan, error) {
	principal := req.PrincipalCents
	if principal <= 0 {
		principal = DefaultPrincipalCents
	}
	term := req.TermMonths
	if term <= 0 {
		term = DefaultTermMonths
	}

	result, err := s.Underwrite(ctx, UnderwriteRequest{
		ApplicantID:             req.ApplicantID,
		RequestedPrincipalCents: principal,
		TermMonths:              term,
	})
	if err != nil {
		return Loan{}, err
	}
	if result.Decision != "approved" {
		return Loan{}, fmt.Errorf("%w: %v", ErrNotApproved, result.Reasons)
	}

	applicant := result.Applicant
	id := newID()
	receivableID := fmt.Sprintf("loan-%s-receivable", id)

	if _, err := s.ledger.OpenAccount(ctx, ledger.Account{
		ID:       receivableID,
		OwnerID:  applicant.ID,
		Type:     ledger.LoanReceivable,
		Currency: "USD",
		Name:     fmt.Sprintf("Loan %s Receivable", id),
	}); err != nil {
		return Loan{}, err
	}

	if _, err := s.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind:        "loan.disbursement",
		Description: fmt.Sprintf("Disbursement for loan %s", id),
		Currency:    "USD",
		Entries: []ledger.Entry{
			{AccountID: receivableID, Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: principal}},
			{AccountID: applicant.DepositAccountID, Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: principal}},
		},
	}); err != nil {
		return Loan{}, err
	}

	originatedAt := s.clock.Now()
	schedule := Schedule(principal, result.Pricing.AnnualRateBps, term, originatedAt)

	rec := loanRecord{
		ID:                  id,
		ApplicantID:         applicant.ID,
		PrincipalCents:      principal,
		TermMonths:          term,
		AnnualRateBps:       result.Pricing.AnnualRateBps,
		Tier:                result.Pricing.Tier,
		RiskScore:           int64(result.Risk.RiskScore),
		PDbps:               result.Risk.PDbps,
		LGDbps:              result.Risk.LGDbps,
		OriginatedAt:        originatedAt,
		ReceivableAccountID: receivableID,
		DepositAccountID:    applicant.DepositAccountID,
	}
	payload, err := json.Marshal(originatedPayload{loanRecord: rec, Schedule: schedule})
	if err != nil {
		return Loan{}, err
	}
	events, err := s.store.Append(ctx, store.Event{
		Type:        eventLoanOriginated,
		AggregateID: id,
		OccurredAt:  originatedAt,
		Payload:     payload,
	})
	if err != nil {
		return Loan{}, err
	}
	s.applyAppended(events)

	return s.Get(ctx, id)
}

// --- reads -------------------------------------------------------

// Get returns one loan, deriving its live status/schedule from loan.*
// events plus the ledger.
func (s *Service) Get(ctx context.Context, id string) (Loan, error) {
	if err := s.refresh(ctx); err != nil {
		return Loan{}, err
	}
	s.proj.mu.Lock()
	st, ok := s.proj.loans[id]
	s.proj.mu.Unlock()
	if !ok {
		return Loan{}, store.ErrNotFound
	}
	return s.buildLoan(ctx, st)
}

func (s *Service) buildLoan(ctx context.Context, st *loanState) (Loan, error) {
	s.proj.mu.Lock()
	rec := st.record
	missed := make(map[int]bool, len(st.missed))
	for k, v := range st.missed {
		missed[k] = v
	}
	defaulted := st.defaulted
	defaultReason := st.defaultReason
	repaidFlag := st.repaid
	s.proj.mu.Unlock()

	schedule := Schedule(rec.PrincipalCents, rec.AnnualRateBps, rec.TermMonths, rec.OriginatedAt)

	txs, err := s.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: rec.ReceivableAccountID, Kind: "loan.repayment"})
	if err != nil {
		return Loan{}, err
	}
	var principalPaid, interestPaid int64
	paidNos := map[int]bool{}
	for _, tx := range txs {
		no, _ := strconv.Atoi(tx.Metadata["installment_no"])
		pc, _ := strconv.ParseInt(tx.Metadata["principal_cents"], 10, 64)
		ic, _ := strconv.ParseInt(tx.Metadata["interest_cents"], 10, 64)
		principalPaid += pc
		interestPaid += ic
		paidNos[no] = true
	}
	for i := range schedule {
		no := schedule[i].No
		switch {
		case paidNos[no]:
			schedule[i].Status = StatusPaid
		case missed[no]:
			schedule[i].Status = StatusMissed
		}
	}

	balance, err := s.ledger.Balance(ctx, rec.ReceivableAccountID)
	if err != nil {
		return Loan{}, err
	}
	outstanding := balance.Minor

	status := LoanActive
	switch {
	case defaulted:
		status = LoanDefaulted
	case repaidFlag || outstanding <= 0:
		status = LoanRepaid
	}

	return Loan{
		ID:                        rec.ID,
		ApplicantID:               rec.ApplicantID,
		PrincipalCents:            rec.PrincipalCents,
		TermMonths:                rec.TermMonths,
		AnnualRateBps:             rec.AnnualRateBps,
		Tier:                      rec.Tier,
		RiskScore:                 int(rec.RiskScore),
		PDbps:                     rec.PDbps,
		LGDbps:                    rec.LGDbps,
		OriginatedAt:              rec.OriginatedAt,
		ReceivableAccountID:       rec.ReceivableAccountID,
		DepositAccountID:          rec.DepositAccountID,
		Status:                    status,
		MissedPayments:            len(missed),
		OutstandingPrincipalCents: outstanding,
		InterestPaidCents:         interestPaid,
		PrincipalPaidCents:        principalPaid,
		Schedule:                  schedule,
		DefaultReason:             defaultReason,
	}, nil
}

// List returns every loan, ordered by origination sequence.
func (s *Service) List(ctx context.Context) ([]Loan, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.proj.mu.Lock()
	order := append([]string(nil), s.proj.order...)
	s.proj.mu.Unlock()

	out := make([]Loan, 0, len(order))
	for _, id := range order {
		s.proj.mu.Lock()
		st := s.proj.loans[id]
		s.proj.mu.Unlock()
		loan, err := s.buildLoan(ctx, st)
		if err != nil {
			return nil, err
		}
		out = append(out, loan)
	}
	return out, nil
}

// Schedule returns a loan's current amortisation schedule (with paid /
// missed statuses overlaid).
func (s *Service) Schedule(ctx context.Context, id string) ([]Installment, error) {
	loan, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return loan.Schedule, nil
}

// WhatIf recomputes a loan's schedule under a hypothetical extra-payment
// scenario.
func (s *Service) WhatIf(ctx context.Context, id string, req WhatIfRequest) (WhatIfResult, error) {
	loan, err := s.Get(ctx, id)
	if err != nil {
		return WhatIfResult{}, err
	}
	return RunWhatIf(loan.PrincipalCents, loan.AnnualRateBps, loan.TermMonths, loan.OriginatedAt, req), nil
}

// --- servicing -------------------------------------------------------

func firstScheduled(sched []Installment) *Installment {
	for i := range sched {
		if sched[i].Status == StatusScheduled {
			return &sched[i]
		}
	}
	return nil
}

// Repay applies a payment to a loan's next unpaid installment (or the
// given amountCents, if positive). It debits the borrower's deposit
// account and credits the loan receivable (principal) plus interest
// income (interest).
func (s *Service) Repay(ctx context.Context, id string, amountCents int64) (Loan, error) {
	loan, err := s.Get(ctx, id)
	if err != nil {
		return Loan{}, err
	}
	if loan.Status != LoanActive {
		return Loan{}, fmt.Errorf("loan: cannot repay a %s loan", loan.Status)
	}
	next := firstScheduled(loan.Schedule)
	if next == nil {
		return Loan{}, errors.New("loan: no unpaid installments remain")
	}
	amt := amountCents
	if amt <= 0 {
		amt = next.PaymentCents
	}

	depositBal, err := s.ledger.Balance(ctx, loan.DepositAccountID)
	if err != nil {
		return Loan{}, err
	}
	if depositBal.Minor < amt {
		return Loan{}, fmt.Errorf("%w: account %s has %d, needs %d", ErrInsufficientFunds, loan.DepositAccountID, depositBal.Minor, amt)
	}

	interestPortion := next.InterestCents
	if interestPortion > amt {
		interestPortion = amt
	}
	principalPortion := amt - interestPortion

	entries := []ledger.Entry{
		{AccountID: loan.DepositAccountID, Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: amt}},
	}
	if principalPortion > 0 {
		entries = append(entries, ledger.Entry{AccountID: loan.ReceivableAccountID, Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: principalPortion}})
	}
	if interestPortion > 0 {
		entries = append(entries, ledger.Entry{AccountID: "interest-income-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: interestPortion}})
	}

	if _, err := s.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind:        "loan.repayment",
		Description: fmt.Sprintf("Repayment for loan %s installment %d", id, next.No),
		Currency:    "USD",
		Entries:     entries,
		Metadata: map[string]string{
			"installment_no":  strconv.Itoa(next.No),
			"principal_cents": strconv.FormatInt(principalPortion, 10),
			"interest_cents":  strconv.FormatInt(interestPortion, 10),
		},
	}); err != nil {
		return Loan{}, err
	}

	updated, err := s.Get(ctx, id)
	if err != nil {
		return Loan{}, err
	}
	if updated.OutstandingPrincipalCents <= 0 {
		if err := s.appendRepaidEvent(ctx, id); err != nil {
			return Loan{}, err
		}
		updated, err = s.Get(ctx, id)
		if err != nil {
			return Loan{}, err
		}
	}
	return updated, nil
}

func (s *Service) appendRepaidEvent(ctx context.Context, id string) error {
	s.proj.mu.Lock()
	st, ok := s.proj.loans[id]
	already := ok && st.repaid
	s.proj.mu.Unlock()
	if already {
		return nil
	}
	payload, err := json.Marshal(repaidPayload{LoanID: id, OccurredAt: s.clock.Now()})
	if err != nil {
		return err
	}
	events, err := s.store.Append(ctx, store.Event{
		Type:        eventLoanRepaid,
		AggregateID: id,
		OccurredAt:  s.clock.Now(),
		Payload:     payload,
	})
	if err != nil {
		return err
	}
	s.applyAppended(events)
	return nil
}

// missPaymentInternal marks the next unmissed, unpaid installment as
// missed, posting a late fee per the loan_servicing rule. It returns
// whether that rule says this loan should now default (missed count
// reaching missed_payments_to_default), leaving the actual Default call
// to the caller (Tick).
func (s *Service) missPaymentInternal(ctx context.Context, id string) (Loan, bool, error) {
	loan, err := s.Get(ctx, id)
	if err != nil {
		return Loan{}, false, err
	}
	if loan.Status != LoanActive {
		return loan, false, fmt.Errorf("loan: cannot miss a payment on a %s loan", loan.Status)
	}
	next := firstScheduled(loan.Schedule)
	if next == nil {
		return loan, false, errors.New("loan: no installment available to mark missed")
	}

	daysPastDue := int(s.clock.Now().Sub(next.DueDate).Hours() / 24)
	if daysPastDue < 0 {
		daysPastDue = 0
	}

	decision, err := s.rules.Evaluate(ctx, "loan_servicing", "loan", id, map[string]any{
		"days_past_due":          int64(daysPastDue),
		"missed_payments_so_far": int64(loan.MissedPayments),
	})
	if err != nil {
		return loan, false, err
	}
	lateFeeCents, _ := decision.Output["late_fee_cents"].(int64)
	shouldDefault, _ := decision.Output["should_default"].(bool)

	if lateFeeCents > 0 {
		if _, err := s.ledger.PostAndFinalize(ctx, ledger.Transaction{
			Kind:        "loan.late_fee",
			Description: fmt.Sprintf("Late fee for loan %s installment %d", id, next.No),
			Currency:    "USD",
			Entries: []ledger.Entry{
				{AccountID: loan.ReceivableAccountID, Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: lateFeeCents}},
				{AccountID: "fee-income-usd", Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: lateFeeCents}},
			},
		}); err != nil {
			return loan, false, err
		}
	}

	payload, err := json.Marshal(paymentMissedPayload{
		LoanID:              id,
		InstallmentNo:       next.No,
		DaysPastDue:         daysPastDue,
		MissedPaymentsSoFar: loan.MissedPayments + 1,
		LateFeeCents:        lateFeeCents,
		OccurredAt:          s.clock.Now(),
	})
	if err != nil {
		return loan, false, err
	}
	events, err := s.store.Append(ctx, store.Event{
		Type:        eventLoanPaymentMissed,
		AggregateID: id,
		OccurredAt:  s.clock.Now(),
		Payload:     payload,
	})
	if err != nil {
		return loan, false, err
	}
	s.applyAppended(events)

	updated, err := s.Get(ctx, id)
	return updated, shouldDefault, err
}

// MissPayment marks the next unpaid installment as missed and posts the
// associated late fee.
func (s *Service) MissPayment(ctx context.Context, id string) (Loan, error) {
	loan, _, err := s.missPaymentInternal(ctx, id)
	return loan, err
}

// Default writes off the loan's outstanding receivable balance and marks
// the loan defaulted.
func (s *Service) Default(ctx context.Context, id string, reason string) (Loan, error) {
	loan, err := s.Get(ctx, id)
	if err != nil {
		return Loan{}, err
	}
	if loan.Status == LoanDefaulted {
		return loan, nil
	}
	if loan.Status == LoanRepaid {
		return Loan{}, errors.New("loan: cannot default a repaid loan")
	}

	balance, err := s.ledger.Balance(ctx, loan.ReceivableAccountID)
	if err != nil {
		return Loan{}, err
	}
	writtenOff := balance.Minor
	if writtenOff > 0 {
		if _, err := s.ledger.PostAndFinalize(ctx, ledger.Transaction{
			Kind:        "loan.writeoff",
			Description: fmt.Sprintf("Write-off for loan %s", id),
			Currency:    "USD",
			Entries: []ledger.Entry{
				{AccountID: "loan-loss-expense-usd", Direction: ledger.Debit, Amount: ledger.Amount{Currency: "USD", Minor: writtenOff}},
				{AccountID: loan.ReceivableAccountID, Direction: ledger.Credit, Amount: ledger.Amount{Currency: "USD", Minor: writtenOff}},
			},
		}); err != nil {
			return Loan{}, err
		}
	}

	payload, err := json.Marshal(defaultedPayload{LoanID: id, Reason: reason, WrittenOffCents: writtenOff, OccurredAt: s.clock.Now()})
	if err != nil {
		return Loan{}, err
	}
	events, err := s.store.Append(ctx, store.Event{
		Type:        eventLoanDefaulted,
		AggregateID: id,
		OccurredAt:  s.clock.Now(),
		Payload:     payload,
	})
	if err != nil {
		return Loan{}, err
	}
	s.applyAppended(events)

	return s.Get(ctx, id)
}

// TickReport summarises the effects of one Tick call.
type TickReport struct {
	MissedPayments []string `json:"missed_payments"`
	Defaults       []string `json:"defaults"`
}

// Tick detects installments now past due+grace without payment (marking
// them missed via MissPayment) and defaults any loan whose missed count
// reaches missed_payments_to_default, per the loan_servicing rule.
func (s *Service) Tick(ctx context.Context) (TickReport, error) {
	report := TickReport{MissedPayments: []string{}, Defaults: []string{}}

	loans, err := s.List(ctx)
	if err != nil {
		return TickReport{}, err
	}
	rule, err := s.rules.Get("loan_servicing")
	if err != nil {
		return TickReport{}, err
	}
	graceF, err := requireNum(rule.Content, "grace_period_days")
	if err != nil {
		return TickReport{}, err
	}
	grace := int(graceF)
	now := s.clock.Now()

	for _, loan := range loans {
		if loan.Status != LoanActive {
			continue
		}
		for _, inst := range loan.Schedule {
			if inst.Status != StatusScheduled {
				continue
			}
			dueWithGrace := inst.DueDate.AddDate(0, 0, grace)
			if !dueWithGrace.Before(now) {
				continue
			}
			_, shouldDefault, err := s.missPaymentInternal(ctx, loan.ID)
			if err != nil {
				return TickReport{}, err
			}
			report.MissedPayments = append(report.MissedPayments, loan.ID)
			if shouldDefault {
				if _, err := s.Default(ctx, loan.ID, "missed_payments_threshold_reached"); err != nil {
					return TickReport{}, err
				}
				report.Defaults = append(report.Defaults, loan.ID)
				break
			}
		}
	}
	return report, nil
}
