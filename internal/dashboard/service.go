package dashboard

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/portfolio"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
)

// Service is the D6 dashboard: a read-only view over the ledger (D1) and the
// loan portfolio (via internal/portfolio) that never writes to the ledger
// and never mutates rules; it only reads and evaluates them.
type Service struct {
	ledger *ledger.Service
	rules  *rules.Engine
	src    portfolio.Source
	clock  simclock.Clock
}

// New constructs the dashboard Service.
func New(l *ledger.Service, r *rules.Engine, src portfolio.Source, clock simclock.Clock) *Service {
	return &Service{ledger: l, rules: r, src: src, clock: clock}
}

// inflowKinds / outflowKinds classify ledger transaction Kinds for Flows.
// loan.late_fee is deliberately excluded from inflows: it is fee income
// accrued on the books, not new cash received into the bank.
var (
	inflowKinds  = map[string]bool{"deposit": true, "loan.repayment": true}
	outflowKinds = map[string]bool{"withdrawal": true, "loan.disbursement": true, "purchase": true}
)

// --- ledger book ---------------------------------------------------------

// LedgerBook returns every ledger account's balance, grouped by account
// type and currency, plus bank-wide totals and treasury FX exposure.
func (s *Service) LedgerBook(ctx context.Context) (LedgerBook, error) {
	accounts, err := s.ledger.ListAccounts(ctx)
	if err != nil {
		return LedgerBook{}, fmt.Errorf("dashboard: listing accounts: %w", err)
	}
	balances, err := s.ledger.Balances(ctx)
	if err != nil {
		return LedgerBook{}, fmt.Errorf("dashboard: loading balances: %w", err)
	}
	byType, err := s.ledger.BalancesByType(ctx)
	if err != nil {
		return LedgerBook{}, fmt.Errorf("dashboard: loading balances by type: %w", err)
	}

	out := LedgerBook{AsOf: s.clock.Now(), ByType: map[string]map[string]int64{}}
	for _, a := range accounts {
		bal := balances[a.ID]
		out.Accounts = append(out.Accounts, AccountBalance{
			AccountID: a.ID,
			Type:      string(a.Type),
			Currency:  a.Currency,
			Minor:     bal.Minor,
			Display:   bal.Display(),
		})
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].AccountID < out.Accounts[j].AccountID })

	for t, byCur := range byType {
		m := make(map[string]int64, len(byCur))
		for cur, minor := range byCur {
			m[cur] = minor
		}
		out.ByType[string(t)] = m
	}

	fiatCents := sumAcrossTypes(byType, "USD", ledger.CustomerDeposit, ledger.TreasuryCash, ledger.TreasuryFX)
	usdcMicro := sumAcrossTypes(byType, "USDC", ledger.CustomerDeposit, ledger.TreasuryCash, ledger.TreasuryFX)
	usdcUsdEquiv, err := s.toUSD("USDC", usdcMicro)
	if err != nil {
		return LedgerBook{}, err
	}
	out.Totals = LedgerTotals{
		TotalFiatCents:              fiatCents,
		TotalUsdcMicro:              usdcMicro,
		TotalUsdcUsdEquivalentCents: usdcUsdEquiv,
	}

	for _, cur := range []string{"USD", "USDC"} {
		minor := byType[ledger.TreasuryFX][cur]
		usdEquiv, err := s.toUSD(cur, minor)
		if err != nil {
			return LedgerBook{}, err
		}
		out.TreasuryFX = append(out.TreasuryFX, TreasuryFXExposure{Currency: cur, Minor: minor, UsdEquivalentCents: usdEquiv})
	}
	return out, nil
}

// sumAcrossTypes sums one currency's minor balance across several account
// types.
func sumAcrossTypes(byType map[ledger.AccountType]map[string]int64, currency string, types ...ledger.AccountType) int64 {
	var total int64
	for _, t := range types {
		total += byType[t][currency]
	}
	return total
}

// toUSD converts minor units of currency into USD cents via ledger.Quote.
// USD is the identity conversion; a zero amount never calls Quote (so it
// works even when no FX rate is configured for a currency the bank happens
// to hold none of).
func (s *Service) toUSD(currency string, minor int64) (int64, error) {
	if currency == "USD" {
		return minor, nil
	}
	if minor == 0 {
		return 0, nil
	}
	q, err := s.ledger.Quote(currency, "USD", minor)
	if err != nil {
		return 0, fmt.Errorf("dashboard: quoting %s->USD: %w", currency, err)
	}
	return q.To.Minor, nil
}

// usdEquivalentBalance sums account type t's balances across every
// currency it holds, converted to USD cents.
func (s *Service) usdEquivalentBalance(byType map[ledger.AccountType]map[string]int64, t ledger.AccountType) (int64, error) {
	var total int64
	for cur, minor := range byType[t] {
		usd, err := s.toUSD(cur, minor)
		if err != nil {
			return 0, err
		}
		total += usd
	}
	return total, nil
}

// --- flows -----------------------------------------------------------

// Flows summarises money in/out over [from, to) in USD-equivalent cents,
// broken down by transaction Kind and by calendar day (for charting).
func (s *Service) Flows(ctx context.Context, from, to time.Time) (Flows, error) {
	txs, err := s.ledger.ListTransactions(ctx, ledger.TxFilter{From: from})
	if err != nil {
		return Flows{}, fmt.Errorf("dashboard: listing transactions: %w", err)
	}

	out := Flows{From: from, To: to}
	kindTotals := map[string]*FlowBreakdown{}
	var kindOrder []string
	dailyIdx := map[string]*DailyFlow{}
	var dailyOrder []string

	for _, tx := range txs {
		if !tx.OccurredAt.Before(to) {
			continue // [from, to) is a half-open interval
		}
		isIn := inflowKinds[tx.Kind]
		isOut := outflowKinds[tx.Kind]
		if !isIn && !isOut {
			continue
		}

		usdCents, err := s.toUSD(tx.Currency, txPrincipal(tx))
		if err != nil {
			return Flows{}, err
		}

		bd, ok := kindTotals[tx.Kind]
		if !ok {
			bd = &FlowBreakdown{Kind: tx.Kind}
			kindTotals[tx.Kind] = bd
			kindOrder = append(kindOrder, tx.Kind)
		}
		bd.UsdEquivalentCents += usdCents
		bd.Count++

		day := tx.OccurredAt.UTC().Format("2006-01-02")
		d, ok := dailyIdx[day]
		if !ok {
			d = &DailyFlow{Date: day}
			dailyIdx[day] = d
			dailyOrder = append(dailyOrder, day)
		}
		if isIn {
			out.InflowsCents += usdCents
			d.InflowsCents += usdCents
		} else {
			out.OutflowsCents += usdCents
			d.OutflowsCents += usdCents
		}
	}
	out.NetCents = out.InflowsCents - out.OutflowsCents

	sort.Strings(kindOrder)
	for _, k := range kindOrder {
		out.ByKind = append(out.ByKind, *kindTotals[k])
	}
	sort.Strings(dailyOrder)
	for _, d := range dailyOrder {
		out.Daily = append(out.Daily, *dailyIdx[d])
	}
	return out, nil
}

// txPrincipal returns a transaction's single-sided total (debits ==
// credits by ledger.Post's invariant), i.e. the amount of tx.Currency that
// moved.
func txPrincipal(tx ledger.Transaction) int64 {
	var total int64
	for _, e := range tx.Entries {
		if e.Direction == ledger.Debit {
			total += e.Amount.Minor
		}
	}
	return total
}

// --- ratios -----------------------------------------------------------

// snapshotID is the entity ID used for dashboard-wide rule applications
// (dashboard_thresholds, liquidity_stress), per CONTRACTS.md's
// "snapshot-<unix ts>" convention.
func snapshotID(now time.Time) string {
	return fmt.Sprintf("snapshot-%d", now.Unix())
}

// computeRatio returns numCents/denCents*100 rounded to 2dp via exact
// big.Rat arithmetic, and whether denCents was zero (in which case the
// returned value is 0 and callers must report status "n/a").
func computeRatio(numCents, denCents int64) (valuePct float64, zeroDenominator bool) {
	if denCents == 0 {
		return 0, true
	}
	return ratToFloatDP(ratioPct(numCents, denCents), 2), false
}

// factorPct reads a percentage out of a rule content map, defaulting to 0
// when the key is absent or malformed (an account type the bank happens to
// hold nothing of need not appear in every rule's factor table).
func factorPct(m map[string]any, key string) *big.Rat {
	if m == nil {
		return big.NewRat(0, 1)
	}
	v, ok := m[key]
	if !ok {
		return big.NewRat(0, 1)
	}
	r, err := toRat(v)
	if err != nil {
		return big.NewRat(0, 1)
	}
	return r
}

func int64FromAny(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	default:
		return 0
	}
}

func float64FromAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	default:
		return 0
	}
}

// hqlaCents sums treasury_cash balances, applying liquidity_stress's
// per-currency hqla_haircut_pct discount in the native currency before
// converting to USD (e.g. USDC HQLA is discounted 15% before being valued
// in USD).
func (s *Service) hqlaCents(byType map[ledger.AccountType]map[string]int64, haircuts map[string]any) (int64, error) {
	var total int64
	for cur, minor := range byType[ledger.TreasuryCash] {
		haircut := factorPct(haircuts, cur)
		net := minor - pctOf(minor, haircut)
		usd, err := s.toUSD(cur, net)
		if err != nil {
			return 0, err
		}
		total += usd
	}
	return total, nil
}

// Ratios computes the four illustrative ratios the dashboard shows
// (loan-to-deposit, LCR, NSFR, capital adequacy) and their threshold
// statuses. All USD-equivalent conversions go through ledger.Quote; all
// percentage math is exact math/big.Rat, only converted to float64 at the
// JSON boundary.
func (s *Service) Ratios(ctx context.Context) (Ratios, error) {
	now := s.clock.Now()

	byType, err := s.ledger.BalancesByType(ctx)
	if err != nil {
		return Ratios{}, fmt.Errorf("dashboard: loading balances by type: %w", err)
	}

	depositsCents, err := s.usdEquivalentBalance(byType, ledger.CustomerDeposit)
	if err != nil {
		return Ratios{}, err
	}
	loansCents, err := s.usdEquivalentBalance(byType, ledger.LoanReceivable)
	if err != nil {
		return Ratios{}, err
	}
	treasuryCashCents, err := s.usdEquivalentBalance(byType, ledger.TreasuryCash)
	if err != nil {
		return Ratios{}, err
	}
	treasuryFXCents, err := s.usdEquivalentBalance(byType, ledger.TreasuryFX)
	if err != nil {
		return Ratios{}, err
	}
	equityCents := byType[ledger.EquityCapital]["USD"]
	interestIncomeCents := byType[ledger.InterestIncome]["USD"]
	feeIncomeCents := byType[ledger.FeeIncome]["USD"]
	loanLossExpenseCents := byType[ledger.LoanLossExpense]["USD"]

	thresholdsRule, err := s.rules.Get("dashboard_thresholds")
	if err != nil {
		return Ratios{}, fmt.Errorf("dashboard: loading dashboard_thresholds: %w", err)
	}
	targets, _ := thresholdsRule.Content["targets"].(map[string]any)

	// --- loan to deposit ---
	ltdValuePct, ltdZero := computeRatio(loansCents, depositsCents)

	// --- LCR, via the liquidity_stress evaluator ---
	liqRule, err := s.rules.Get("liquidity_stress")
	if err != nil {
		return Ratios{}, fmt.Errorf("dashboard: loading liquidity_stress: %w", err)
	}
	haircuts, _ := liqRule.Content["hqla_haircut_pct"].(map[string]any)
	hqlaCents, err := s.hqlaCents(byType, haircuts)
	if err != nil {
		return Ratios{}, err
	}
	lcrDecision, err := s.rules.Evaluate(ctx, "liquidity_stress", "dashboard", snapshotID(now), map[string]any{
		"hqla_cents": hqlaCents, "deposits_cents": depositsCents,
	})
	if err != nil {
		return Ratios{}, fmt.Errorf("dashboard: evaluating liquidity_stress: %w", err)
	}
	netOutflowsCents := int64FromAny(lcrDecision.Output["net_outflows_cents"])
	lcrValuePct := float64FromAny(lcrDecision.Output["lcr_pct"])
	lcrZero := netOutflowsCents == 0

	// --- NSFR, from liquidity_stress's nsfr ASF/RSF factors ---
	nsfrSection, _ := liqRule.Content["nsfr"].(map[string]any)
	asfFactors, _ := nsfrSection["asf_factors_pct"].(map[string]any)
	rsfFactors, _ := nsfrSection["rsf_factors_pct"].(map[string]any)
	asfCents := pctOf(depositsCents, factorPct(asfFactors, "customer_deposit")) +
		pctOf(equityCents, factorPct(asfFactors, "equity_capital"))
	rsfCents := pctOf(loansCents, factorPct(rsfFactors, "loan_receivable")) +
		pctOf(treasuryCashCents, factorPct(rsfFactors, "treasury_cash")) +
		pctOf(treasuryFXCents, factorPct(rsfFactors, "treasury_fx"))
	nsfrValuePct, nsfrZero := computeRatio(asfCents, rsfCents)

	// --- capital adequacy ---
	exposures, err := s.src.PortfolioSnapshot(ctx)
	if err != nil {
		return Ratios{}, fmt.Errorf("dashboard: portfolio snapshot: %w", err)
	}
	var rwaCents int64
	for _, exp := range exposures {
		if exp.OutstandingPrincipalCents <= 0 {
			continue
		}
		d, err := s.rules.Evaluate(ctx, "risk_weights", "loan", exp.LoanID, map[string]any{
			"tier": exp.Tier, "status": string(exp.Status), "ead_cents": exp.OutstandingPrincipalCents,
		})
		if err != nil {
			return Ratios{}, fmt.Errorf("dashboard: evaluating risk_weights for loan %q: %w", exp.LoanID, err)
		}
		rwaCents += int64FromAny(d.Output["rwa_cents"])
	}
	capitalCents := equityCents + interestIncomeCents + feeIncomeCents - loanLossExpenseCents
	carValuePct, carZero := computeRatio(capitalCents, rwaCents)

	thresholdsDecision, err := s.rules.Evaluate(ctx, "dashboard_thresholds", "dashboard", snapshotID(now), map[string]any{
		"lcr_pct":              lcrValuePct,
		"nsfr_pct":             nsfrValuePct,
		"capital_adequacy_pct": carValuePct,
		"loan_to_deposit_pct":  ltdValuePct,
	})
	if err != nil {
		return Ratios{}, fmt.Errorf("dashboard: evaluating dashboard_thresholds: %w", err)
	}
	statusFor := func(key string, zeroDenominator bool) string {
		if zeroDenominator {
			return "n/a"
		}
		st, _ := thresholdsDecision.Output[key].(string)
		return st
	}

	ltd := Metric{
		ValuePct: ltdValuePct, NumeratorCents: loansCents, DenominatorCents: depositsCents,
		Status: statusFor("loan_to_deposit", ltdZero), TargetPct: float64FromAny(targets["loan_to_deposit_maximum_pct"]),
		Disclaimer: disclaimer, ModeledOn: modeledOnInternal,
		Components:        map[string]any{"loans_cents": loansCents, "deposits_cents": depositsCents},
		RuleApplicationID: thresholdsDecision.ApplicationID,
	}
	if ltdZero {
		ltd.Components["note"] = "zero_denominator"
	}

	lcrComponents := map[string]any{
		"hqla_cents": hqlaCents, "net_outflows_cents": netOutflowsCents, "deposits_cents": depositsCents,
	}
	if note, ok := lcrDecision.Output["note"]; ok {
		lcrComponents["note"] = note
	} else if lcrZero {
		lcrComponents["note"] = "zero_denominator"
	}
	lcr := Metric{
		ValuePct: lcrValuePct, NumeratorCents: hqlaCents, DenominatorCents: netOutflowsCents,
		Status: statusFor("lcr", lcrZero), TargetPct: float64FromAny(targets["lcr_minimum_pct"]),
		Disclaimer: disclaimer, ModeledOn: modeledOnLiquidity, Components: lcrComponents,
		// The liquidity_stress evaluation (not the thresholds one) is the
		// rule application that actually computed this metric's value, so
		// it is the more informative audit trail for this specific metric.
		RuleApplicationID: lcrDecision.ApplicationID,
	}

	nsfr := Metric{
		ValuePct: nsfrValuePct, NumeratorCents: asfCents, DenominatorCents: rsfCents,
		Status: statusFor("nsfr", nsfrZero), TargetPct: float64FromAny(targets["nsfr_minimum_pct"]),
		Disclaimer: disclaimer, ModeledOn: modeledOnLiquidity,
		Components: map[string]any{
			"asf_cents": asfCents, "rsf_cents": rsfCents, "deposits_cents": depositsCents,
			"equity_cents": equityCents, "loans_cents": loansCents,
			"treasury_cash_cents": treasuryCashCents, "treasury_fx_cents": treasuryFXCents,
		},
		RuleApplicationID: thresholdsDecision.ApplicationID,
	}
	if nsfrZero {
		nsfr.Components["note"] = "zero_denominator"
	}

	car := Metric{
		ValuePct: carValuePct, NumeratorCents: capitalCents, DenominatorCents: rwaCents,
		Status: statusFor("capital_adequacy", carZero), TargetPct: float64FromAny(targets["capital_adequacy_minimum_pct"]),
		Disclaimer: disclaimer, ModeledOn: modeledOnCapital,
		Components: map[string]any{
			"capital_cents": capitalCents, "rwa_cents": rwaCents,
			"equity_cents": equityCents, "interest_income_cents": interestIncomeCents,
			"fee_income_cents": feeIncomeCents, "loan_loss_expense_cents": loanLossExpenseCents,
		},
		RuleApplicationID: thresholdsDecision.ApplicationID,
	}
	if carZero {
		car.Components["note"] = "zero_denominator"
	}

	return Ratios{
		AsOf: now, LoanToDeposit: ltd, LCR: lcr, NSFR: nsfr, CapitalAdequacy: car,
		ThresholdRuleID: thresholdsDecision.RuleID, ThresholdVersion: thresholdsDecision.Version,
	}, nil
}

// --- summary -----------------------------------------------------------

// Summary bundles the ledger book, a trailing 30-day flow window and the
// current ratios.
func (s *Service) Summary(ctx context.Context) (Summary, error) {
	book, err := s.LedgerBook(ctx)
	if err != nil {
		return Summary{}, err
	}
	now := s.clock.Now()
	flows, err := s.Flows(ctx, now.AddDate(0, 0, -30), now)
	if err != nil {
		return Summary{}, err
	}
	ratios, err := s.Ratios(ctx)
	if err != nil {
		return Summary{}, err
	}
	return Summary{AsOf: now, LedgerBook: book, Flows: flows, Ratios: ratios}, nil
}
