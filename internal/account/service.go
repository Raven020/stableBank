package account

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
)

// newRand returns a deterministic math/rand source seeded from seed, used
// by BulkRandomPurchases so an identical seed always produces an identical
// sequence of amounts and merchants.
func newRand(seed int64) *mrand.Rand {
	return mrand.New(mrand.NewSource(seed))
}

// ErrDeclined is returned by Purchase (alongside a populated Receipt) when
// the spend_waterfall rule declines a purchase for insufficient funds.
var ErrDeclined = errors.New("account: purchase declined")

// spendWaterfallRuleID is the fixed rules/registry.yaml rule_id this
// package evaluates against.
const spendWaterfallRuleID = "spend_waterfall"

// Service is the D4 transaction-account service: it owns no persistent
// state of its own (customer accounts are entirely derived from the
// ledger's customer_deposit accounts, so a restart against Postgres
// rediscovers them), and defers every money movement to *ledger.Service
// and every funding decision to *rules.Engine.
type Service struct {
	ledger *ledger.Service
	rules  *rules.Engine
	clock  simclock.Clock
}

// New constructs an account Service.
func New(l *ledger.Service, r *rules.Engine, clock simclock.Clock) *Service {
	return &Service{ledger: l, rules: r, clock: clock}
}

// newPurchaseID returns an identifier that sorts lexicographically in
// creation order (a zero-padded real-wall-clock nanosecond prefix plus a
// random suffix for uniqueness). This is deliberately independent of the
// simulated clock: ListPurchases needs a stable "newest first" ordering
// even when many purchases are posted while the simulated clock is frozen
// (e.g. under simclock.Fixed in tests, or between /simulate/advance-time
// calls in the demo), which OccurredAt alone cannot provide. It is purely
// an identifier/ordering concern, not a business timestamp: every
// ledger-visible time (Transaction.OccurredAt, Receipt.OccurredAt) still
// comes from the injected simclock.Clock.
func newPurchaseID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%020d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

// ledgerAccountID returns the ledger account id for a customer account's
// leg in the given ledger.Amount-style currency code ("USD" or "USDC").
func ledgerAccountID(customerID, currency string) string {
	return customerID + "-" + strings.ToLower(currency)
}

// --- account discovery / views ------------------------------------------

// Get returns the read model for customer account id. It is derived
// entirely from the ledger's <id>-usd / <id>-usdc customer_deposit
// accounts; at least one of them must exist.
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	usdID := ledgerAccountID(id, "USD")
	usdcID := ledgerAccountID(id, "USDC")

	usdAcc, usdErr := s.ledger.GetAccount(ctx, usdID)
	if usdErr != nil && !errors.Is(usdErr, store.ErrNotFound) {
		return View{}, usdErr
	}
	usdcAcc, usdcErr := s.ledger.GetAccount(ctx, usdcID)
	if usdcErr != nil && !errors.Is(usdcErr, store.ErrNotFound) {
		return View{}, usdcErr
	}
	if usdErr != nil && usdcErr != nil {
		return View{}, fmt.Errorf("account: %q: %w", id, store.ErrNotFound)
	}

	view := View{ID: id, Balances: map[string]ledger.Amount{}, LedgerAccountIDs: map[string]string{}}

	if usdErr == nil {
		view.OwnerID = usdAcc.OwnerID
		bal, err := s.ledger.Balance(ctx, usdID)
		if err != nil {
			return View{}, err
		}
		view.Balances["USD"] = bal
		view.LedgerAccountIDs["USD"] = usdID
		view.UsdEquivalentCents += bal.Minor
	}
	if usdcErr == nil {
		if view.OwnerID == "" {
			view.OwnerID = usdcAcc.OwnerID
		}
		bal, err := s.ledger.Balance(ctx, usdcID)
		if err != nil {
			return View{}, err
		}
		view.Balances["USDC"] = bal
		view.LedgerAccountIDs["USDC"] = usdcID
		quote, err := s.ledger.Quote("USDC", "USD", bal.Minor)
		if err != nil {
			return View{}, err
		}
		view.UsdEquivalentCents += quote.To.Minor
	}
	return view, nil
}

// ListAccounts discovers every customer account by scanning the ledger's
// customer_deposit accounts (any account whose ID ends in "-usd" or
// "-usdc"). This is what makes account discovery survive a restart against
// Postgres without a separate registry.
func (s *Service) ListAccounts(ctx context.Context) ([]View, error) {
	accs, err := s.ledger.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, a := range accs {
		if a.Type != ledger.CustomerDeposit {
			continue
		}
		switch {
		case strings.HasSuffix(a.ID, "-usd"):
			seen[strings.TrimSuffix(a.ID, "-usd")] = true
		case strings.HasSuffix(a.ID, "-usdc"):
			seen[strings.TrimSuffix(a.ID, "-usdc")] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]View, 0, len(ids))
	for _, id := range ids {
		v, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// --- deposits -------------------------------------------------------------

// Deposit posts and finalizes a Kind "deposit" transaction crediting
// customer account id's ledger leg for amt.Currency: debit
// treasury-cash-<ccy> / credit <id>-<ccy>. The ledger account must already
// exist (opened via Seed or by the integrator).
func (s *Service) Deposit(ctx context.Context, id string, amt ledger.Amount) (ledger.Transaction, error) {
	if amt.Minor <= 0 {
		return ledger.Transaction{}, &ValidationError{"deposit amount must be positive"}
	}
	if _, ok := ledger.Currencies[amt.Currency]; !ok {
		return ledger.Transaction{}, &ValidationError{fmt.Sprintf("unknown currency %q", amt.Currency)}
	}
	target := ledgerAccountID(id, amt.Currency)
	if _, err := s.ledger.GetAccount(ctx, target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ledger.Transaction{}, fmt.Errorf("account: %q has no %s ledger account: %w", id, amt.Currency, store.ErrNotFound)
		}
		return ledger.Transaction{}, err
	}
	treasury := "treasury-cash-" + strings.ToLower(amt.Currency)
	return s.ledger.PostAndFinalize(ctx, ledger.Transaction{
		Kind:        "deposit",
		Description: fmt.Sprintf("Deposit to %s", id),
		Currency:    amt.Currency,
		Entries: []ledger.Entry{
			{AccountID: treasury, Direction: ledger.Debit, Amount: amt},
			{AccountID: target, Direction: ledger.Credit, Amount: amt},
		},
		Metadata: map[string]string{"account_id": id},
	})
}

// --- seeding ---------------------------------------------------------------

// DemoAccountID and DemoOwnerID are the well-known seeded demo customer
// account and owner (CONTRACTS.md: demo-account-1, owner demo-user).
const (
	DemoAccountID = "demo-account-1"
	DemoOwnerID   = "demo-user"
)

// Seed idempotently opens the demo customer account (ledger accounts
// demo-account-1-usd / demo-account-1-usdc, owned by demo-user) if
// missing, then deposits USD 1,500.00 and USDC 800.000000 via Deposit, but
// only the first time (i.e. only if that ledger account has no prior
// deposit transaction).
func (s *Service) Seed(ctx context.Context) error {
	if err := s.openCustomerAccount(ctx, DemoAccountID, DemoOwnerID); err != nil {
		return err
	}
	usd, err := ledger.ParseAmount("USD", "1500.00")
	if err != nil {
		return err
	}
	usdc, err := ledger.ParseAmount("USDC", "800.000000")
	if err != nil {
		return err
	}
	if err := s.depositIfEmpty(ctx, DemoAccountID, usd); err != nil {
		return err
	}
	if err := s.depositIfEmpty(ctx, DemoAccountID, usdc); err != nil {
		return err
	}
	return nil
}

func (s *Service) openCustomerAccount(ctx context.Context, id, owner string) error {
	for _, cur := range []string{"USD", "USDC"} {
		lid := ledgerAccountID(id, cur)
		if _, err := s.ledger.GetAccount(ctx, lid); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := s.ledger.OpenAccount(ctx, ledger.Account{
			ID:       lid,
			OwnerID:  owner,
			Type:     ledger.CustomerDeposit,
			Currency: cur,
			Name:     fmt.Sprintf("%s (%s)", id, cur),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) depositIfEmpty(ctx context.Context, id string, amt ledger.Amount) error {
	target := ledgerAccountID(id, amt.Currency)
	txs, err := s.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: target, Kind: "deposit"})
	if err != nil {
		return err
	}
	if len(txs) > 0 {
		return nil
	}
	_, err = s.Deposit(ctx, id, amt)
	return err
}

// --- purchases --------------------------------------------------------------

// Purchase evaluates the spend_waterfall rule against account id's current
// balances and, if approved, posts finalized ledger purchase transactions
// for each funding leg. If declined, no ledger transactions are posted;
// Purchase still returns a populated Receipt (Status "declined") alongside
// ErrDeclined so callers can distinguish "no funds" from a real failure.
func (s *Service) Purchase(ctx context.Context, req PurchaseRequest) (Receipt, error) {
	if req.AccountID == "" {
		return Receipt{}, &ValidationError{"account_id is required"}
	}
	if req.AmountCents <= 0 {
		return Receipt{}, &ValidationError{"amount_cents must be positive"}
	}
	merchant := req.Merchant
	if merchant == "" {
		merchant = defaultMerchants[0]
	}

	usdID := ledgerAccountID(req.AccountID, "USD")
	usdcID := ledgerAccountID(req.AccountID, "USDC")

	usdBal, err := s.ledger.Balance(ctx, usdID)
	if err != nil {
		return Receipt{}, fmt.Errorf("account: %q: %w", req.AccountID, err)
	}
	usdcBal, err := s.ledger.Balance(ctx, usdcID)
	if err != nil {
		return Receipt{}, fmt.Errorf("account: %q: %w", req.AccountID, err)
	}
	usdcQuote, err := s.ledger.Quote("USDC", "USD", usdcBal.Minor)
	if err != nil {
		return Receipt{}, err
	}

	purchaseID := newPurchaseID()
	input := map[string]any{
		"amount_cents":                      req.AmountCents,
		"usd_balance_cents":                 usdBal.Minor,
		"usdc_balance_usd_equivalent_cents": usdcQuote.To.Minor,
	}
	decision, err := s.rules.Evaluate(ctx, spendWaterfallRuleID, "purchase", purchaseID, input)
	if err != nil {
		return Receipt{}, fmt.Errorf("account: evaluating spend_waterfall: %w", err)
	}

	matchedRule, _ := decision.Output["matched_rule"].(string)
	receipt := Receipt{
		PurchaseID:  purchaseID,
		AccountID:   req.AccountID,
		AmountCents: req.AmountCents,
		Merchant:    merchant,
		Waterfall: WaterfallInfo{
			RuleID:        decision.RuleID,
			Version:       decision.Version,
			MatchedRule:   matchedRule,
			ApplicationID: decision.ApplicationID,
		},
		OccurredAt: s.clock.Now(),
	}

	decisionStr, _ := decision.Output["decision"].(string)
	if decisionStr != "approved" {
		receipt.Status = "declined"
		receipt.DeclineReason = "insufficient_funds"
		return receipt, ErrDeclined
	}

	fundingRaw, _ := decision.Output["funding"].([]any)
	if len(fundingRaw) == 0 {
		return Receipt{}, errors.New("account: spend_waterfall approved a purchase with no funding legs")
	}

	var (
		txIDs    []string
		fundings []Funding
	)
	for _, raw := range fundingRaw {
		leg, ok := raw.(map[string]any)
		if !ok {
			return Receipt{}, fmt.Errorf("account: malformed funding leg %v", raw)
		}
		currency, _ := leg["currency"].(string)
		usdEquiv, err := numField(leg["usd_equivalent_cents"])
		if err != nil {
			return Receipt{}, fmt.Errorf("account: funding leg usd_equivalent_cents: %w", err)
		}
		sharePctRaw, err := numField(leg["share_pct"])
		if err != nil {
			return Receipt{}, fmt.Errorf("account: funding leg share_pct: %w", err)
		}
		sharePct := int(sharePctRaw)

		meta := map[string]string{
			"purchase_id":          purchaseID,
			"merchant":             merchant,
			"funded_share_pct":     strconv.Itoa(sharePct),
			"usd_equivalent_cents": strconv.FormatInt(usdEquiv, 10),
			"amount_cents":         strconv.FormatInt(req.AmountCents, 10),
			"matched_rule":         matchedRule,
			"rule_id":              decision.RuleID,
			"rule_version":         strconv.Itoa(decision.Version),
			"application_id":       decision.ApplicationID,
		}

		switch currency {
		case "USD":
			amt := ledger.Amount{Currency: "USD", Minor: usdEquiv}
			tx, err := s.ledger.PostAndFinalize(ctx, ledger.Transaction{
				Kind:        "purchase",
				Description: fmt.Sprintf("Purchase at %s", merchant),
				Currency:    "USD",
				Entries: []ledger.Entry{
					{AccountID: usdID, Direction: ledger.Debit, Amount: amt},
					{AccountID: "merchant-settlement-usd", Direction: ledger.Credit, Amount: amt},
				},
				Metadata: meta,
			})
			if err != nil {
				return Receipt{}, err
			}
			txIDs = append(txIDs, tx.ID)
			fundings = append(fundings, Funding{
				Currency:           "USD",
				AmountMinor:        amt.Minor,
				AmountDisplay:      amt.Display(),
				UsdEquivalentCents: usdEquiv,
				SharePct:           sharePct,
			})
		case "USDC":
			legQuote, err := s.ledger.Quote("USD", "USDC", usdEquiv)
			if err != nil {
				return Receipt{}, err
			}
			amt := legQuote.To
			tx, err := s.ledger.PostAndFinalize(ctx, ledger.Transaction{
				Kind:        "purchase",
				Description: fmt.Sprintf("Purchase at %s", merchant),
				Currency:    "USDC",
				Entries: []ledger.Entry{
					{AccountID: usdcID, Direction: ledger.Debit, Amount: amt},
					{AccountID: "merchant-settlement-usdc", Direction: ledger.Credit, Amount: amt},
				},
				Metadata: meta,
			})
			if err != nil {
				return Receipt{}, err
			}
			txIDs = append(txIDs, tx.ID)
			fundings = append(fundings, Funding{
				Currency:           "USDC",
				AmountMinor:        amt.Minor,
				AmountDisplay:      amt.Display(),
				UsdEquivalentCents: usdEquiv,
				SharePct:           sharePct,
			})
		default:
			return Receipt{}, fmt.Errorf("account: unknown funding currency %q", currency)
		}
	}

	receipt.Status = "approved"
	receipt.FundedBy = fundings
	receipt.LedgerTransactionIDs = txIDs
	return receipt, nil
}

// defaultMerchants is the small fixed merchant list BulkRandomPurchases
// samples from; index 0 is also the default single-purchase merchant name
// when PurchaseRequest.Merchant is blank.
var defaultMerchants = []string{
	"General Store",
	"Coffee Corner",
	"Grocery Mart",
	"Metro Transit",
	"Cloud Hosting Co",
	"Bookstore Nine",
	"Pharmacy Plus",
	"Streaming Plus",
}

// BulkRandomPurchases issues req.Count purchases of a uniformly random
// amount in [MinCents, MaxCents] against a random merchant from
// defaultMerchants, using a math/rand source seeded from req.Seed so the
// whole sequence (amounts, merchants, and therefore ledger effects) is
// deterministic for a given seed. Declined purchases are recorded in the
// returned slice (with Status "declined") rather than aborting the run;
// only an unexpected error stops the batch early.
func (s *Service) BulkRandomPurchases(ctx context.Context, req BulkRequest) ([]Receipt, error) {
	if req.AccountID == "" {
		return nil, &ValidationError{"account_id is required"}
	}
	if req.Count <= 0 {
		return nil, &ValidationError{"count must be positive"}
	}
	if req.MinCents <= 0 || req.MaxCents <= 0 || req.MaxCents < req.MinCents {
		return nil, &ValidationError{"min_cents/max_cents must be positive with max_cents >= min_cents"}
	}

	rng := newRand(req.Seed)
	span := req.MaxCents - req.MinCents + 1

	receipts := make([]Receipt, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		amount := req.MinCents
		if span > 1 {
			amount += rng.Int63n(span)
		}
		merchant := defaultMerchants[rng.Intn(len(defaultMerchants))]
		receipt, err := s.Purchase(ctx, PurchaseRequest{
			AccountID:   req.AccountID,
			AmountCents: amount,
			Merchant:    merchant,
		})
		if err != nil && !errors.Is(err, ErrDeclined) {
			return receipts, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

// --- purchase history -------------------------------------------------------

// ListPurchases reconstructs every approved purchase against customer
// account id from the ledger's Kind "purchase" transactions on its -usd /
// -usdc legs, grouped by Metadata["purchase_id"], newest first. Declined
// purchases post no ledger transactions and therefore do not appear here.
func (s *Service) ListPurchases(ctx context.Context, id string) ([]Receipt, error) {
	usdID := ledgerAccountID(id, "USD")
	usdcID := ledgerAccountID(id, "USDC")

	usdTxs, err := s.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: usdID, Kind: "purchase"})
	if err != nil {
		return nil, err
	}
	usdcTxs, err := s.ledger.ListTransactions(ctx, ledger.TxFilter{AccountID: usdcID, Kind: "purchase"})
	if err != nil {
		return nil, err
	}

	all := make([]ledger.Transaction, 0, len(usdTxs)+len(usdcTxs))
	all = append(all, usdTxs...)
	all = append(all, usdcTxs...)

	type group struct {
		receipt Receipt
	}
	groups := map[string]*group{}
	var order []string

	for _, tx := range all {
		pid := tx.Metadata["purchase_id"]
		if pid == "" {
			continue
		}
		g, ok := groups[pid]
		if !ok {
			amountCents, _ := strconv.ParseInt(tx.Metadata["amount_cents"], 10, 64)
			version, _ := strconv.Atoi(tx.Metadata["rule_version"])
			g = &group{receipt: Receipt{
				PurchaseID:  pid,
				AccountID:   id,
				Status:      "approved",
				AmountCents: amountCents,
				Merchant:    tx.Metadata["merchant"],
				Waterfall: WaterfallInfo{
					RuleID:        tx.Metadata["rule_id"],
					Version:       version,
					MatchedRule:   tx.Metadata["matched_rule"],
					ApplicationID: tx.Metadata["application_id"],
				},
			}}
			groups[pid] = g
			order = append(order, pid)
		}
		if tx.OccurredAt.After(g.receipt.OccurredAt) {
			g.receipt.OccurredAt = tx.OccurredAt
		}
		g.receipt.LedgerTransactionIDs = append(g.receipt.LedgerTransactionIDs, tx.ID)

		var legAccount string
		if tx.Currency == "USD" {
			legAccount = usdID
		} else {
			legAccount = usdcID
		}
		var minor int64
		for _, e := range tx.Entries {
			if e.AccountID == legAccount {
				minor = e.Amount.Minor
			}
		}
		usdEquiv, _ := strconv.ParseInt(tx.Metadata["usd_equivalent_cents"], 10, 64)
		sharePct, _ := strconv.Atoi(tx.Metadata["funded_share_pct"])
		g.receipt.FundedBy = append(g.receipt.FundedBy, Funding{
			Currency:           tx.Currency,
			AmountMinor:        minor,
			AmountDisplay:      ledger.Amount{Currency: tx.Currency, Minor: minor}.Display(),
			UsdEquivalentCents: usdEquiv,
			SharePct:           sharePct,
		})
	}

	// purchase_id is generated by newPurchaseID, which sorts
	// lexicographically in creation order (a zero-padded wall-clock
	// nanosecond prefix); sorting descending on it gives a stable
	// newest-first ordering even when every leg shares the same simulated
	// OccurredAt.
	sort.Strings(order)
	out := make([]Receipt, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, groups[order[i]].receipt)
	}
	return out, nil
}
