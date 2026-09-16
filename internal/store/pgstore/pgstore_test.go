package pgstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/pgstore"
	"github.com/Raven020/stableBank/internal/store/storetest"
	"github.com/Raven020/stableBank/migrations"
)

// TestConformance runs the shared store.Store conformance suite against a
// real Postgres database. Postgres is not available in this development
// environment, so the test skips itself unless TEST_DATABASE_URL is set.
//
// To run it:
//
//	docker compose up -d
//	export TEST_DATABASE_URL="postgres://stablebank:stablebank@localhost:5432/stablebank?sslmode=disable"
//	go test ./internal/store/pgstore/...
func TestConformance(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping pgstore conformance test")
	}

	storetest.Run(t, func(t *testing.T) store.Store {
		ctx := context.Background()

		s, err := pgstore.Open(ctx, databaseURL)
		if err != nil {
			t.Fatalf("pgstore.Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })

		if err := s.Migrate(ctx, migrations.FS); err != nil {
			t.Fatalf("Migrate: %v", err)
		}

		// The database is shared across subtests, so truncate everything
		// each time a fresh store is requested to give every subtest an
		// empty starting point, matching memstore.New().
		if err := s.Truncate(ctx); err != nil {
			t.Fatalf("truncate: %v", err)
		}

		return s
	})
}
