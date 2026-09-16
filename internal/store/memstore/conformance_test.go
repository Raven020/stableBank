package memstore_test

import (
	"testing"

	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
	"github.com/Raven020/stableBank/internal/store/storetest"
)

// TestConformance runs the shared store.Store conformance suite against
// memstore, the reference implementation every other store.Store
// implementation (pgstore) must match.
func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return memstore.New()
	})
}
