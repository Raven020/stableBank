package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	es := memstore.New()
	clock := simclock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return New(es, clock, nil)
}

func TestEnsureSystemAccountsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	first, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(first) != len(systemAccounts) {
		t.Fatalf("got %d accounts, want %d", len(first), len(systemAccounts))
	}

	// Calling again must not error or duplicate accounts.
	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts (second call): %v", err)
	}
	second, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(second) != len(systemAccounts) {
		t.Fatalf("got %d accounts after second call, want %d", len(second), len(systemAccounts))
	}
}

func TestSeedCapitalIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if err := s.SeedCapital(ctx, 1_000_000_00); err != nil {
		t.Fatalf("SeedCapital: %v", err)
	}
	if err := s.SeedCapital(ctx, 1_000_000_00); err != nil {
		t.Fatalf("SeedCapital (second call): %v", err)
	}

	cashBal, err := s.Balance(ctx, "treasury-cash-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if cashBal.Minor != 1_000_000_00 {
		t.Fatalf("treasury-cash-usd balance = %d, want %d (seed should not double-post)", cashBal.Minor, 1_000_000_00)
	}
	equityBal, err := s.Balance(ctx, "equity-capital-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if equityBal.Minor != 1_000_000_00 {
		t.Fatalf("equity-capital-usd balance = %d, want %d", equityBal.Minor, 1_000_000_00)
	}

	txs, err := s.ListTransactions(ctx, TxFilter{Kind: "capital.seed"})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("got %d capital.seed transactions, want 1", len(txs))
	}
	if txs[0].Status != StatusFinal {
		t.Fatalf("capital.seed status = %q, want final", txs[0].Status)
	}
}

func TestPostValidationFailures(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if _, err := s.OpenAccount(ctx, Account{ID: "cust-usd", OwnerID: "u1", Type: CustomerDeposit, Currency: "USD"}); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if _, err := s.OpenAccount(ctx, Account{ID: "cust-usdc", OwnerID: "u1", Type: CustomerDeposit, Currency: "USDC"}); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	tests := []struct {
		name string
		tx   Transaction
	}{
		{
			name: "single entry",
			tx: Transaction{
				Currency: "USD",
				Entries: []Entry{
					{AccountID: "cust-usd", Direction: Debit, Amount: Amount{"USD", 100}},
				},
			},
		},
		{
			name: "mismatched entry currency",
			tx: Transaction{
				Currency: "USD",
				Entries: []Entry{
					{AccountID: "cust-usd", Direction: Debit, Amount: Amount{"USD", 100}},
					{AccountID: "cust-usdc", Direction: Credit, Amount: Amount{"USDC", 100}},
				},
			},
		},
		{
			name: "account currency mismatch",
			tx: Transaction{
				Currency: "USD",
				Entries: []Entry{
					{AccountID: "cust-usdc", Direction: Debit, Amount: Amount{"USD", 100}},
					{AccountID: "treasury-cash-usd", Direction: Credit, Amount: Amount{"USD", 100}},
				},
			},
		},
		{
			name: "unbalanced",
			tx: Transaction{
				Currency: "USD",
				Entries: []Entry{
					{AccountID: "cust-usd", Direction: Debit, Amount: Amount{"USD", 100}},
					{AccountID: "treasury-cash-usd", Direction: Credit, Amount: Amount{"USD", 99}},
				},
			},
		},
		{
			name: "zero amount",
			tx: Transaction{
				Currency: "USD",
				Entries: []Entry{
					{AccountID: "cust-usd", Direction: Debit, Amount: Amount{"USD", 0}},
					{AccountID: "treasury-cash-usd", Direction: Credit, Amount: Amount{"USD", 0}},
				},
			},
		},
		{
			name: "unknown account",
			tx: Transaction{
				Currency: "USD",
				Entries: []Entry{
					{AccountID: "does-not-exist", Direction: Debit, Amount: Amount{"USD", 100}},
					{AccountID: "treasury-cash-usd", Direction: Credit, Amount: Amount{"USD", 100}},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Post(ctx, tc.tx); err == nil {
				t.Fatalf("expected validation error, got none")
			}
		})
	}
}

func TestBalanceProjectionDepositAndPurchase(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if _, err := s.OpenAccount(ctx, Account{ID: "cust-usd", OwnerID: "u1", Type: CustomerDeposit, Currency: "USD"}); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if err := s.SeedCapital(ctx, 10_000_00); err != nil {
		t.Fatalf("SeedCapital: %v", err)
	}

	// Deposit: debit treasury-cash-usd / credit cust-usd.
	depositTx, err := s.PostAndFinalize(ctx, Transaction{
		Kind:     "deposit",
		Currency: "USD",
		Entries: []Entry{
			{AccountID: "treasury-cash-usd", Direction: Debit, Amount: Amount{"USD", 150000}},
			{AccountID: "cust-usd", Direction: Credit, Amount: Amount{"USD", 150000}},
		},
	})
	if err != nil {
		t.Fatalf("deposit Post: %v", err)
	}
	if depositTx.Status != StatusFinal {
		t.Fatalf("deposit status = %q, want final", depositTx.Status)
	}

	bal, err := s.Balance(ctx, "cust-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Minor != 150000 {
		t.Fatalf("cust-usd balance after deposit = %d, want 150000", bal.Minor)
	}

	// Purchase: debit cust-usd / credit merchant-settlement-usd.
	_, err = s.PostAndFinalize(ctx, Transaction{
		Kind:     "purchase",
		Currency: "USD",
		Metadata: map[string]string{"purchase_id": "p1"},
		Entries: []Entry{
			{AccountID: "cust-usd", Direction: Debit, Amount: Amount{"USD", 2500}},
			{AccountID: "merchant-settlement-usd", Direction: Credit, Amount: Amount{"USD", 2500}},
		},
	})
	if err != nil {
		t.Fatalf("purchase Post: %v", err)
	}

	bal, err = s.Balance(ctx, "cust-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Minor != 150000-2500 {
		t.Fatalf("cust-usd balance after purchase = %d, want %d", bal.Minor, 150000-2500)
	}

	merchantBal, err := s.Balance(ctx, "merchant-settlement-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if merchantBal.Minor != 2500 {
		t.Fatalf("merchant-settlement-usd balance = %d, want 2500", merchantBal.Minor)
	}

	// Cross-check against Balances()/BalancesByType().
	all, err := s.Balances(ctx)
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if all["cust-usd"].Minor != 150000-2500 {
		t.Fatalf("Balances()[cust-usd] = %d, want %d", all["cust-usd"].Minor, 150000-2500)
	}

	byType, err := s.BalancesByType(ctx)
	if err != nil {
		t.Fatalf("BalancesByType: %v", err)
	}
	if got := byType[CustomerDeposit]["USD"]; got != 150000-2500 {
		t.Fatalf("BalancesByType[CustomerDeposit][USD] = %d, want %d", got, 150000-2500)
	}
}

func TestSettlementStateMachine(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if err := s.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}
	if _, err := s.OpenAccount(ctx, Account{ID: "cust-usd", OwnerID: "u1", Type: CustomerDeposit, Currency: "USD"}); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	tx, err := s.Post(ctx, Transaction{
		Kind:     "deposit",
		Currency: "USD",
		Entries: []Entry{
			{AccountID: "treasury-cash-usd", Direction: Debit, Amount: Amount{"USD", 100}},
			{AccountID: "cust-usd", Direction: Credit, Amount: Amount{"USD", 100}},
		},
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if tx.Status != StatusPending {
		t.Fatalf("new tx status = %q, want pending", tx.Status)
	}

	// Finalize before settle must fail.
	if _, err := s.Finalize(ctx, tx.ID); err == nil {
		t.Fatal("expected error finalizing a pending transaction")
	}

	settled, err := s.Settle(ctx, tx.ID)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settled.Status != StatusSettled {
		t.Fatalf("status after Settle = %q, want settled", settled.Status)
	}

	// Settling again must fail (already settled, not pending).
	if _, err := s.Settle(ctx, tx.ID); err == nil {
		t.Fatal("expected error re-settling an already-settled transaction")
	}

	final, err := s.Finalize(ctx, tx.ID)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if final.Status != StatusFinal {
		t.Fatalf("status after Finalize = %q, want final", final.Status)
	}

	// Finalizing again must fail.
	if _, err := s.Finalize(ctx, tx.ID); err == nil {
		t.Fatal("expected error re-finalizing an already-final transaction")
	}

	// Unknown transaction ID.
	if _, err := s.Settle(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected error settling unknown transaction")
	}
}

func TestGetAccountAndTransactionNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if _, err := s.GetAccount(ctx, "nope"); err == nil {
		t.Fatal("expected error for unknown account")
	}
	if _, err := s.GetTransaction(ctx, "nope"); err == nil {
		t.Fatal("expected error for unknown transaction")
	}
}
