# stableBank — Intelligence-First Banking PoC (Go)

A proof-of-concept banking platform where **business logic is versioned,
auditable data** (YAML rule files) rather than hardcoded Go. Two currencies
(USD fiat, USDC stablecoin), an event-sourced double-entry ledger, a
personal-loan product with dual (customer + bank) forecasts, a fiat/stablecoin
spend waterfall, an illustrative APRA-style metrics dashboard, a live rules
editor with hot reload, and a scripted stage demo — all in one binary.

> **This is a PoC.** Every external rail is simulated. See
> [Deliberate shortcuts](#deliberate-poc-shortcuts) before drawing conclusions
> from any figure it shows.

## Run it

Requirements: Go 1.25 (older Go auto-downloads the toolchain). Postgres is
optional.

```bash
# in-memory storage (no database needed)
go run ./cmd/stablebank
# → http://localhost:8080  (UI)   http://localhost:8080/healthz

# with Postgres
docker compose up -d postgres
DATABASE_URL='postgres://stablebank:stablebank@localhost:5432/stablebank?sslmode=disable' go run ./cmd/stablebank
```

Environment variables: `ADDR` (default `:8080`), `SIM_START` (RFC3339 start of
the simulated clock, default `2026-09-01T09:00:00Z`), `RULES_DIR` (default
`rules`), `DEMO_SCENARIOS` (default `demo/demo-scenarios.yaml`),
`DATABASE_URL` (unset = in-memory), `SOURCE_COMMIT` (recorded on seeded rule
versions).

Tests (all run against the in-memory store; the Postgres conformance test
skips unless `TEST_DATABASE_URL` is set):

```bash
go vet ./... && go test ./...
```

## What gets seeded

| Thing | ID | Notes |
|---|---|---|
| Demo user / account | `demo-user` / `demo-account-1` | ledger accounts `demo-account-1-usd` (USD 1,500.00) and `demo-account-1-usdc` (USDC 800.000000) |
| Applicants | `demo-applicant-1` (credit 650), `demo-applicant-2` (760), `demo-applicant-3` (590) | applicant 1 is approved at min credit score 620 and declined at 700 — the demo pivot |
| Baseline loan | for `demo-applicant-2` | USD 5,000.00, 24 months, disbursed into `demo-account-1-usd` |
| Capital | `equity-capital-usd` | USD 250,000.00 illustrative capital, cash held in `treasury-cash-usd` |

## Populate a demo state with the simulation endpoints

Everything under `/simulate/*` and `/demo/*` is a **PoC-only surface**. It
exists to drive demos and must be excluded from any production build.

```bash
B=http://localhost:8080

# 1. Underwrite the demo applicant (rule-driven; response lists the rule versions used)
curl -s $B/loan/underwrite -d '{"applicant_id":"demo-applicant-1"}'

# 2. Simulated card purchase — note the funded_by breakdown and the waterfall rule/version
curl -s $B/simulate/purchase -d '{"account_id":"demo-account-1","amount_cents":5000}'

# 3. Twenty random purchases to fill the ledger
curl -s $B/simulate/purchases/bulk -d '{"account_id":"demo-account-1","count":20,"min_cents":500,"max_cents":9000}'

# 4. Fast-forward 40 days — the seeded loan's first installment becomes overdue (due day 30 + 5 grace)
curl -s $B/simulate/advance-time -d '{"days":40}'

# 5. Run a canned multi-step scenario (see GET /simulate/presets for the bodies)
curl -s $B/simulate/presets
curl -s $B/simulate/run-scenario -d @- <<'JSON'
{"name":"quick","steps":[{"type":"advance_time","days":31},{"type":"repay","loan_id":"$first_loan","count":1},
 {"type":"random_purchases","account_id":"demo-account-1","count":5,"min_cents":500,"max_cents":5000}]}
JSON

# 6. Manually default a loan (bypasses the missed-payment accumulation)
curl -s $B/simulate/trigger-default -d '{"loan_id":"<loan id from GET /loans>"}'

# 7. Watch the portfolio metrics move
curl -s $B/dashboard/summary
```

### The "why this decision" lookup

Every decision writes a `rule_applications` row. Given any entity id
(purchase id, applicant id, loan id, dashboard snapshot id):

```bash
curl -s "$B/rules/applications?entity_id=demo-applicant-1"
curl -s $B/rules/loan_underwriting/applications/demo-applicant-1
```

### Live rule editing (the centrepiece)

```bash
# Read the live YAML
curl -s $B/rules/loan_underwriting | jq -r .raw_yaml > /tmp/uw.yaml
# Edit eligibility.min_credit_score: 620 → 700, then save as a new version
python3 - <<'PY'
import json,sys; y=open('/tmp/uw.yaml').read().replace('min_credit_score: 620','min_credit_score: 700')
print(json.dumps({"yaml":y,"change_note":"tighten credit","allow_failing_examples":True}))
PY
# | curl -s -X PUT $B/rules/loan_underwriting -d @-
curl -s $B/loan/underwrite -d '{"applicant_id":"demo-applicant-1"}'   # now declined, same code
```

Save = validate against the JSON Schema → run the rule's own `examples` as a
test gate → insert a new `rule_versions` row → replace the in-memory cache in
the same request. There is no cache between the save and the next decision.

The UI's **Rules Editor** tab does the same thing with a textarea, and the
**Demo Mode** tab runs the scripted sequence in `demo/demo-scenarios.yaml`
one button at a time with on-screen narration and a safe reset.

### Scripted stage demo from the shell

```bash
S=$B/demo/scenarios/live-demo-v1
curl -s $S                                   # steps, narration, run state
for step in step-1-baseline step-2-tighten-credit-score step-3-rerun-same-applicant \
            step-4-flip-waterfall step-5-rerun-purchase step-6-lower-lcr-target step-7-reset; do
  curl -s -X POST $S/steps/$step/run | head -c 600; echo
done
```

Step 1 approves `demo-applicant-1`; step 2 writes `loan_underwriting` v2 with
`min_credit_score: 700` (the rule's own examples now fail, which the step
consciously accepts); step 3 declines the same applicant with reason
`credit_score_below_minimum`; step 4 flips the waterfall; step 5's purchase
is funded 100% from USDC; step 6 lowers the LCR target; step 7 reactivates
the pre-demo versions as **new** versions (v3) so the audit trail is intact.
Steps are gated in order; pass `{"force": true}` to replay one out of order.

## Architecture

Modular monolith, one binary, clean package boundaries:

```
cmd/stablebank        wiring only
internal/simclock     the single shared "now"; /simulate/advance-time moves it
internal/store        EventStore + RuleStore interfaces; memstore (tests/dev) and pgstore (Postgres)
internal/ledger       System 1 — event-sourced, append-only, strict same-currency double entry,
                      integer minor units (USD 2dp, USDC 6dp), FX as two linked txs via treasury_fx
internal/rules        System 4 — YAML loader, JSON Schema validation, rule_versions/rule_applications,
                      examples-as-tests, versioned save, synchronous hot reload, GET/PUT /rules
internal/account      System 3 — /simulate/purchase, rule-driven spend waterfall, funded-by receipt
internal/loan         System 2 — shared risk score, underwriting, amortisation, what-if, bank forecast,
                      lifecycle as ledger transactions, missed-payment/default detection from the clock
internal/dashboard    System 5 — ledger book, money in/out, LTD, LCR/NSFR/CAR-style metrics + thresholds
internal/simulate     the "play button": advance-time, run-scenario, trigger-default
internal/demo         System 7 — scripted demo steps, sequenced buttons, reset-as-new-version
web/                  embedded vanilla-JS dashboard (System 5/6/7 UI)
rules/                the business logic: 9 rule files, JSON schemas, registry.yaml
migrations/           Postgres schema
```

`CONTRACTS.md` documents the cross-package APIs.

### Rules

| rule_id | domain | what it governs |
|---|---|---|
| `loan_underwriting` | loan | eligibility thresholds, pricing tiers, allowed terms |
| `risk_scoring` | loan | mock rule-based risk score → tier → PD/LGD (swappable for a model) |
| `loan_servicing` | loan | grace period, late fee, missed payments before default |
| `fx_policy` | ledger | mocked USD/USDC rate with provenance, rounding, treasury accounts |
| `depeg_policy` | ledger | **documentation only** — thresholds and actions, not enforced |
| `spend_waterfall` | account | order in which fiat/stablecoin fund a purchase |
| `dashboard_thresholds` | dashboard | target/minimum values the ratios are compared against |
| `risk_weights` | dashboard | mocked risk weight per loan tier for the CAR-style metric |
| `liquidity_stress` | dashboard | mocked stress window / runoff / ASF-RSF factors |

Each file carries `rule_id, version, description, rationale, owner,
last_reviewed` and an `examples` block; domain tests load those examples and
assert the evaluator reproduces them, so the rule files are self-verifying.

## Deliberate PoC shortcuts

* **No KYC / compliance controls.** Explicitly out of scope.
* **No real rails.** Card swipes are `POST /simulate/purchase`; there is no
  card processor, bank rail, or blockchain call. USDC settlement is
  ledger-internal only.
* **Mocked FX.** The USD/USDC rate is a configurable value in `fx_policy`
  with `source: mock`.
* **Rule-based mock risk score.** `risk_scoring` is a points table, designed
  to be swapped for a real model behind the same interface.
* **Depeg policy is documented, not monitored.** Nothing watches a price feed.
* **Single hardcoded demo user, no auth.**
* **Illustrative regulatory-style metrics only.** The LCR/NSFR/capital
  adequacy figures follow the *structure* of APRA APS 210 / APS 110 with
  mocked assumptions from rule files. They are labelled "Illustrative / PoC —
  not a certified regulatory figure" everywhere. Real compliance requires ADI
  licensing, audited capital and APRA supervision; none of that is claimed.
* **No approval workflow on rule saves.** A production version must require
  reviewed, multi-party sign-off (every rule change is a PR) before a new
  version becomes effective; the PoC saves immediately to make the demo
  visible.
* **Skipped by design:** stress testing, RAROC, prepayment (CPR) modelling.
* **PoC-only surfaces:** `/simulate/*` and `/demo/*` must not ship in a
  production build.
* **Postgres path is untested in this environment.** `pgstore` mirrors the
  in-memory store's semantics and shares a conformance suite, but the suite
  only runs against Postgres when `TEST_DATABASE_URL` is set.
