package dashboard

import "time"

// disclaimer is attached to every illustrative ratio the dashboard reports.
const disclaimer = "Illustrative / PoC — not a certified regulatory figure"

const (
	modeledOnLiquidity = "APRA APS 210 (Liquidity)"
	modeledOnCapital   = "APRA APS 110 (Capital Adequacy)"
	modeledOnInternal  = "internal ratio"
)

// AccountBalance is one ledger account's natural-sign balance, as shown in
// the ledger book.
type AccountBalance struct {
	AccountID string `json:"account_id"`
	Type      string `json:"type"`
	Currency  string `json:"currency"`
	Minor     int64  `json:"minor"`
	Display   string `json:"display"`
}

// TreasuryFXExposure is one currency's treasury_fx exposure, converted to
// its USD equivalent so multi-currency FX exposure is visible at a glance.
type TreasuryFXExposure struct {
	Currency           string `json:"currency"`
	Minor              int64  `json:"minor"`
	UsdEquivalentCents int64  `json:"usd_equivalent_cents"`
}

// LedgerTotals summarises the ledger book across every account.
type LedgerTotals struct {
	// TotalFiatCents is USD across customer_deposit + treasury (treasury_cash
	// and treasury_fx) accounts.
	TotalFiatCents int64 `json:"total_fiat_cents"`
	// TotalUsdcMicro is USDC (in USDC's 6-decimal minor unit) across
	// customer_deposit + treasury accounts.
	TotalUsdcMicro int64 `json:"total_usdc_micro"`
	// TotalUsdcUsdEquivalentCents is TotalUsdcMicro converted to USD cents
	// via ledger.Quote.
	TotalUsdcUsdEquivalentCents int64 `json:"total_usdc_usd_equivalent_cents"`
}

// LedgerBook is the full ledger snapshot: every account's balance, grouped
// by type and currency, plus bank-wide totals and FX exposure.
type LedgerBook struct {
	AsOf       time.Time                   `json:"as_of"`
	Accounts   []AccountBalance            `json:"accounts"`
	ByType     map[string]map[string]int64 `json:"by_type"` // account_type -> currency -> minor
	Totals     LedgerTotals                `json:"totals"`
	TreasuryFX []TreasuryFXExposure        `json:"treasury_fx"`
}

// FlowBreakdown is one transaction Kind's contribution to a Flows window.
type FlowBreakdown struct {
	Kind               string `json:"kind"`
	UsdEquivalentCents int64  `json:"usd_equivalent_cents"`
	Count              int    `json:"count"`
}

// DailyFlow is one calendar day's inflow/outflow totals, for charting.
type DailyFlow struct {
	Date          string `json:"date"` // "2026-09-01"
	InflowsCents  int64  `json:"inflows_cents"`
	OutflowsCents int64  `json:"outflows_cents"`
}

// Flows is the money-in/money-out summary for [From, To).
type Flows struct {
	From          time.Time       `json:"from"`
	To            time.Time       `json:"to"`
	InflowsCents  int64           `json:"inflows_cents"`
	OutflowsCents int64           `json:"outflows_cents"`
	NetCents      int64           `json:"net_cents"`
	ByKind        []FlowBreakdown `json:"by_kind"`
	Daily         []DailyFlow     `json:"daily"`
}

// Metric is one illustrative ratio: its value, the raw amounts behind it,
// the threshold status, and the regulatory framework it is loosely modeled
// on. Every Metric always carries the PoC disclaimer.
type Metric struct {
	ValuePct          float64        `json:"value_pct"`
	NumeratorCents    int64          `json:"numerator_cents"`
	DenominatorCents  int64          `json:"denominator_cents"`
	Status            string         `json:"status"`
	TargetPct         float64        `json:"target_pct"`
	Disclaimer        string         `json:"disclaimer"`
	ModeledOn         string         `json:"modeled_on"`
	Components        map[string]any `json:"components,omitempty"`
	RuleApplicationID string         `json:"rule_application_id,omitempty"`
}

// Ratios bundles the four illustrative ratios the dashboard shows, plus the
// dashboard_thresholds rule version that decided their statuses.
type Ratios struct {
	AsOf             time.Time `json:"as_of"`
	LoanToDeposit    Metric    `json:"loan_to_deposit"`
	LCR              Metric    `json:"lcr"`
	NSFR             Metric    `json:"nsfr"`
	CapitalAdequacy  Metric    `json:"capital_adequacy"`
	ThresholdRuleID  string    `json:"threshold_rule_id"`
	ThresholdVersion int       `json:"threshold_version"`
}

// Summary is the all-in-one dashboard payload.
type Summary struct {
	AsOf       time.Time  `json:"as_of"`
	LedgerBook LedgerBook `json:"ledger_book"`
	Flows      Flows      `json:"flows"`
	Ratios     Ratios     `json:"ratios"`
}
