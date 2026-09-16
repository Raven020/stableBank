package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Raven020/stableBank/internal/store"
)

// Event types owned by the ledger package. Payload is always the JSON
// encoding of the corresponding Go struct (Account or Transaction).
const (
	eventAccountOpened = "ledger.account.opened"
	eventTxPosted      = "ledger.tx.posted"
	eventTxSettled     = "ledger.tx.settled"
	eventTxFinalized   = "ledger.tx.finalized"
)

// ledgerEventTypes is the set of event types this package owns; List calls
// are filtered to these so the ledger's projection never has to understand
// event payloads from other domains sharing the same store.
var ledgerEventTypes = []string{eventAccountOpened, eventTxPosted, eventTxSettled, eventTxFinalized}

// projection is the in-memory read model, rebuilt by replaying events.
// Balances are never mutated directly: they are derived from the running
// debit/credit totals accumulated when each transaction was posted.
type projection struct {
	mu       sync.Mutex
	lastSeq  int64
	accounts map[string]Account
	txs      map[string]Transaction
	debit    map[string]int64 // accountID -> lifetime debit total (minor units)
	credit   map[string]int64 // accountID -> lifetime credit total (minor units)
}

func newProjection() *projection {
	return &projection{
		accounts: map[string]Account{},
		txs:      map[string]Transaction{},
		debit:    map[string]int64{},
		credit:   map[string]int64{},
	}
}

// apply folds one event into the projection. Callers must hold p.mu.
func (p *projection) apply(e store.Event) error {
	switch e.Type {
	case eventAccountOpened:
		var a Account
		if err := json.Unmarshal(e.Payload, &a); err != nil {
			return fmt.Errorf("ledger: decode %s: %w", e.Type, err)
		}
		p.accounts[a.ID] = a
	case eventTxPosted:
		var tx Transaction
		if err := json.Unmarshal(e.Payload, &tx); err != nil {
			return fmt.Errorf("ledger: decode %s: %w", e.Type, err)
		}
		p.txs[tx.ID] = tx
		for _, en := range tx.Entries {
			switch en.Direction {
			case Debit:
				p.debit[en.AccountID] += en.Amount.Minor
			case Credit:
				p.credit[en.AccountID] += en.Amount.Minor
			}
		}
	case eventTxSettled, eventTxFinalized:
		var tx Transaction
		if err := json.Unmarshal(e.Payload, &tx); err != nil {
			return fmt.Errorf("ledger: decode %s: %w", e.Type, err)
		}
		if existing, ok := p.txs[tx.ID]; ok {
			existing.Status = tx.Status
			p.txs[tx.ID] = existing
		} else {
			p.txs[tx.ID] = tx
		}
	}
	if e.Seq > p.lastSeq {
		p.lastSeq = e.Seq
	}
	return nil
}

// refresh replays any events with Seq greater than the last one this
// projection has seen. Reads are cheap (bounded by new events since the last
// call); state is always derived from the event log, never mutated
// directly.
func (s *Service) refresh(ctx context.Context) error {
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	events, err := s.store.List(ctx, store.EventFilter{
		Types:    ledgerEventTypes,
		AfterSeq: s.proj.lastSeq,
	})
	if err != nil {
		return err
	}
	for _, e := range events {
		if err := s.proj.apply(e); err != nil {
			return err
		}
	}
	return nil
}

// applyAppended folds freshly appended events (already known to be new)
// directly into the projection without a round trip through the store.
func (s *Service) applyAppended(events []store.Event) {
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	for _, e := range events {
		_ = s.proj.apply(e)
	}
}
