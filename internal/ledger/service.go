package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
)

// Service is the event-sourced ledger. Every write appends store.Event rows;
// every read is derived by replaying those events into an in-memory
// projection cache that is refreshed incrementally (by Seq).
type Service struct {
	store store.EventStore
	clock simclock.Clock
	rates RateProvider

	proj *projection
}

// New constructs a ledger Service. rates may be nil if FX features are not
// needed (Quote/Convert will then error).
func New(es store.EventStore, clock simclock.Clock, rates RateProvider) *Service {
	return &Service{store: es, clock: clock, rates: rates, proj: newProjection()}
}

// newID returns a random 16-byte hex identifier, mirroring
// memstore.NewID's shape without importing memstore from non-test code.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- accounts ---------------------------------------------------------

type systemAccountSpec struct {
	ID       string
	Type     AccountType
	Currency string
	Name     string
}

// systemAccounts lists every well-known account EnsureSystemAccounts opens.
var systemAccounts = []systemAccountSpec{
	{"treasury-cash-usd", TreasuryCash, "USD", "Treasury Cash (USD)"},
	{"treasury-cash-usdc", TreasuryCash, "USDC", "Treasury Cash (USDC)"},
	{"treasury-fx-usd", TreasuryFX, "USD", "Treasury FX (USD)"},
	{"treasury-fx-usdc", TreasuryFX, "USDC", "Treasury FX (USDC)"},
	{"merchant-settlement-usd", MerchantSettlement, "USD", "Merchant Settlement (USD)"},
	{"merchant-settlement-usdc", MerchantSettlement, "USDC", "Merchant Settlement (USDC)"},
	{"interest-income-usd", InterestIncome, "USD", "Interest Income (USD)"},
	{"fee-income-usd", FeeIncome, "USD", "Fee Income (USD)"},
	{"loan-loss-expense-usd", LoanLossExpense, "USD", "Loan Loss Expense (USD)"},
	{"equity-capital-usd", EquityCapital, "USD", "Equity Capital (USD)"},
}

// EnsureSystemAccounts idempotently opens every well-known system account.
func (s *Service) EnsureSystemAccounts(ctx context.Context) error {
	if err := s.refresh(ctx); err != nil {
		return err
	}
	for _, spec := range systemAccounts {
		s.proj.mu.Lock()
		_, exists := s.proj.accounts[spec.ID]
		s.proj.mu.Unlock()
		if exists {
			continue
		}
		if _, err := s.OpenAccount(ctx, Account{
			ID:       spec.ID,
			OwnerID:  "system",
			Type:     spec.Type,
			Currency: spec.Currency,
			Name:     spec.Name,
		}); err != nil {
			return err
		}
	}
	return nil
}

// SeedCapital idempotently (by Kind "capital.seed") posts and finalizes the
// initial capital transaction: debit treasury-cash-usd / credit
// equity-capital-usd.
func (s *Service) SeedCapital(ctx context.Context, usdCents int64) error {
	if err := s.refresh(ctx); err != nil {
		return err
	}
	s.proj.mu.Lock()
	for _, tx := range s.proj.txs {
		if tx.Kind == "capital.seed" {
			s.proj.mu.Unlock()
			return nil
		}
	}
	s.proj.mu.Unlock()

	_, err := s.PostAndFinalize(ctx, Transaction{
		Kind:        "capital.seed",
		Description: "Initial capital seed",
		Currency:    "USD",
		Entries: []Entry{
			{AccountID: "treasury-cash-usd", Direction: Debit, Amount: Amount{Currency: "USD", Minor: usdCents}},
			{AccountID: "equity-capital-usd", Direction: Credit, Amount: Amount{Currency: "USD", Minor: usdCents}},
		},
	})
	return err
}

// OpenAccount opens a new account, appending a ledger.account.opened event.
func (s *Service) OpenAccount(ctx context.Context, a Account) (Account, error) {
	if a.ID == "" {
		return Account{}, errors.New("ledger: account id required")
	}
	if _, ok := Currencies[a.Currency]; !ok {
		return Account{}, fmt.Errorf("ledger: unknown currency %q", a.Currency)
	}
	if err := s.refresh(ctx); err != nil {
		return Account{}, err
	}
	s.proj.mu.Lock()
	_, exists := s.proj.accounts[a.ID]
	s.proj.mu.Unlock()
	if exists {
		return Account{}, fmt.Errorf("ledger: account %q already exists", a.ID)
	}

	a.OpenedAt = s.clock.Now()
	payload, err := json.Marshal(a)
	if err != nil {
		return Account{}, err
	}
	events, err := s.store.Append(ctx, store.Event{
		Type:        eventAccountOpened,
		AggregateID: a.ID,
		OccurredAt:  a.OpenedAt,
		Payload:     payload,
	})
	if err != nil {
		return Account{}, err
	}
	s.applyAppended(events)
	return a, nil
}

// GetAccount returns one account by ID.
func (s *Service) GetAccount(ctx context.Context, id string) (Account, error) {
	if err := s.refresh(ctx); err != nil {
		return Account{}, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	a, ok := s.proj.accounts[id]
	if !ok {
		return Account{}, store.ErrNotFound
	}
	return a, nil
}

// ListAccounts returns every account, ordered by ID.
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	out := make([]Account, 0, len(s.proj.accounts))
	for _, a := range s.proj.accounts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// --- transactions -------------------------------------------------------

// validate enforces Post's rules: >=2 entries, single currency matching
// tx.Currency, every account exists and matches that currency, amounts
// positive, debits == credits > 0. Callers must have already refreshed the
// projection.
func (s *Service) validate(tx Transaction) error {
	if len(tx.Entries) < 2 {
		return errors.New("ledger: transaction requires at least 2 entries")
	}
	if _, ok := Currencies[tx.Currency]; !ok {
		return fmt.Errorf("ledger: unknown currency %q", tx.Currency)
	}
	var totalDebit, totalCredit int64
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	for _, e := range tx.Entries {
		if e.Amount.Currency != tx.Currency {
			return fmt.Errorf("ledger: entry currency %q does not match transaction currency %q", e.Amount.Currency, tx.Currency)
		}
		if e.Amount.Minor <= 0 {
			return errors.New("ledger: entry amount must be positive")
		}
		acc, ok := s.proj.accounts[e.AccountID]
		if !ok {
			return fmt.Errorf("ledger: account %q does not exist", e.AccountID)
		}
		if acc.Currency != tx.Currency {
			return fmt.Errorf("ledger: account %q currency %q does not match transaction currency %q", e.AccountID, acc.Currency, tx.Currency)
		}
		switch e.Direction {
		case Debit:
			totalDebit += e.Amount.Minor
		case Credit:
			totalCredit += e.Amount.Minor
		default:
			return fmt.Errorf("ledger: invalid entry direction %q", e.Direction)
		}
	}
	if totalDebit != totalCredit {
		return fmt.Errorf("ledger: unbalanced transaction: debits %d != credits %d", totalDebit, totalCredit)
	}
	if totalDebit <= 0 {
		return errors.New("ledger: transaction total must be positive")
	}
	return nil
}

// Post validates and appends a new pending transaction.
func (s *Service) Post(ctx context.Context, tx Transaction) (Transaction, error) {
	if err := s.refresh(ctx); err != nil {
		return Transaction{}, err
	}
	if err := s.validate(tx); err != nil {
		return Transaction{}, err
	}
	if tx.ID == "" {
		tx.ID = newID()
	}
	tx.Status = StatusPending
	tx.OccurredAt = s.clock.Now()
	if err := s.appendTxEvent(ctx, eventTxPosted, tx); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}

// PostAndFinalize posts tx and immediately settles and finalizes it.
func (s *Service) PostAndFinalize(ctx context.Context, tx Transaction) (Transaction, error) {
	posted, err := s.Post(ctx, tx)
	if err != nil {
		return Transaction{}, err
	}
	settled, err := s.Settle(ctx, posted.ID)
	if err != nil {
		return Transaction{}, err
	}
	return s.Finalize(ctx, settled.ID)
}

// Settle moves a transaction from pending to settled.
func (s *Service) Settle(ctx context.Context, txID string) (Transaction, error) {
	return s.transition(ctx, txID, StatusPending, StatusSettled, eventTxSettled)
}

// Finalize moves a transaction from settled to final.
func (s *Service) Finalize(ctx context.Context, txID string) (Transaction, error) {
	return s.transition(ctx, txID, StatusSettled, StatusFinal, eventTxFinalized)
}

func (s *Service) transition(ctx context.Context, txID string, from, to Status, eventType string) (Transaction, error) {
	if err := s.refresh(ctx); err != nil {
		return Transaction{}, err
	}
	s.proj.mu.Lock()
	tx, ok := s.proj.txs[txID]
	s.proj.mu.Unlock()
	if !ok {
		return Transaction{}, store.ErrNotFound
	}
	if tx.Status != from {
		return Transaction{}, fmt.Errorf("ledger: cannot move transaction %q from %q to %q", txID, tx.Status, to)
	}
	tx.Status = to
	tx.OccurredAt = s.clock.Now()
	if err := s.appendTxEvent(ctx, eventType, tx); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}

func (s *Service) appendTxEvent(ctx context.Context, eventType string, tx Transaction) error {
	payload, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	events, err := s.store.Append(ctx, store.Event{
		Type:        eventType,
		AggregateID: tx.ID,
		OccurredAt:  tx.OccurredAt,
		Payload:     payload,
	})
	if err != nil {
		return err
	}
	s.applyAppended(events)
	return nil
}

// GetTransaction returns one transaction by ID.
func (s *Service) GetTransaction(ctx context.Context, id string) (Transaction, error) {
	if err := s.refresh(ctx); err != nil {
		return Transaction{}, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	tx, ok := s.proj.txs[id]
	if !ok {
		return Transaction{}, store.ErrNotFound
	}
	return tx, nil
}

// ListTransactions returns transactions matching f, ordered by OccurredAt
// then ID.
func (s *Service) ListTransactions(ctx context.Context, f TxFilter) ([]Transaction, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	var out []Transaction
	for _, tx := range s.proj.txs {
		if f.AccountID != "" && !txHasAccount(tx, f.AccountID) {
			continue
		}
		if f.Kind != "" && tx.Kind != f.Kind {
			continue
		}
		if f.Status != "" && tx.Status != f.Status {
			continue
		}
		if !f.From.IsZero() && tx.OccurredAt.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && tx.OccurredAt.After(f.To) {
			continue
		}
		out = append(out, tx)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].OccurredAt.Before(out[j].OccurredAt)
	})
	return out, nil
}

func txHasAccount(tx Transaction, accountID string) bool {
	for _, e := range tx.Entries {
		if e.AccountID == accountID {
			return true
		}
	}
	return false
}

// --- balances -------------------------------------------------------

// Balance returns accountID's natural-sign balance: positive when in the
// account type's normal direction. Includes pending transactions.
func (s *Service) Balance(ctx context.Context, accountID string) (Amount, error) {
	if err := s.refresh(ctx); err != nil {
		return Amount{}, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	acc, ok := s.proj.accounts[accountID]
	if !ok {
		return Amount{}, store.ErrNotFound
	}
	return Amount{Currency: acc.Currency, Minor: s.naturalBalanceLocked(acc)}, nil
}

// naturalBalanceLocked requires the caller to hold s.proj.mu.
func (s *Service) naturalBalanceLocked(acc Account) int64 {
	d := s.proj.debit[acc.ID]
	c := s.proj.credit[acc.ID]
	if NormalBalance(acc.Type) == Debit {
		return d - c
	}
	return c - d
}

// Balances returns every account's natural-sign balance, derived by
// replaying events.
func (s *Service) Balances(ctx context.Context) (map[string]Amount, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	out := make(map[string]Amount, len(s.proj.accounts))
	for id, acc := range s.proj.accounts {
		out[id] = Amount{Currency: acc.Currency, Minor: s.naturalBalanceLocked(acc)}
	}
	return out, nil
}

// BalancesByType aggregates natural-sign balances by account type and
// currency.
func (s *Service) BalancesByType(ctx context.Context) (map[AccountType]map[string]int64, error) {
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	out := map[AccountType]map[string]int64{}
	for _, acc := range s.proj.accounts {
		bal := s.naturalBalanceLocked(acc)
		byCur := out[acc.Type]
		if byCur == nil {
			byCur = map[string]int64{}
			out[acc.Type] = byCur
		}
		byCur[acc.Currency] += bal
	}
	return out, nil
}
