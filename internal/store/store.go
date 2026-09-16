// Package store defines the persistence contracts shared by every domain
// package. Two implementations exist: memstore (in-memory, used by tests and
// when DATABASE_URL is unset) and pgstore (Postgres, the intended PoC target).
//
// The ledger, loans and accounts are fully event-sourced: the only mutable
// tables are the rule tables (rule_versions is append-only too; rule
// applications are an append-only audit log).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when a lookup matches nothing.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert violates a uniqueness constraint
// (e.g. inserting a rule version that already exists).
var ErrConflict = errors.New("store: conflict")

// Event is one immutable, append-only fact. Domain packages define the Type
// namespace they own, e.g. "ledger.account.opened", "ledger.tx.posted",
// "ledger.tx.settled", "loan.originated".
type Event struct {
	ID          string          `json:"id"`           // assigned by the store if empty
	Seq         int64           `json:"seq"`          // global monotonic sequence, assigned by the store
	Type        string          `json:"type"`         // dotted event type
	AggregateID string          `json:"aggregate_id"` // entity the event belongs to (tx id, account id, loan id)
	OccurredAt  time.Time       `json:"occurred_at"`  // simulated clock time
	RecordedAt  time.Time       `json:"recorded_at"`  // wall clock time, assigned by the store if zero
	Payload     json.RawMessage `json:"payload"`
}

// EventFilter narrows a List call. Zero values mean "no constraint".
type EventFilter struct {
	Types       []string // any of these types
	AggregateID string
	AfterSeq    int64 // strictly greater than
	Limit       int
}

// EventStore is the append-only event log.
type EventStore interface {
	// Append persists events in order, assigning Seq (and ID/RecordedAt when
	// empty). It returns the stored events with those fields populated. All
	// events in one call are committed atomically.
	Append(ctx context.Context, events ...Event) ([]Event, error)
	// List returns events matching filter ordered by Seq ascending.
	List(ctx context.Context, filter EventFilter) ([]Event, error)
}

// RuleVersion is one immutable version of a business rule. Saving an edit
// never updates a row: it inserts a new version and closes the previous one
// by setting its EffectiveTo.
type RuleVersion struct {
	RuleID        string     `json:"rule_id"`
	Version       int        `json:"version"`
	RuleType      string     `json:"rule_type"`
	Content       []byte     `json:"content"`      // raw YAML exactly as authored
	ContentHash   string     `json:"content_hash"` // sha256 hex of Content
	SourceCommit  string     `json:"source_commit"`
	Author        string     `json:"author"`
	ChangeNote    string     `json:"change_note"`
	EffectiveFrom time.Time  `json:"effective_from"` // simulated clock time
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	CreatedAt     time.Time  `json:"created_at"` // wall clock
}

// RuleApplication records one decision: which rule + version, what inputs,
// what outcome, applied to which entity.
type RuleApplication struct {
	ID         string          `json:"id"`
	RuleID     string          `json:"rule_id"`
	Version    int             `json:"version"`
	EntityType string          `json:"entity_type"` // "loan", "transaction", "account", "dashboard", ...
	EntityID   string          `json:"entity_id"`
	Inputs     json.RawMessage `json:"inputs"`
	Outcome    json.RawMessage `json:"outcome"`
	AppliedAt  time.Time       `json:"applied_at"` // simulated clock time
	RecordedAt time.Time       `json:"recorded_at"`
}

// RuleStore persists rule versions and the decision audit log.
type RuleStore interface {
	// InsertRuleVersion appends v. If a currently-open version (EffectiveTo ==
	// nil) exists for the same RuleID, its EffectiveTo is set to
	// v.EffectiveFrom in the same transaction. Returns ErrConflict if
	// (RuleID, Version) already exists.
	InsertRuleVersion(ctx context.Context, v RuleVersion) error
	// ListRuleVersions returns all versions for ruleID ascending by Version.
	// An empty ruleID returns every version of every rule.
	ListRuleVersions(ctx context.Context, ruleID string) ([]RuleVersion, error)
	// GetRuleVersion returns one version or ErrNotFound.
	GetRuleVersion(ctx context.Context, ruleID string, version int) (RuleVersion, error)
	// InsertRuleApplication appends to the audit log, assigning ID/RecordedAt
	// when empty.
	InsertRuleApplication(ctx context.Context, a RuleApplication) (RuleApplication, error)
	// ListRuleApplications filters by ruleID and/or entityID (either may be
	// empty), newest first, at most limit rows (0 = store default of 100).
	ListRuleApplications(ctx context.Context, ruleID, entityID string, limit int) ([]RuleApplication, error)
}

// Store is the full persistence surface handed to main.
type Store interface {
	EventStore
	RuleStore
	Close() error
}
