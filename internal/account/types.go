// Package account implements the D4 deliverable: a transaction account
// that spends across a customer's fiat (USD) and stablecoin (USDC)
// balances according to the hot-reloadable spend_waterfall rule.
package account

import (
	"time"

	"github.com/Raven020/stableBank/internal/ledger"
)

// PurchaseRequest is the input to Purchase.
type PurchaseRequest struct {
	AccountID   string `json:"account_id"`
	AmountCents int64  `json:"amount_cents"`
	Merchant    string `json:"merchant,omitempty"`
}

// BulkRequest is the input to BulkRandomPurchases.
type BulkRequest struct {
	AccountID string `json:"account_id"`
	Count     int    `json:"count"`
	MinCents  int64  `json:"min_cents"`
	MaxCents  int64  `json:"max_cents"`
	Seed      int64  `json:"seed,omitempty"`
}

// Funding describes one currency leg that funded a purchase.
type Funding struct {
	Currency           string `json:"currency"`
	AmountMinor        int64  `json:"amount_minor"`
	AmountDisplay      string `json:"amount_display"`
	UsdEquivalentCents int64  `json:"usd_equivalent_cents"`
	SharePct           int    `json:"share_pct"`
}

// WaterfallInfo records which version of the spend_waterfall rule decided a
// purchase, for audit purposes.
type WaterfallInfo struct {
	RuleID        string `json:"rule_id"`
	Version       int    `json:"version"`
	MatchedRule   string `json:"matched_rule"`
	ApplicationID string `json:"application_id"`
}

// Receipt is the outcome of a purchase attempt, approved or declined.
type Receipt struct {
	PurchaseID           string        `json:"purchase_id"`
	AccountID            string        `json:"account_id"`
	Status               string        `json:"status"` // approved | declined
	AmountCents          int64         `json:"amount_cents"`
	Merchant             string        `json:"merchant"`
	FundedBy             []Funding     `json:"funded_by"`
	Waterfall            WaterfallInfo `json:"waterfall"`
	LedgerTransactionIDs []string      `json:"ledger_transaction_ids"`
	DeclineReason        string        `json:"decline_reason,omitempty"`
	OccurredAt           time.Time     `json:"occurred_at"`
}

// View is the read model returned by Get/ListAccounts.
type View struct {
	ID                 string                   `json:"id"`
	OwnerID            string                   `json:"owner_id"`
	Balances           map[string]ledger.Amount `json:"balances"`
	LedgerAccountIDs   map[string]string        `json:"ledger_account_ids"`
	UsdEquivalentCents int64                    `json:"usd_equivalent_cents"`
}

// ValidationError marks a request-shaped error (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "account: " + e.Msg }
