# stableBank — Cross-Package Contracts

This file is the single source of truth for how the packages fit together.
Every package is developed independently against these contracts. **If you
need to change a contract, change this file first and say so in your report.**

Module path: `github.com/Raven020/stableBank`. Go 1.25 (`net/http` pattern
routing: `mux.HandleFunc("GET /rules/{id}", ...)`, `r.PathValue("id")`).

Dependencies already in go.mod (do **not** add others, do **not** run
`go mod tidy`): `github.com/jackc/pgx/v5`, `gopkg.in/yaml.v3`,
`github.com/santhosh-tekuri/jsonschema/v6`.

## Layout

```
cmd/stablebank/main.go        wiring only (owned by the integrator, do not edit)
internal/simclock             DONE — shared simulated clock
internal/store                DONE — EventStore / RuleStore interfaces
internal/store/memstore       DONE — in-memory implementation (tests default to this)
internal/store/pgstore        Postgres implementation            [D3]
internal/httpx                DONE — JSON helpers
internal/portfolio            DONE — LoanExposure shared type
internal/ledger               event-sourced double-entry ledger  [D1]
internal/rules                rules engine + editor backend      [D2]
internal/account              transaction account + waterfall    [D4]
internal/loan                 loans, risk score, forecasts       [D5]
internal/dashboard            ledger book + illustrative ratios  [D6]
internal/simulate             /simulate/* control endpoints      [D7]
internal/demo                 /demo/* scripted scenarios         [D8]
web/                          embedded SPA (vanilla JS)          [D9]
rules/                        YAML rule files + JSON schemas + registry.yaml
demo/demo-scenarios.yaml
migrations/                   Postgres SQL migrations
```

## Global conventions

* **Money**: `int64` minor units. USD has 2 decimals, USDC has 6 (the ERC-20
  USDC contract uses 6). No `float64` anywhere near money; use `math/big`
  for intermediate arithmetic. Basis points (`_bps`) are `int64`, percent
  values that need fractions (e.g. `10.5` CAR minimum) are stored as
  `_pct` numbers in YAML and handled as `big.Rat` in Go.
* **Time**: always `clock.Now()` from an injected `simclock.Clock`. Never
  `time.Now()` in domain logic (the store may use wall time for
  `RecordedAt`).
* **IDs**: strings. Use `memstore.NewID()` or a package-local equivalent.
  Well-known seeded IDs are listed below.
* **Errors over HTTP**: `httpx.WriteError(w, status, msg, details)`.
  400 for validation, 404 not found, 409 conflict/decline, 422 for rule
  validation failures, 500 otherwise.
* **Every package exposes** `func (s *Service) RegisterRoutes(mux *http.ServeMux)`.
* **Every service constructor** takes its dependencies explicitly (no
  globals).
* **Tests** use `memstore.New()` and `simclock.New(...)`. Run
  `gofmt -l . && go vet ./... && go test ./...` before reporting done.
* **Do not** touch `cmd/stablebank/main.go`, `go.mod`, `go.sum`, or another
  deliverable's directory. Do not run any `git` command; the integrator commits.

## Ledger (`internal/ledger`) — D1

```go
type Currency struct{ Code string; Decimals int }
var Currencies = map[string]Currency{"USD": {"USD", 2}, "USDC": {"USDC", 6}}

type Amount struct{ Currency string `json:"currency"`; Minor int64 `json:"minor"` }
func (a Amount) Display() string            // "12.34 USD", "800.000000 USDC"
func ParseAmount(code, decimal string) (Amount, error)

type Direction string // "debit" | "credit"
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
func NormalBalance(t AccountType) Direction

type Account struct {
    ID string; OwnerID string; Type AccountType; Currency string; Name string; OpenedAt time.Time
}
type Entry struct{ AccountID string; Direction Direction; Amount Amount }
type Status string // "pending" | "settled" | "final"
type Transaction struct {
    ID string; Kind string; Description string; Currency string
    Entries []Entry; Status Status; LinkedTxID string
    Metadata map[string]string; OccurredAt time.Time
}
```

Rules enforced by `Post`: ≥2 entries, all entries same currency as the tx,
every account exists and has that currency, sum(debits) == sum(credits) > 0.
New transactions start `pending`. `Settle` and `Finalize` move
`pending→settled→final` (any other transition is an error). Callers that
want an instantly-final tx call `PostAndFinalize`.

```go
type Service struct{ /* store.EventStore, simclock.Clock, RateProvider */ }
func New(es store.EventStore, clock simclock.Clock, rates RateProvider) *Service
func (s *Service) EnsureSystemAccounts(ctx) error   // idempotent, opens the accounts listed below
func (s *Service) SeedCapital(ctx, usdCents int64) error // debit treasury-cash-usd / credit equity-capital-usd (idempotent by Kind "capital.seed")
func (s *Service) OpenAccount(ctx, a Account) (Account, error)
func (s *Service) GetAccount(ctx, id string) (Account, error)
func (s *Service) ListAccounts(ctx) ([]Account, error)
func (s *Service) Post(ctx, tx Transaction) (Transaction, error)
func (s *Service) PostAndFinalize(ctx, tx Transaction) (Transaction, error)
func (s *Service) Settle(ctx, txID string) (Transaction, error)
func (s *Service) Finalize(ctx, txID string) (Transaction, error)
func (s *Service) GetTransaction(ctx, id string) (Transaction, error)
func (s *Service) ListTransactions(ctx, f TxFilter) ([]Transaction, error) // TxFilter{AccountID, Kind, From, To time.Time, Status}
func (s *Service) Balance(ctx, accountID string) (Amount, error)   // natural-sign balance (positive when in normal direction), includes pending
func (s *Service) Balances(ctx) (map[string]Amount, error)          // all accounts, derived by replaying events
func (s *Service) BalancesByType(ctx) (map[AccountType]map[string]int64, error) // type -> currency -> minor
func (s *Service) Convert(ctx, req FXRequest) (FXResult, error)     // two linked txs through treasury_fx
func (s *Service) Quote(from, to string, fromMinor int64) (FXQuote, error) // pure, no ledger writes

type RateProvider interface{ Rate(from, to string) (Rate, error) }
type Rate struct{ From, To string; Scaled int64 /* to-per-from × 1e8 */; Source string; AsOf time.Time }
type FXRequest struct{ FromAccountID, ToAccountID string; From Amount; Kind, Description string; Metadata map[string]string }
type FXQuote  struct{ From Amount; To Amount; Rate Rate; RoundingMode string }
type FXResult struct{ Quote FXQuote; FromTx, ToTx Transaction }
```

`Convert` posts: tx1 (From currency): debit FromAccountID / credit
`treasury-fx-<from>`; tx2 (To currency): debit `treasury-fx-<to>` / credit
ToAccountID. Each carries the other's ID in `LinkedTxID` and both share
`Metadata["fx_rate"]`, `Metadata["fx_source"]`. Both are finalized. The
RateProvider is `rules`-driven: the ledger package exports
`type RuleRateProvider` that reads rule `fx_policy` through a minimal
interface `RuleReader interface{ Get(ruleID string) (map[string]any, int, error) }`
(returns content + live version) so the ledger does not import `rules`.
The ledger package also exports `FXEvaluator` implementing
`rules.Evaluator` semantics as a plain function
`func EvaluateFXPolicy(content, input map[string]any) (map[string]any, error)`
so the rules engine can register it without an import cycle.

Well-known system accounts opened by `EnsureSystemAccounts`:

| ID | Type | Currency |
|---|---|---|
| `treasury-cash-usd` / `treasury-cash-usdc` | treasury_cash | USD / USDC |
| `treasury-fx-usd` / `treasury-fx-usdc` | treasury_fx | USD / USDC |
| `merchant-settlement-usd` / `merchant-settlement-usdc` | merchant_settlement | USD / USDC |
| `interest-income-usd` | interest_income | USD |
| `fee-income-usd` | fee_income | USD |
| `loan-loss-expense-usd` | loan_loss_expense | USD |
| `equity-capital-usd` | equity_capital | USD |

Event types: `ledger.account.opened`, `ledger.tx.posted`,
`ledger.tx.settled`, `ledger.tx.finalized`. Payload = JSON of the struct.

HTTP: `GET /ledger/accounts`, `GET /ledger/accounts/{id}`,
`GET /ledger/accounts/{id}/balance`, `GET /ledger/transactions?account_id=&kind=`,
`GET /ledger/transactions/{id}`, `POST /ledger/transactions/{id}/settle`,
`POST /ledger/transactions/{id}/finalize`, `GET /ledger/balances`,
`GET /ledger/fx/quote?from=USD&to=USDC&amount_minor=10000`, `GET /ledger/currencies`.

Transaction `Kind` vocabulary (used by dashboard flows):
`deposit`, `withdrawal`, `purchase`, `fx`, `capital.seed`, `loan.disbursement`,
`loan.repayment`, `loan.late_fee`, `loan.writeoff`.

## Rules engine (`internal/rules`) — D2

```go
type Meta struct {
    RuleID string `yaml:"rule_id"`; RuleType string `yaml:"rule_type"`; Version int `yaml:"version"`
    Domain, Description, Rationale, Owner, LastReviewed string
}
type Example struct{ Name string; Input map[string]any; Expected map[string]any }
type Rule struct {
    Meta; Content map[string]any /* whole YAML doc */; Raw []byte
    Examples []Example; EffectiveFrom time.Time
}
type Evaluator func(content map[string]any, input map[string]any) (map[string]any, error)

type Engine struct{ /* store.RuleStore, simclock.Clock, registry, cache, evaluators */ }
func NewEngine(st store.RuleStore, clock simclock.Clock, rulesDir string) *Engine
func (e *Engine) LoadFromDisk(ctx) error                 // registry.yaml → validate each file → insert v1 if store has none → warm cache
func (e *Engine) RegisterEvaluator(ruleType string, ev Evaluator)
func (e *Engine) Get(ruleID string) (*Rule, error)      // live version from in-memory cache (no I/O)
func (e *Engine) GetContent(ruleID string) (map[string]any, int, error) // satisfies ledger.RuleReader
func (e *Engine) List() []Summary                        // registry entries + live version + last_reviewed
func (e *Engine) Versions(ctx, ruleID) ([]store.RuleVersion, error)
func (e *Engine) Evaluate(ctx, ruleID, entityType, entityID string, input map[string]any) (Decision, error)
    // runs the registered evaluator for the rule's type against the live version,
    // then writes a store.RuleApplication. Returns the Decision.
type Decision struct{ RuleID string; Version int; Input, Output map[string]any; AppliedAt time.Time; ApplicationID string }
func (e *Engine) Validate(raw []byte) (*Rule, error)     // yaml parse + common schema + per-type schema; error lists all problems
func (e *Engine) RunExamples(rule *Rule) []ExampleResult // uses registered evaluator; ExampleResult{Name, Passed, Expected, Actual, Error}
func (e *Engine) Save(ctx, ruleID string, raw []byte, opts SaveOptions) (SaveResult, error)
    // SaveOptions{Author, ChangeNote string; AllowFailingExamples bool}
    // 1. ruleID must be in registry; 2. Validate; 3. raw rule_id must equal ruleID;
    // 4. RunExamples — if any fail and !AllowFailingExamples → return ErrExamplesFailed with results;
    // 5. insert version = latest+1 (ignore the version field in YAML; rewrite it), effective_from = clock.Now();
    // 6. replace cache entry synchronously (hot reload). SaveResult{Version, Previous int, Examples []ExampleResult, Diff string}
func (e *Engine) Reactivate(ctx, ruleID string, version int, note string) (SaveResult, error) // new version whose Content == old version's Content
func (e *Engine) RegisterRoutes(mux *http.ServeMux)
```

Example comparison: JSON-normalise both sides; `expected` is matched as a
**subset** of actual output (only keys present in expected are compared;
nested maps recurse; numbers compare as float64 after JSON round-trip).

HTTP:
* `GET /rules` → `{ "rules": [Summary] }`
* `GET /rules/{id}` → `{ meta, version, effective_from, content, raw_yaml, examples }`
* `GET /rules/{id}/versions`, `GET /rules/{id}/versions/{v}`
* `GET /rules/{id}/applications?limit=` and `GET /rules/{id}/applications/{entity_id}`
* `GET /rules/applications?entity_id=` (all rules for one entity)
* `PUT /rules/{id}` body `{ "yaml": "...", "change_note": "...", "allow_failing_examples": false }`
  → 200 SaveResult, 422 `{error:"validation_failed", details:[...]}`,
  409 `{error:"examples_failed", details:{examples:[...]}}`
* `POST /rules/{id}/validate` same body → dry run: `{ valid, problems, examples, diff }`
* `POST /rules/{id}/reactivate` body `{ "version": 1, "change_note": "" }`

Rule files live at the paths in `rules/registry.yaml`:

```yaml
rules:
  - rule_id: loan_underwriting     rule_type: loan_underwriting     path: rules/loan/loan_underwriting.yaml       domain: loan       owner: credit-risk@stablebank.demo
  - rule_id: risk_scoring          rule_type: risk_scoring          path: rules/loan/risk_scoring.yaml            domain: loan       owner: credit-risk@stablebank.demo
  - rule_id: loan_servicing        rule_type: loan_servicing        path: rules/loan/loan_servicing.yaml          domain: loan       owner: collections@stablebank.demo
  - rule_id: fx_policy             rule_type: fx_policy             path: rules/ledger/fx_policy.yaml             domain: ledger     owner: treasury@stablebank.demo
  - rule_id: depeg_policy          rule_type: depeg_policy          path: rules/ledger/depeg_policy.yaml          domain: ledger     owner: treasury@stablebank.demo
  - rule_id: spend_waterfall       rule_type: spend_waterfall       path: rules/account/spend_waterfall.yaml      domain: account    owner: payments@stablebank.demo
  - rule_id: dashboard_thresholds  rule_type: dashboard_thresholds  path: rules/dashboard/dashboard_thresholds.yaml domain: dashboard owner: finance@stablebank.demo
  - rule_id: risk_weights          rule_type: risk_weights          path: rules/dashboard/risk_weights.yaml       domain: dashboard  owner: finance@stablebank.demo
  - rule_id: liquidity_stress      rule_type: liquidity_stress      path: rules/dashboard/liquidity_stress.yaml   domain: dashboard  owner: treasury@stablebank.demo
```

(Write it as proper multi-line YAML; the one-line form above is for brevity.)

Every rule file has these top-level keys: `rule_id`, `rule_type`, `version`,
`domain`, `description`, `rationale`, `owner`, `last_reviewed` (ISO date),
the type-specific keys below, and `examples: [{name, input, expected}]`
(≥2 examples each). JSON schemas: `rules/schemas/common.schema.json` plus
`rules/schemas/<rule_type>.schema.json` (`additionalProperties: false` on the
type-specific objects so typos are caught).

### Type-specific YAML shapes (field paths are load-bearing: the demo script edits them)

**loan_underwriting**
```yaml
eligibility:
  min_credit_score: 620
  min_monthly_income_cents: 250000
  max_debt_to_income_pct: 45          # (existing_debt_monthly + new_installment) / income
  min_account_age_days: 90
  min_risk_score: 40
pricing_tiers:                        # matched by risk_score descending; first match wins
  - { tier: A, min_risk_score: 80, annual_rate_bps: 699,  max_principal_cents: 5000000 }
  - { tier: B, min_risk_score: 60, annual_rate_bps: 999,  max_principal_cents: 3000000 }
  - { tier: C, min_risk_score: 40, annual_rate_bps: 1499, max_principal_cents: 1500000 }
allowed_term_months: [12, 24, 36, 48, 60]
# example input:  { credit_score, monthly_income_cents, existing_debt_monthly_cents, account_age_days,
#                   risk_score, requested_principal_cents, term_months, proposed_installment_cents }
# example output: { approved: bool, tier: "B", annual_rate_bps: 999, reasons: ["credit_score_below_minimum", ...] }
```
Evaluator lives in `internal/loan` (`loan.EvaluateUnderwriting`).

**risk_scoring**
```yaml
base_score: 50
factors:
  - name: credit_score
    input: credit_score
    bands: [ { max: 579, points: -25 }, { min: 580, max: 669, points: 0 }, { min: 670, max: 739, points: 15 }, { min: 740, points: 30 } ]
  - name: income        input: monthly_income_cents    bands: [...]
  - name: account_age   input: account_age_days        bands: [...]
  - name: existing_balance input: existing_balance_cents bands: [...]
clamp: { min: 0, max: 100 }
tiers:                                # by final score, descending
  - { tier: A, min_score: 80, pd_bps: 150 }
  - { tier: B, min_score: 60, pd_bps: 400 }
  - { tier: C, min_score: 40, pd_bps: 900 }
  - { tier: D, min_score: 0,  pd_bps: 2000 }
lgd_bps: 4500
# input:  { credit_score, monthly_income_cents, account_age_days, existing_balance_cents }
# output: { risk_score: 65, tier: "B", pd_bps: 400, lgd_bps: 4500, factor_points: { credit_score: 0, ... } }
```
Evaluator in `internal/loan` (`loan.EvaluateRiskScore`).

**loan_servicing**
```yaml
grace_period_days: 5
missed_payments_to_default: 3
late_fee_cents: 2500
# input:  { days_past_due, missed_payments_so_far }
# output: { is_missed: bool, late_fee_cents, should_default: bool }
```
Evaluator in `internal/loan` (`loan.EvaluateServicing`).

**fx_policy**
```yaml
supported_currencies: [USD, USDC]
pairs:
  - { base: USDC, quote: USD, mock_rate: "0.99850000", source: mock }   # USD per 1 USDC, 8 dp string
rounding: half_up                      # half_up | down
treasury_fx_accounts: { USD: treasury-fx-usd, USDC: treasury-fx-usdc }
# input:  { from: USD, to: USDC, amount_minor: 10000 }
# output: { to_amount_minor: 100150225, rate_scaled: 99850000, rate_display: "0.99850000", source: "mock" }
```
Evaluator in `internal/ledger` (`ledger.EvaluateFXPolicy`). The provider
inverts the rate for USD→USDC using exact big.Rat arithmetic.

**depeg_policy** (documentation-only; not enforced — stated limitation)
```yaml
enforced: false
reference_currency: USD
thresholds:
  watch_below_usd: "0.99500000"
  depeg_below_usd: "0.97000000"
actions_on_depeg: [ "halt_usdc_funding_in_waterfall", "halt_fx_conversions", "notify_treasury" ]
# input:  { usdc_price_usd: "0.9800" }  output: { status: "watch" | "normal" | "depeg" }
```
Evaluator in `internal/ledger` (`ledger.EvaluateDepegPolicy`).

**spend_waterfall**
```yaml
conditions_evaluated_in_order:
  - { rule: prefer_fiat_if_sufficient }
  - { rule: prefer_stablecoin_if_sufficient }
  - { rule: split_fiat_then_stablecoin }
  - { rule: decline }
allow_split: true
# input:  { amount_cents, usd_balance_cents, usdc_balance_usd_equivalent_cents }
# output: { decision: "approved"|"declined", matched_rule: "...", funding: [ { currency: "USD", usd_equivalent_cents: 5000, share_pct: 100 } ] }
```
Known `rule` values: `prefer_fiat_if_sufficient`,
`prefer_stablecoin_if_sufficient`, `split_fiat_then_stablecoin`,
`split_stablecoin_then_fiat`, `decline`. Evaluator in `internal/account`
(`account.EvaluateWaterfall`). Split rules only apply if `allow_split`.

**dashboard_thresholds**
```yaml
targets:
  lcr_minimum_pct: 100
  nsfr_minimum_pct: 100
  capital_adequacy_minimum_pct: 10.5
  loan_to_deposit_maximum_pct: 90
# input:  { lcr_pct, nsfr_pct, capital_adequacy_pct, loan_to_deposit_pct }
# output: { lcr: "ok"|"below_target", nsfr: ..., capital_adequacy: ..., loan_to_deposit: "ok"|"above_maximum" }
```
Evaluator in `internal/dashboard`.

**risk_weights**
```yaml
weights_by_tier_pct: { A: 50, B: 75, C: 100, D: 150 }
defaulted_weight_pct: 150
default_weight_pct: 100
# input:  { tier: "B", status: "active", ead_cents: 100000 }  output: { risk_weight_pct: 75, rwa_cents: 75000 }
```
Evaluator in `internal/dashboard`.

**liquidity_stress**
```yaml
stress_window_days: 30
deposit_runoff_pct: 10
hqla_haircut_pct: { USD: 0, USDC: 15 }
nsfr:
  asf_factors_pct: { customer_deposit: 90, equity_capital: 100 }
  rsf_factors_pct: { loan_receivable: 85, treasury_cash: 5, treasury_fx: 50 }
# input:  { hqla_cents, deposits_cents }  output: { net_outflows_cents, lcr_pct }
```
Evaluator in `internal/dashboard`.

**Evaluator registration** happens in `main.go` (integrator):
`rules.RegisterEvaluator("fx_policy", ledger.EvaluateFXPolicy)` etc. Each
domain package must also ship a test that loads its own rule file(s) from
`../../rules/...`, parses `examples`, and asserts its evaluator reproduces
`expected` (subset match — copy `rules.MatchExpected` or implement locally
until D2 lands; the rules package will export
`func MatchExpected(expected, actual map[string]any) (bool, string)`).

## Account (`internal/account`) — D4

```go
type Service struct{ /* *ledger.Service, *rules.Engine, simclock.Clock */ }
func New(l *ledger.Service, r *rules.Engine, clock simclock.Clock) *Service
func (s *Service) Seed(ctx) error   // idempotent: opens demo-account-1 (below), deposits USD 1,500.00 and USDC 800.000000
func (s *Service) Purchase(ctx, req PurchaseRequest) (Receipt, error)
func (s *Service) BulkRandomPurchases(ctx, req BulkRequest) ([]Receipt, error) // deterministic with req.Seed
func (s *Service) Deposit(ctx, accountID string, amt ledger.Amount) (ledger.Transaction, error)
func (s *Service) Get(ctx, id string) (View, error)      // View{ID, OwnerID, Balances map[currency]ledger.Amount, LedgerAccountIDs map[currency]string, UsdEquivalentCents int64}
func (s *Service) RegisterRoutes(mux)
```

Customer account `demo-account-1` (owner `demo-user`) maps to ledger
accounts `demo-account-1-usd` (customer_deposit, USD) and
`demo-account-1-usdc` (customer_deposit, USDC). Convention for any customer
account `<id>`: ledger IDs `<id>-usd` and `<id>-usdc`.

Purchase flow: compute balances → USDC USD-equivalent via
`ledger.Quote("USDC","USD",usdcMinor)` → `rules.Evaluate("spend_waterfall",
"purchase", purchaseID, input)` → post ledger txs: USD portion = debit
`<id>-usd` / credit `merchant-settlement-usd` (Kind `purchase`); USDC portion
= `Convert` USD-equivalent? **No** — USDC portion is spent in USDC: debit
`<id>-usdc` / credit `merchant-settlement-usdc` with the USDC amount from
`Quote("USD","USDC", usdPortionCents)` (Kind `purchase`). All purchase txs
are finalized and carry `Metadata["purchase_id"]`.

```go
type Receipt struct {
    PurchaseID string `json:"purchase_id"`; AccountID string; Status string /* approved|declined */
    AmountCents int64 `json:"amount_cents"`; Merchant string
    FundedBy []Funding `json:"funded_by"`   // Funding{Currency, AmountMinor, AmountDisplay, UsdEquivalentCents, SharePct int}
    Waterfall struct{ RuleID string; Version int; MatchedRule string; ApplicationID string } `json:"waterfall"`
    LedgerTransactionIDs []string `json:"ledger_transaction_ids"`
    DeclineReason string `json:"decline_reason,omitempty"`
    OccurredAt time.Time
}
```

HTTP: `POST /simulate/purchase` `{account_id, amount_cents, merchant?}`
→ 200 Receipt (approved) or 409 with the Receipt in `details` (declined);
`POST /simulate/purchases/bulk` `{account_id, count, min_cents, max_cents, seed?}`;
`GET /accounts/{id}`; `POST /accounts/{id}/deposit` `{currency, amount}`
(amount as decimal string "100.00"); `GET /accounts/{id}/purchases`;
`GET /accounts`.

## Loan (`internal/loan`) — D5

```go
type Applicant struct{ ID, Name, DepositAccountID string; CreditScore int; MonthlyIncomeCents, ExistingDebtMonthlyCents, ExistingBalanceCents int64; AccountAgeDays int }
type Service struct{ /* *ledger.Service, *rules.Engine, simclock.Clock, store.EventStore, applicants */ }
func New(l *ledger.Service, r *rules.Engine, es store.EventStore, clock simclock.Clock) *Service
func (s *Service) Seed(ctx) error   // applicants below + originates one baseline loan for demo-applicant-2 (24 months, 500,000 cents) so dashboards are non-empty
func (s *Service) Underwrite(ctx, req UnderwriteRequest) (UnderwriteResult, error)
func (s *Service) Originate(ctx, req OriginateRequest) (Loan, error)   // must be approved by Underwrite; posts Kind loan.disbursement
func (s *Service) Get(ctx, id) (Loan, error); List(ctx) ([]Loan, error)
func (s *Service) Schedule(ctx, id) ([]Installment, error)
func (s *Service) WhatIf(ctx, id, req WhatIfRequest) (WhatIfResult, error)
func (s *Service) Repay(ctx, id string, amountCents int64 /* 0 = next installment */) (Loan, error)
func (s *Service) MissPayment(ctx, id) (Loan, error)   // posts loan.late_fee, increments missed count
func (s *Service) Default(ctx, id, reason string) (Loan, error) // posts loan.writeoff of outstanding principal
func (s *Service) Tick(ctx) (TickReport, error)  // detect installments past due+grace without payment → MissPayment; missed ≥ threshold → Default. TickReport{MissedPayments, Defaults []string}
func (s *Service) BankForecast(ctx) (BankForecast, error) // ExpectedLossCents (Σ PD×LGD×EAD), PortfolioYieldBps, Cohorts []Cohort{Month, Loans, OriginatedCents, OutstandingCents, RepaidCents, Defaulted int, DefaultRatePct string}
func (s *Service) PortfolioSnapshot(ctx) ([]portfolio.LoanExposure, error)  // implements portfolio.Source
func (s *Service) RegisterRoutes(mux)
```

Seeded applicants (all with DepositAccountID `demo-account-1-usd`):

| ID | credit | income/mo | debt/mo | balance | age days | Note |
|---|---|---|---|---|---|---|
| `demo-applicant-1` | 650 | 650000 | 80000 | 150000 | 400 | approved at min 620, declined at 700 (demo step) |
| `demo-applicant-2` | 760 | 900000 | 50000 | 500000 | 900 | prime; baseline loan seeded |
| `demo-applicant-3` | 590 | 300000 | 120000 | 20000 | 60 | declined |

Loan ledger: each loan `L` gets account `loan-<L>-receivable` (loan_receivable,
USD). Disbursement: debit `loan-<L>-receivable` / credit borrower deposit
(Kind `loan.disbursement`). Repayment: debit borrower deposit / credit
`loan-<L>-receivable` (principal) + credit `interest-income-usd` (interest)
(Kind `loan.repayment`, Metadata `installment_no`, `principal_cents`,
`interest_cents`). Late fee: debit `loan-<L>-receivable` / credit
`fee-income-usd` (Kind `loan.late_fee`). Write-off: debit
`loan-loss-expense-usd` / credit `loan-<L>-receivable` (Kind `loan.writeoff`).
Loan status/state is **derived** by replaying `loan.*` events plus the
ledger; the loan package also appends its own events
(`loan.originated`, `loan.repaid`, `loan.payment_missed`, `loan.defaulted`)
carrying decision metadata (risk score, tier, rate).

Amortisation: monthly rate = annual_bps / 120000 as big.Rat; payment =
P·r/(1−(1+r)^−n) rounded half-up to cents; last installment absorbs rounding.

HTTP: `POST /loan/underwrite` `{applicant_id, requested_principal_cents?, term_months?}`
→ `{ decision: "approved"|"declined", applicant, risk: {risk_score, tier, pd_bps, lgd_bps}, pricing: {annual_rate_bps, tier, max_principal_cents}, monthly_installment_cents, affordability: {installment_to_income_pct, flag: "comfortable"|"stretched"|"unaffordable"}, reasons: [], rule_applications: [{rule_id, version, application_id}] }`;
`POST /loan/originate` `{applicant_id, principal_cents, term_months}`;
`GET /loans`, `GET /loans/{id}`, `GET /loans/{id}/schedule`,
`POST /loans/{id}/what-if` `{extra_monthly_cents?, lump_sum_cents?, lump_sum_at_installment?}`
→ `{baseline: {total_interest_cents, payoff_installments}, scenario: {...}, interest_saved_cents, months_saved}`;
`POST /loans/{id}/repay` `{amount_cents?}`; `POST /loans/{id}/miss-payment`;
`POST /loans/{id}/default` `{reason}`; `GET /loans/forecast/bank`;
`GET /loans/{id}/forecast` (customer-facing: schedule summary + affordability + what-if defaults);
`GET /loan/applicants`.

## Dashboard (`internal/dashboard`) — D6

```go
func New(l *ledger.Service, r *rules.Engine, src portfolio.Source, clock simclock.Clock) *Service
func (s *Service) LedgerBook(ctx) (LedgerBook, error)
func (s *Service) Flows(ctx, from, to time.Time) (Flows, error)
func (s *Service) Ratios(ctx) (Ratios, error)      // LTD, LCR-style, NSFR-style, CAR-style + threshold status via rules.Evaluate("dashboard_thresholds", "dashboard", "snapshot-<ts>")
func (s *Service) Summary(ctx) (Summary, error)    // all three plus clock.Now()
func (s *Service) RegisterRoutes(mux)
```
Definitions (all from ledger balances, USD-equivalent via `Quote`):
* deposits = Σ customer_deposit balances; loans = Σ loan_receivable (outstanding)
* LTD% = loans / deposits
* HQLA = Σ treasury_cash (USDC haircut per rule) ; net outflows = deposits × runoff% ; LCR% = HQLA/netOutflows
* NSFR% = (deposits×ASF + equity×ASF) / (loans×RSF + treasury_cash×RSF + treasury_fx×RSF)
* Capital = equity_capital + interest_income + fee_income − loan_loss_expense ; RWA = Σ per-loan `risk_weights` ; CAR% = capital / RWA
* Every metric struct carries `"disclaimer": "Illustrative / PoC — not a certified regulatory figure"` and `"modeled_on": "APRA APS 210"` / `"APRA APS 110"`.

HTTP: `GET /dashboard/summary`, `GET /dashboard/ledger-book`,
`GET /dashboard/flows?window_days=30` (or `from`/`to` RFC3339),
`GET /dashboard/ratios`.

## Simulate (`internal/simulate`) — D7

```go
func New(clock *simclock.SimClock, loans *loan.Service, accounts *account.Service, ledger *ledger.Service) *Service
```
HTTP (PoC-only surface): `POST /simulate/advance-time` `{days}` or `{to}` →
`{previous, now, tick: TickReport}` (advances clock then calls `loans.Tick`);
`POST /simulate/trigger-default` `{loan_id, reason?}`;
`POST /simulate/run-scenario` `{steps:[...]}` step types: `advance_time{days}`,
`repay{loan_id, count}`, `miss_payment{loan_id}`, `random_purchases{account_id, count, min_cents, max_cents}`,
`originate{applicant_id, principal_cents, term_months}`, `deposit{account_id, currency, amount}`,
`trigger_default{loan_id}` → `{results:[{step, ok, output|error}]}`;
`GET /simulate/clock`. Also `GET /simulate/presets` returning a couple of
canned scenario bodies the UI can fire.

## Demo (`internal/demo`) — D8

Loads `demo/demo-scenarios.yaml` (shape exactly as in the build prompt:
`scenario_id, description, steps[{id,label,action,request|rule_id+field_path+new_value,narration}]`).
```go
func New(r *rules.Engine, handler http.Handler /* the full mux, for call_api via httptest.NewRecorder */, clock simclock.Clock, path string) *Service
```
HTTP: `GET /demo/scenarios`, `GET /demo/scenarios/{id}` (steps + run state),
`POST /demo/scenarios/{id}/start` (snapshot live version per rule touched),
`POST /demo/scenarios/{id}/steps/{step_id}/run` → `{step, action, before?, after?, response?, narration}`,
`POST /demo/scenarios/{id}/reset` (Reactivate snapshot versions as new versions),
`POST /demo/scenarios/{id}/reload` (re-read YAML file).
`edit_rule` goes through `rules.Save` (validated path; set `AllowFailingExamples: true`
only when the YAML step says `allow_failing_examples: true`, and report example results).
Field paths: dotted with `[n]` indexes (`conditions_evaluated_in_order[0].rule`).

## Web (`web/`) — D9

`web/embed.go`: `package web; //go:embed index.html app.js styles.css; var FS embed.FS`
served at `/` by main. Tabs: Dashboard, Ledger, Account & Purchases, Loans,
Simulation, Rules Editor, Demo Mode. Vanilla JS, no build step, no CDN.
