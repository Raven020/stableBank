package simclock

import (
	"testing"
	"time"
)

func TestAdvanceForwardOnly(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := New(start)
	var got []time.Time
	c.Subscribe(func(prev, next time.Time) { got = append(got, prev, next) })

	now, err := c.AdvanceDays(3)
	if err != nil {
		t.Fatal(err)
	}
	if want := start.AddDate(0, 0, 3); !now.Equal(want) {
		t.Fatalf("got %v want %v", now, want)
	}
	if len(got) != 2 || !got[0].Equal(start) || !got[1].Equal(now) {
		t.Fatalf("listener not notified correctly: %v", got)
	}
	if _, err := c.SetTo(start); err == nil {
		t.Fatal("expected rewind to be rejected")
	}
	if _, err := c.AdvanceDays(0); err == nil {
		t.Fatal("expected zero advance to be rejected")
	}
}
