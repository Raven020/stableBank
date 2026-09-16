package ledger

import "time"

// Entry is one leg of a double-entry transaction.
type Entry struct {
	AccountID string    `json:"account_id"`
	Direction Direction `json:"direction"`
	Amount    Amount    `json:"amount"`
}

// Status is a transaction's position in the pending -> settled -> final
// state machine.
type Status string

const (
	StatusPending Status = "pending"
	StatusSettled Status = "settled"
	StatusFinal   Status = "final"
)

// Transaction is an event-sourced double-entry transaction. Entries never
// change after Post; only Status advances.
type Transaction struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Description string            `json:"description"`
	Currency    string            `json:"currency"`
	Entries     []Entry           `json:"entries"`
	Status      Status            `json:"status"`
	LinkedTxID  string            `json:"linked_tx_id,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	OccurredAt  time.Time         `json:"occurred_at"`
}

// TxFilter narrows ListTransactions. Zero values mean "no constraint".
type TxFilter struct {
	AccountID string
	Kind      string
	From, To  time.Time
	Status    Status
}
