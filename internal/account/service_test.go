package account_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

// ruleReader adapts *rules.Engine to ledger.RuleReader, mirroring
// cmd/stablebank/main.go's wiring pattern.
type ruleReader struct{ e *rules.Engine }

func (r ruleReader) Get(ruleID string) (map[string]any, int, error) { return r.e.GetContent(ruleID) }

type harness struct {
	svc    *account.Service
	ledger *ledger.Service
	rules  *rules.Engine
	store  *memstore.Store
	clock  simclock.Clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	clock := simclock.Fixed(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))

	engine := rules.NewEngine(st, clock, "../../rules")
	engine.RegisterEvaluator("spend_waterfall", account.EvaluateWaterfall)
	if err := engine.LoadFromDisk(ctx); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}

	rates := ledger.NewRuleRateProvider(ruleReader{engine})
	book := ledger.New(st, clock, rates)
	if err := book.EnsureSystemAccounts(ctx); err != nil {
		t.Fatalf("EnsureSystemAccounts: %v", err)
	}

	svc := account.New(book, engine, clock)
	return &harness{svc: svc, ledger: book, rules: engine, store: st, clock: clock}
}

// openAccount opens a fresh customer account with ledger IDs <id>-usd /
// <id>-usdc and deposits the given USD/USDC amounts (either may be zero,
// which is skipped).
func (h *harness) openAccount(t *testing.T, id string, usdCents int64, usdcMicros int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.ledger.OpenAccount(ctx, ledger.Account{ID: id + "-usd", OwnerID: "u-" + id, Type: ledger.CustomerDeposit, Currency: "USD"}); err != nil {
		t.Fatalf("OpenAccount usd: %v", err)
	}
	if _, err := h.ledger.OpenAccount(ctx, ledger.Account{ID: id + "-usdc", OwnerID: "u-" + id, Type: ledger.CustomerDeposit, Currency: "USDC"}); err != nil {
		t.Fatalf("OpenAccount usdc: %v", err)
	}
	if usdCents > 0 {
		if _, err := h.svc.Deposit(ctx, id, ledger.Amount{Currency: "USD", Minor: usdCents}); err != nil {
			t.Fatalf("Deposit USD: %v", err)
		}
	}
	if usdcMicros > 0 {
		if _, err := h.svc.Deposit(ctx, id, ledger.Amount{Currency: "USDC", Minor: usdcMicros}); err != nil {
			t.Fatalf("Deposit USDC: %v", err)
		}
	}
}

func TestSeedIsIdempotentAndFundsDemoAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := h.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed (second call): %v", err)
	}

	view, err := h.svc.Get(ctx, account.DemoAccountID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if view.OwnerID != account.DemoOwnerID {
		t.Fatalf("owner = %q, want %q", view.OwnerID, account.DemoOwnerID)
	}
	if view.Balances["USD"].Minor != 150000 {
		t.Fatalf("USD balance = %d, want 150000", view.Balances["USD"].Minor)
	}
	if view.Balances["USDC"].Minor != 800_000000 {
		t.Fatalf("USDC balance = %d, want 800000000", view.Balances["USDC"].Minor)
	}

	// Seeding twice must not double the deposits.
	usdTxs, err := h.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: "demo-account-1-usd", Kind: "deposit"})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(usdTxs) != 1 {
		t.Fatalf("got %d USD deposit txs, want 1", len(usdTxs))
	}
}

func TestPurchaseFullyFundedByFiat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.openAccount(t, "acct-fiat", 150000 /* $1500 */, 800_000000 /* 800 USDC */)

	receipt, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-fiat", AmountCents: 10000, Merchant: "Test Merchant"})
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}
	if receipt.Status != "approved" {
		t.Fatalf("status = %q, want approved", receipt.Status)
	}
	if receipt.Waterfall.MatchedRule != "prefer_fiat_if_sufficient" {
		t.Fatalf("matched_rule = %q, want prefer_fiat_if_sufficient", receipt.Waterfall.MatchedRule)
	}
	if len(receipt.FundedBy) != 1 || receipt.FundedBy[0].Currency != "USD" || receipt.FundedBy[0].SharePct != 100 {
		t.Fatalf("unexpected funded_by: %#v", receipt.FundedBy)
	}

	bal, err := h.ledger.Balance(ctx, "acct-fiat-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Minor != 150000-10000 {
		t.Fatalf("USD balance after purchase = %d, want %d", bal.Minor, 150000-10000)
	}
}

func TestPurchaseFundedByStablecoinWhenFiatInsufficient(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// USD balance too small for the purchase; USDC balance alone covers it.
	h.openAccount(t, "acct-coin", 2000 /* $20 */, 1000_000000 /* 1000 USDC ~= $998.50 */)

	receipt, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-coin", AmountCents: 5000})
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}
	if receipt.Waterfall.MatchedRule != "prefer_stablecoin_if_sufficient" {
		t.Fatalf("matched_rule = %q, want prefer_stablecoin_if_sufficient", receipt.Waterfall.MatchedRule)
	}
	if len(receipt.FundedBy) != 1 || receipt.FundedBy[0].Currency != "USDC" || receipt.FundedBy[0].SharePct != 100 {
		t.Fatalf("unexpected funded_by: %#v", receipt.FundedBy)
	}

	usdBal, err := h.ledger.Balance(ctx, "acct-coin-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if usdBal.Minor != 2000 {
		t.Fatalf("USD balance should be untouched, got %d", usdBal.Minor)
	}
}

func TestPurchaseSplitWhenOnlyCombinedSufficient(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.openAccount(t, "acct-split", 2000 /* $20 */, 50_000000 /* 50 USDC ~= $49.93 */)

	receipt, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-split", AmountCents: 6000})
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}
	if receipt.Waterfall.MatchedRule != "split_fiat_then_stablecoin" {
		t.Fatalf("matched_rule = %q, want split_fiat_then_stablecoin", receipt.Waterfall.MatchedRule)
	}
	if len(receipt.FundedBy) != 2 {
		t.Fatalf("expected 2 funding legs, got %#v", receipt.FundedBy)
	}
	totalPct := 0
	totalUsdEquiv := int64(0)
	for _, f := range receipt.FundedBy {
		totalPct += f.SharePct
		totalUsdEquiv += f.UsdEquivalentCents
	}
	if totalPct != 100 {
		t.Fatalf("share_pct total = %d, want 100", totalPct)
	}
	if totalUsdEquiv != receipt.AmountCents {
		t.Fatalf("usd_equivalent_cents total = %d, want %d", totalUsdEquiv, receipt.AmountCents)
	}
	if len(receipt.LedgerTransactionIDs) != 2 {
		t.Fatalf("expected 2 ledger transactions, got %d", len(receipt.LedgerTransactionIDs))
	}

	usdBal, err := h.ledger.Balance(ctx, "acct-split-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if usdBal.Minor != 0 {
		t.Fatalf("USD balance after split purchase = %d, want 0 (fully drawn down)", usdBal.Minor)
	}
}

func TestPurchaseDeclinedWhenInsufficientAndLedgerUntouched(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.openAccount(t, "acct-poor", 1000, 0)

	before, err := h.ledger.ListTransactions(ctx, ledger.TxFilter{Kind: "purchase"})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}

	receipt, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-poor", AmountCents: 5000})
	if !errors.Is(err, account.ErrDeclined) {
		t.Fatalf("Purchase err = %v, want ErrDeclined", err)
	}
	if receipt.Status != "declined" || receipt.DeclineReason != "insufficient_funds" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.Waterfall.MatchedRule != "decline" {
		t.Fatalf("matched_rule = %q, want decline", receipt.Waterfall.MatchedRule)
	}

	after, err := h.ledger.ListTransactions(ctx, ledger.TxFilter{Kind: "purchase"})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("ledger gained purchase transactions on a decline: before=%d after=%d", len(before), len(after))
	}

	usdBal, err := h.ledger.Balance(ctx, "acct-poor-usd")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if usdBal.Minor != 1000 {
		t.Fatalf("USD balance changed on a decline: got %d, want 1000", usdBal.Minor)
	}
}

func TestPurchaseRejectsNonPositiveAmount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.openAccount(t, "acct-guard", 1000, 0)

	for _, amt := range []int64{0, -100} {
		_, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-guard", AmountCents: amt})
		var verr *account.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("amount %d: err = %v, want *ValidationError", amt, err)
		}
	}
}

func TestRuleApplicationLoggedWithPurchaseIDAsEntity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.openAccount(t, "acct-audit", 150000, 0)

	receipt, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-audit", AmountCents: 1000})
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}

	apps, err := h.store.ListRuleApplications(ctx, "spend_waterfall", receipt.PurchaseID, 0)
	if err != nil {
		t.Fatalf("ListRuleApplications: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("got %d rule applications for purchase %q, want 1", len(apps), receipt.PurchaseID)
	}
	if apps[0].EntityID != receipt.PurchaseID {
		t.Fatalf("application entity_id = %q, want %q", apps[0].EntityID, receipt.PurchaseID)
	}
	if apps[0].RuleID != "spend_waterfall" {
		t.Fatalf("application rule_id = %q, want spend_waterfall", apps[0].RuleID)
	}
}

func TestHotReloadFlipsFundingToStablecoin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// Baseline: with the default policy, a purchase well within the USD
	// balance is funded from fiat.
	before, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: account.DemoAccountID, AmountCents: 10000})
	if err != nil {
		t.Fatalf("Purchase (before edit): %v", err)
	}
	if before.Waterfall.MatchedRule != "prefer_fiat_if_sufficient" {
		t.Fatalf("baseline matched_rule = %q, want prefer_fiat_if_sufficient", before.Waterfall.MatchedRule)
	}

	// Edit the live rule: flip conditions_evaluated_in_order[0].rule to
	// prefer_stablecoin_if_sufficient (the exact field path the demo script
	// edits). This intentionally breaks example 1 (which expects fiat to
	// win), so we must allow failing examples for this save, exactly as the
	// demo's edit_rule step would.
	rule, err := h.rules.Get("spend_waterfall")
	if err != nil {
		t.Fatalf("Get(spend_waterfall): %v", err)
	}
	newRaw := strings.Replace(string(rule.Raw), "{ rule: prefer_fiat_if_sufficient }", "{ rule: prefer_stablecoin_if_sufficient }", 1)
	if newRaw == string(rule.Raw) {
		t.Fatal("test setup: expected literal condition text not found in spend_waterfall.yaml")
	}
	if _, err := h.rules.Save(ctx, "spend_waterfall", []byte(newRaw), rules.SaveOptions{
		Author: "test", ChangeNote: "flip to stablecoin-first", AllowFailingExamples: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The very next purchase must now be funded from USDC, with no other
	// code path change required (hot reload).
	after, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: account.DemoAccountID, AmountCents: 10000})
	if err != nil {
		t.Fatalf("Purchase (after edit): %v", err)
	}
	if after.Waterfall.MatchedRule != "prefer_stablecoin_if_sufficient" {
		t.Fatalf("matched_rule after hot reload = %q, want prefer_stablecoin_if_sufficient", after.Waterfall.MatchedRule)
	}
	if len(after.FundedBy) != 1 || after.FundedBy[0].Currency != "USDC" {
		t.Fatalf("unexpected funded_by after hot reload: %#v", after.FundedBy)
	}
}

func TestBulkRandomPurchasesDeterministic(t *testing.T) {
	run := func() []account.Receipt {
		h := newHarness(t)
		ctx := context.Background()
		if err := h.svc.Seed(ctx); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		receipts, err := h.svc.BulkRandomPurchases(ctx, account.BulkRequest{
			AccountID: account.DemoAccountID, Count: 6, MinCents: 500, MaxCents: 8000, Seed: 42,
		})
		if err != nil {
			t.Fatalf("BulkRandomPurchases: %v", err)
		}
		return receipts
	}

	a := run()
	b := run()
	if len(a) != len(b) {
		t.Fatalf("got %d and %d receipts, want equal lengths", len(a), len(b))
	}
	for i := range a {
		if a[i].AmountCents != b[i].AmountCents || a[i].Merchant != b[i].Merchant || a[i].Status != b[i].Status {
			t.Fatalf("receipt %d differs between runs: %#v vs %#v", i, a[i], b[i])
		}
	}
}

func TestGetUnknownAccountReturnsNotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, err := h.svc.Get(ctx, "does-not-exist")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func TestListPurchasesNewestFirst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.openAccount(t, "acct-hist", 150000, 800_000000)

	var ids []string
	for _, amt := range []int64{1000, 2000, 3000} {
		r, err := h.svc.Purchase(ctx, account.PurchaseRequest{AccountID: "acct-hist", AmountCents: amt})
		if err != nil {
			t.Fatalf("Purchase: %v", err)
		}
		ids = append(ids, r.PurchaseID)
	}

	list, err := h.svc.ListPurchases(ctx, "acct-hist")
	if err != nil {
		t.Fatalf("ListPurchases: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d purchases, want 3", len(list))
	}
	// Newest first: the last purchase made (ids[2]) should come first.
	if list[0].PurchaseID != ids[2] || list[2].PurchaseID != ids[0] {
		t.Fatalf("purchases not newest-first: got order %v, want reverse of %v", []string{list[0].PurchaseID, list[1].PurchaseID, list[2].PurchaseID}, ids)
	}
}
