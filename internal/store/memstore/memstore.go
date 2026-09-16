// Package memstore is an in-memory store.Store used by tests and by the
// binary when DATABASE_URL is not set. It mirrors pgstore semantics exactly.
package memstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/Raven020/stableBank/internal/store"
)

// Store is safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	events   []store.Event
	versions map[string][]store.RuleVersion // ruleID -> ascending by version
	apps     []store.RuleApplication
	seq      int64
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{versions: map[string][]store.RuleVersion{}}
}

// NewID returns a random 16-byte hex identifier.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Store) Append(_ context.Context, events ...store.Event) ([]store.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Event, 0, len(events))
	for _, e := range events {
		s.seq++
		e.Seq = s.seq
		if e.ID == "" {
			e.ID = NewID()
		}
		if e.RecordedAt.IsZero() {
			e.RecordedAt = time.Now().UTC()
		}
		s.events = append(s.events, e)
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) List(_ context.Context, f store.EventFilter) ([]store.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Event
	for _, e := range s.events {
		if e.Seq <= f.AfterSeq {
			continue
		}
		if f.AggregateID != "" && e.AggregateID != f.AggregateID {
			continue
		}
		if len(f.Types) > 0 && !contains(f.Types, e.Type) {
			continue
		}
		out = append(out, e)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) InsertRuleVersion(_ context.Context, v store.RuleVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.versions[v.RuleID]
	for _, existing := range vs {
		if existing.Version == v.Version {
			return store.ErrConflict
		}
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	for i := range vs {
		if vs[i].EffectiveTo == nil {
			t := v.EffectiveFrom
			vs[i].EffectiveTo = &t
		}
	}
	vs = append(vs, v)
	sort.Slice(vs, func(i, j int) bool { return vs[i].Version < vs[j].Version })
	s.versions[v.RuleID] = vs
	return nil
}

func (s *Store) ListRuleVersions(_ context.Context, ruleID string) ([]store.RuleVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ruleID != "" {
		return append([]store.RuleVersion(nil), s.versions[ruleID]...), nil
	}
	ids := make([]string, 0, len(s.versions))
	for id := range s.versions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []store.RuleVersion
	for _, id := range ids {
		out = append(out, s.versions[id]...)
	}
	return out, nil
}

func (s *Store) GetRuleVersion(_ context.Context, ruleID string, version int) (store.RuleVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.versions[ruleID] {
		if v.Version == version {
			return v, nil
		}
	}
	return store.RuleVersion{}, store.ErrNotFound
}

func (s *Store) InsertRuleApplication(_ context.Context, a store.RuleApplication) (store.RuleApplication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = NewID()
	}
	if a.RecordedAt.IsZero() {
		a.RecordedAt = time.Now().UTC()
	}
	s.apps = append(s.apps, a)
	return a, nil
}

func (s *Store) ListRuleApplications(_ context.Context, ruleID, entityID string, limit int) ([]store.RuleApplication, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	var out []store.RuleApplication
	for i := len(s.apps) - 1; i >= 0 && len(out) < limit; i-- {
		a := s.apps[i]
		if ruleID != "" && a.RuleID != ruleID {
			continue
		}
		if entityID != "" && a.EntityID != entityID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Store) Close() error { return nil }

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
