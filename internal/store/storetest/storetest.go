// Package storetest is a conformance test suite shared by every
// store.Store implementation (internal/store/memstore, internal/store/pgstore).
// It pins down the exact semantics documented in internal/store/store.go so
// the implementations stay interchangeable.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/store"
)

// Run exercises every method of store.Store against stores returned by
// newStore. newStore is called once per subtest (and, for the ordering /
// default-limit checks, more than once) and must each time return a store
// with no pre-existing events, rule versions, or rule applications.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Run("Events", func(t *testing.T) { testEvents(t, newStore(t)) })
	t.Run("RuleVersions", func(t *testing.T) { testRuleVersions(t, newStore(t)) })
	t.Run("RuleApplications", func(t *testing.T) { testRuleApplications(t, newStore) })
}

func testEvents(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	in := []store.Event{
		{Type: "ledger.tx.posted", AggregateID: "acct-1", OccurredAt: base, Payload: json.RawMessage(`{"n":1}`)},
		{Type: "ledger.tx.settled", AggregateID: "acct-1", OccurredAt: base.Add(time.Minute), Payload: json.RawMessage(`{"n":2}`)},
		{Type: "loan.originated", AggregateID: "loan-1", OccurredAt: base.Add(2 * time.Minute), Payload: json.RawMessage(`{"n":3}`)},
	}
	out, err := s.Append(ctx, in...)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 events back, got %d", len(out))
	}
	for i, e := range out {
		if e.Seq == 0 {
			t.Errorf("event %d: Seq not assigned", i)
		}
		if e.ID == "" {
			t.Errorf("event %d: ID not assigned", i)
		}
		if e.RecordedAt.IsZero() {
			t.Errorf("event %d: RecordedAt not assigned", i)
		}
	}
	if !(out[0].Seq < out[1].Seq && out[1].Seq < out[2].Seq) {
		t.Fatalf("expected strictly increasing Seq within one Append call, got %d, %d, %d",
			out[0].Seq, out[1].Seq, out[2].Seq)
	}

	// Explicit ID / RecordedAt are preserved, not overwritten.
	explicit := store.Event{
		ID: "evt-explicit", Type: "loan.repaid", AggregateID: "loan-1",
		OccurredAt: base, RecordedAt: base.Add(time.Hour), Payload: json.RawMessage(`{}`),
	}
	outExplicit, err := s.Append(ctx, explicit)
	if err != nil {
		t.Fatalf("Append explicit: %v", err)
	}
	if outExplicit[0].ID != "evt-explicit" {
		t.Errorf("expected explicit ID preserved, got %q", outExplicit[0].ID)
	}
	if !outExplicit[0].RecordedAt.Equal(base.Add(time.Hour)) {
		t.Errorf("expected explicit RecordedAt preserved, got %v", outExplicit[0].RecordedAt)
	}
	if outExplicit[0].Seq <= out[2].Seq {
		t.Errorf("expected a later Append to get a larger Seq")
	}

	all, err := s.List(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 events total, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("List not ascending by Seq at index %d", i)
		}
	}

	byAgg, err := s.List(ctx, store.EventFilter{AggregateID: "acct-1"})
	if err != nil {
		t.Fatalf("List by aggregate: %v", err)
	}
	if len(byAgg) != 2 {
		t.Fatalf("expected 2 events for acct-1, got %d", len(byAgg))
	}
	for _, e := range byAgg {
		if e.AggregateID != "acct-1" {
			t.Fatalf("List by aggregate returned wrong aggregate: %+v", e)
		}
	}

	byType, err := s.List(ctx, store.EventFilter{Types: []string{"loan.originated", "loan.repaid"}})
	if err != nil {
		t.Fatalf("List by types: %v", err)
	}
	if len(byType) != 2 {
		t.Fatalf("expected 2 events matching either type, got %d", len(byType))
	}

	afterSeq, err := s.List(ctx, store.EventFilter{AfterSeq: out[0].Seq})
	if err != nil {
		t.Fatalf("List after seq: %v", err)
	}
	if len(afterSeq) != 3 {
		t.Fatalf("expected 3 events strictly after the first seq, got %d", len(afterSeq))
	}
	for _, e := range afterSeq {
		if e.Seq <= out[0].Seq {
			t.Fatalf("List AfterSeq returned seq %d <= filter %d", e.Seq, out[0].Seq)
		}
	}

	limited, err := s.List(ctx, store.EventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 events with Limit:2, got %d", len(limited))
	}
	if limited[0].Seq != all[0].Seq || limited[1].Seq != all[1].Seq {
		t.Fatalf("List with a limit should return the oldest matching events first")
	}

	// Combined filter + limit.
	combined, err := s.List(ctx, store.EventFilter{AggregateID: "acct-1", Limit: 1})
	if err != nil {
		t.Fatalf("List combined: %v", err)
	}
	if len(combined) != 1 || combined[0].AggregateID != "acct-1" {
		t.Fatalf("List combined filter+limit misbehaved: %+v", combined)
	}
}

func testRuleVersions(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	v1 := store.RuleVersion{
		RuleID: "fx_policy", Version: 1, RuleType: "fx_policy",
		Content: []byte("v1-content"), ContentHash: "hash1",
		Author: "alice", ChangeNote: "initial", EffectiveFrom: base,
	}
	if err := s.InsertRuleVersion(ctx, v1); err != nil {
		t.Fatalf("InsertRuleVersion v1: %v", err)
	}

	got, err := s.GetRuleVersion(ctx, "fx_policy", 1)
	if err != nil {
		t.Fatalf("GetRuleVersion v1: %v", err)
	}
	if got.EffectiveTo != nil {
		t.Fatalf("expected v1 open (EffectiveTo nil) before v2 exists, got %v", got.EffectiveTo)
	}
	if string(got.Content) != "v1-content" || got.ContentHash != "hash1" || got.Author != "alice" || got.ChangeNote != "initial" {
		t.Fatalf("GetRuleVersion returned mismatched fields: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("expected CreatedAt to be assigned")
	}
	if !got.EffectiveFrom.Equal(base) {
		t.Fatalf("expected EffectiveFrom preserved, got %v", got.EffectiveFrom)
	}

	// Duplicate (rule_id, version) -> ErrConflict, and must not disturb the
	// existing row.
	dup := v1
	dup.Content = []byte("different")
	if err := s.InsertRuleVersion(ctx, dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict inserting duplicate version, got %v", err)
	}
	stillOpen, err := s.GetRuleVersion(ctx, "fx_policy", 1)
	if err != nil {
		t.Fatalf("GetRuleVersion after conflict: %v", err)
	}
	if stillOpen.EffectiveTo != nil {
		t.Fatalf("a rejected conflicting insert must not close v1, got EffectiveTo=%v", stillOpen.EffectiveTo)
	}
	if string(stillOpen.Content) != "v1-content" {
		t.Fatalf("a rejected conflicting insert must not modify v1's content, got %q", stillOpen.Content)
	}

	v2From := base.Add(24 * time.Hour)
	v2 := store.RuleVersion{
		RuleID: "fx_policy", Version: 2, RuleType: "fx_policy",
		Content: []byte("v2-content"), ContentHash: "hash2",
		Author: "bob", ChangeNote: "update", EffectiveFrom: v2From,
	}
	if err := s.InsertRuleVersion(ctx, v2); err != nil {
		t.Fatalf("InsertRuleVersion v2: %v", err)
	}

	v1After, err := s.GetRuleVersion(ctx, "fx_policy", 1)
	if err != nil {
		t.Fatalf("GetRuleVersion v1 after v2: %v", err)
	}
	if v1After.EffectiveTo == nil || !v1After.EffectiveTo.Equal(v2From) {
		t.Fatalf("expected v1.EffectiveTo == v2.EffectiveFrom (%v), got %v", v2From, v1After.EffectiveTo)
	}
	v2After, err := s.GetRuleVersion(ctx, "fx_policy", 2)
	if err != nil {
		t.Fatalf("GetRuleVersion v2: %v", err)
	}
	if v2After.EffectiveTo != nil {
		t.Fatalf("expected v2 to be open, got EffectiveTo=%v", v2After.EffectiveTo)
	}

	// A different rule_id is entirely independent.
	other := store.RuleVersion{
		RuleID: "depeg_policy", Version: 1, RuleType: "depeg_policy",
		Content: []byte("d1"), ContentHash: "dhash1", EffectiveFrom: base,
	}
	if err := s.InsertRuleVersion(ctx, other); err != nil {
		t.Fatalf("InsertRuleVersion other rule: %v", err)
	}

	if _, err := s.GetRuleVersion(ctx, "fx_policy", 99); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing version, got %v", err)
	}
	if _, err := s.GetRuleVersion(ctx, "no_such_rule", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing rule, got %v", err)
	}

	fxVersions, err := s.ListRuleVersions(ctx, "fx_policy")
	if err != nil {
		t.Fatalf("ListRuleVersions fx_policy: %v", err)
	}
	if len(fxVersions) != 2 || fxVersions[0].Version != 1 || fxVersions[1].Version != 2 {
		t.Fatalf("expected [v1, v2] ascending by version, got %+v", fxVersions)
	}

	allVersions, err := s.ListRuleVersions(ctx, "")
	if err != nil {
		t.Fatalf("ListRuleVersions all: %v", err)
	}
	if len(allVersions) != 3 {
		t.Fatalf("expected 3 versions across all rules, got %d", len(allVersions))
	}
	if allVersions[0].RuleID != "depeg_policy" || allVersions[1].RuleID != "fx_policy" || allVersions[2].RuleID != "fx_policy" {
		t.Fatalf("expected rule_id-ascending grouping (depeg_policy, fx_policy, fx_policy), got %+v", allVersions)
	}
	if allVersions[1].Version != 1 || allVersions[2].Version != 2 {
		t.Fatalf("expected fx_policy versions ascending within the group, got %+v", allVersions)
	}
}

func testRuleApplications(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Run("AutoAssignment", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		auto, err := s.InsertRuleApplication(ctx, store.RuleApplication{
			RuleID: "loan_underwriting", Version: 1, EntityType: "loan", EntityID: "loan-9",
			Inputs: json.RawMessage(`{}`), Outcome: json.RawMessage(`{}`), AppliedAt: base,
		})
		if err != nil {
			t.Fatalf("InsertRuleApplication auto: %v", err)
		}
		if auto.ID == "" {
			t.Fatalf("expected ID to be assigned")
		}
		if auto.RecordedAt.IsZero() {
			t.Fatalf("expected RecordedAt to be assigned")
		}

		// Explicit ID / RecordedAt are preserved, not overwritten.
		explicit := store.RuleApplication{
			ID: "app-explicit", RuleID: "loan_underwriting", Version: 1,
			EntityType: "loan", EntityID: "loan-9",
			Inputs: json.RawMessage(`{}`), Outcome: json.RawMessage(`{}`),
			AppliedAt: base, RecordedAt: base.Add(time.Hour),
		}
		gotExplicit, err := s.InsertRuleApplication(ctx, explicit)
		if err != nil {
			t.Fatalf("InsertRuleApplication explicit: %v", err)
		}
		if gotExplicit.ID != "app-explicit" {
			t.Fatalf("expected explicit ID preserved, got %q", gotExplicit.ID)
		}
		if !gotExplicit.RecordedAt.Equal(base.Add(time.Hour)) {
			t.Fatalf("expected explicit RecordedAt preserved, got %v", gotExplicit.RecordedAt)
		}
	})

	t.Run("FilteringAndOrdering", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		// Build a small, explicitly-ordered set of applications. RecordedAt
		// is set explicitly (rather than left to the store) and increases
		// with insertion order, so "newest first" is unambiguous regardless
		// of how fast the underlying store's wall clock ticks.
		type spec struct {
			ruleID, entityID string
		}
		specs := []spec{
			{"loan_underwriting", "loan-1"}, // a0
			{"loan_underwriting", "loan-2"}, // a1
			{"risk_scoring", "loan-1"},      // a2
			{"loan_underwriting", "loan-1"}, // a3
			{"risk_scoring", "loan-2"},      // a4
		}
		apps := make([]store.RuleApplication, len(specs))
		for i, sp := range specs {
			a, err := s.InsertRuleApplication(ctx, store.RuleApplication{
				RuleID: sp.ruleID, Version: 1, EntityType: "loan", EntityID: sp.entityID,
				Inputs:     json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
				Outcome:    json.RawMessage(fmt.Sprintf(`{"o":%d}`, i)),
				AppliedAt:  base.Add(time.Duration(i) * time.Minute),
				RecordedAt: base.Add(time.Duration(i+1) * time.Second),
			})
			if err != nil {
				t.Fatalf("InsertRuleApplication %d: %v", i, err)
			}
			apps[i] = a
		}

		assertOrder := func(t *testing.T, got []store.RuleApplication, wantIdx []int) {
			t.Helper()
			if len(got) != len(wantIdx) {
				t.Fatalf("expected %d results, got %d (%+v)", len(wantIdx), len(got), got)
			}
			for i, w := range wantIdx {
				if got[i].ID != apps[w].ID {
					t.Fatalf("result %d: expected application %d (id=%s), got id=%s", i, w, apps[w].ID, got[i].ID)
				}
			}
		}

		all, err := s.ListRuleApplications(ctx, "", "", 0)
		if err != nil {
			t.Fatalf("ListRuleApplications all: %v", err)
		}
		// newest first: a4, a3, a2, a1, a0.
		assertOrder(t, all, []int{4, 3, 2, 1, 0})

		byRule, err := s.ListRuleApplications(ctx, "loan_underwriting", "", 0)
		if err != nil {
			t.Fatalf("ListRuleApplications by rule: %v", err)
		}
		assertOrder(t, byRule, []int{3, 1, 0})

		byEntity, err := s.ListRuleApplications(ctx, "", "loan-1", 0)
		if err != nil {
			t.Fatalf("ListRuleApplications by entity: %v", err)
		}
		assertOrder(t, byEntity, []int{3, 2, 0})

		byBoth, err := s.ListRuleApplications(ctx, "loan_underwriting", "loan-1", 0)
		if err != nil {
			t.Fatalf("ListRuleApplications by rule+entity: %v", err)
		}
		assertOrder(t, byBoth, []int{3, 0})

		limited, err := s.ListRuleApplications(ctx, "", "", 2)
		if err != nil {
			t.Fatalf("ListRuleApplications limited: %v", err)
		}
		assertOrder(t, limited, []int{4, 3})

		none, err := s.ListRuleApplications(ctx, "no_such_rule", "", 0)
		if err != nil {
			t.Fatalf("ListRuleApplications no match: %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("expected no results for an unknown rule, got %+v", none)
		}
	})

	t.Run("DefaultLimit", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		const n = 105
		var last store.RuleApplication
		for i := 0; i < n; i++ {
			a, err := s.InsertRuleApplication(ctx, store.RuleApplication{
				RuleID: "loan_servicing", Version: 1, EntityType: "loan", EntityID: "loan-1",
				Inputs:     json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
				Outcome:    json.RawMessage(fmt.Sprintf(`{"o":%d}`, i)),
				AppliedAt:  base.Add(time.Duration(i) * time.Minute),
				RecordedAt: base.Add(time.Duration(i) * time.Second),
			})
			if err != nil {
				t.Fatalf("InsertRuleApplication %d: %v", i, err)
			}
			last = a
		}

		got, err := s.ListRuleApplications(ctx, "", "", 0)
		if err != nil {
			t.Fatalf("ListRuleApplications default limit: %v", err)
		}
		if len(got) != 100 {
			t.Fatalf("expected default limit of 100, got %d", len(got))
		}
		if got[0].ID != last.ID {
			t.Fatalf("expected the most recently inserted application first, got %+v", got[0])
		}
	})
}
