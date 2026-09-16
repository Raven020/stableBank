package memstore

import (
	"context"
	"testing"
	"time"

	"github.com/Raven020/stableBank/internal/store"
)

func TestRuleVersionClosesPrevious(t *testing.T) {
	ctx := context.Background()
	s := New()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.InsertRuleVersion(ctx, store.RuleVersion{RuleID: "r", Version: 1, EffectiveFrom: t0}); err != nil {
		t.Fatal(err)
	}
	t1 := t0.AddDate(0, 0, 5)
	if err := s.InsertRuleVersion(ctx, store.RuleVersion{RuleID: "r", Version: 2, EffectiveFrom: t1}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRuleVersion(ctx, store.RuleVersion{RuleID: "r", Version: 2}); err != store.ErrConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	vs, _ := s.ListRuleVersions(ctx, "r")
	if len(vs) != 2 || vs[0].EffectiveTo == nil || !vs[0].EffectiveTo.Equal(t1) || vs[1].EffectiveTo != nil {
		t.Fatalf("unexpected versions: %+v", vs)
	}
}

func TestEventsSequenced(t *testing.T) {
	ctx := context.Background()
	s := New()
	out, err := s.Append(ctx, store.Event{Type: "a", AggregateID: "x"}, store.Event{Type: "b", AggregateID: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Seq != 1 || out[1].Seq != 2 || out[0].ID == "" {
		t.Fatalf("bad seq/id: %+v", out)
	}
	got, _ := s.List(ctx, store.EventFilter{Types: []string{"b"}})
	if len(got) != 1 || got[0].AggregateID != "y" {
		t.Fatalf("filter failed: %+v", got)
	}
}
