// Package simclock provides the single shared notion of "now" for the whole
// system. Every time-dependent decision (loan due dates, ledger timestamps,
// rule effective_from checks) must read from a Clock, never from time.Now()
// directly. The simulation endpoints advance the SimClock so that a demo
// operator can fast-forward the platform without waiting real time.
package simclock

import (
	"errors"
	"sync"
	"time"
)

// Clock is the read-only view injected into domain services.
type Clock interface {
	Now() time.Time
}

// Listener is notified after the clock advances. prev < next always.
type Listener func(prev, next time.Time)

// SimClock is a forward-only simulated clock. It is safe for concurrent use.
type SimClock struct {
	mu        sync.RWMutex
	now       time.Time
	listeners []Listener
}

// New returns a SimClock starting at start (truncated to the second, UTC).
func New(start time.Time) *SimClock {
	return &SimClock{now: start.UTC().Truncate(time.Second)}
}

// Now returns the current simulated time.
func (c *SimClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance moves the clock forward by d and notifies listeners. Negative or
// zero durations are rejected: the simulated clock never rewinds.
func (c *SimClock) Advance(d time.Duration) (time.Time, error) {
	if d <= 0 {
		return c.Now(), errors.New("simclock: advance duration must be positive")
	}
	return c.SetTo(c.Now().Add(d))
}

// AdvanceDays moves the clock forward by n whole days.
func (c *SimClock) AdvanceDays(n int) (time.Time, error) {
	if n <= 0 {
		return c.Now(), errors.New("simclock: days must be positive")
	}
	return c.Advance(time.Duration(n) * 24 * time.Hour)
}

// SetTo jumps the clock to t, which must be strictly after the current time.
func (c *SimClock) SetTo(t time.Time) (time.Time, error) {
	c.mu.Lock()
	prev := c.now
	t = t.UTC()
	if !t.After(prev) {
		c.mu.Unlock()
		return prev, errors.New("simclock: target time must be after current simulated time")
	}
	c.now = t
	listeners := append([]Listener(nil), c.listeners...)
	c.mu.Unlock()
	for _, l := range listeners {
		l(prev, t)
	}
	return t, nil
}

// Subscribe registers a listener invoked after every advance.
func (c *SimClock) Subscribe(l Listener) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listeners = append(c.listeners, l)
}

// Fixed is a Clock that always returns the same instant; handy in tests.
type Fixed time.Time

// Now implements Clock.
func (f Fixed) Now() time.Time { return time.Time(f) }
