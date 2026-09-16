package ledger

import "time"

// Direction is which side of an entry money moves to.
type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// AccountType classifies a ledger account and determines its NormalBalance.
type AccountType string

const (
	CustomerDeposit    AccountType = "customer_deposit"    // liability, credit-normal
	LoanReceivable     AccountType = "loan_receivable"     // asset,  debit-normal
	TreasuryCash       AccountType = "treasury_cash"       // asset,  debit-normal
	TreasuryFX         AccountType = "treasury_fx"         // asset,  debit-normal (FX exposure visible here)
	MerchantSettlement AccountType = "merchant_settlement" // liability, credit-normal
	InterestIncome     AccountType = "interest_income"     // income, credit-normal
	FeeIncome          AccountType = "fee_income"          // income, credit-normal
	LoanLossExpense    AccountType = "loan_loss_expense"   // expense, debit-normal
	EquityCapital      AccountType = "equity_capital"      // equity, credit-normal
)

// NormalBalance returns the direction in which balances of accounts of type
// t naturally grow.
func NormalBalance(t AccountType) Direction {
	switch t {
	case LoanReceivable, TreasuryCash, TreasuryFX, LoanLossExpense:
		return Debit
	default:
		return Credit
	}
}

// Account is one ledger account.
type Account struct {
	ID       string      `json:"id"`
	OwnerID  string      `json:"owner_id"`
	Type     AccountType `json:"type"`
	Currency string      `json:"currency"`
	Name     string      `json:"name"`
	OpenedAt time.Time   `json:"opened_at"`
}
