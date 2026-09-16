// Package pgstore is the Postgres implementation of store.Store, backed by
// github.com/jackc/pgx/v5/pgxpool. It mirrors internal/store/memstore
// semantics exactly (see internal/store/store.go for the contract and
// internal/store/storetest for the shared conformance suite both
// implementations must pass).
//
// # Running against a real Postgres
//
// This machine does not have Postgres available, so pgstore's own tests
// (internal/store/pgstore/pgstore_test.go) skip themselves unless the
// TEST_DATABASE_URL environment variable is set. To exercise them for real:
//
//	docker compose up -d                                        # starts the "postgres" service
//	                                                             # from docker-compose.yml (user/password/db
//	                                                             # are all "stablebank")
//	export TEST_DATABASE_URL="postgres://stablebank:stablebank@localhost:5432/stablebank?sslmode=disable"
//	go test ./internal/store/pgstore/...
//
// The binary (cmd/stablebank/main.go, owned by the integrator) is expected to
// open the same way at runtime via:
//
//	DATABASE_URL="postgres://stablebank:stablebank@localhost:5432/stablebank?sslmode=disable"
//	store, err := pgstore.Open(ctx, os.Getenv("DATABASE_URL"))
//	err = store.Migrate(ctx, migrations.FS)
package pgstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a store.Store backed by a Postgres connection pool. It is safe
// for concurrent use (pgxpool.Pool is).
type Store struct {
	pool *pgxpool.Pool
}

// Open creates a connection pool for databaseURL and verifies connectivity
// with a ping.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgstore: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close closes the underlying connection pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// Truncate wipes every table pgstore owns. It is not part of store.Store; it
// exists only so pgstore_test.go can reset a shared database to an empty
// state between conformance subtests, the way memstore.New() does implicitly
// by returning a fresh instance.
func (s *Store) Truncate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE TABLE rule_applications, rule_versions, events`)
	if err != nil {
		return fmt.Errorf("pgstore: truncate: %w", err)
	}
	return nil
}

// Migrate applies every *.sql file in migrationsFS, in lexical filename
// order, that is not yet recorded in the schema_migrations table. Each
// migration file runs in its own transaction; the file's statements
// (split on ';') are executed in order and the migration is recorded in
// schema_migrations before the transaction commits, so a failed migration
// never gets partially recorded.
//
// schema_migrations itself is created here (idempotently) rather than via a
// migration file, since the migrator needs it to exist before it can decide
// what else to apply.
func (s *Store) Migrate(ctx context.Context, migrationsFS fs.FS) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("pgstore: ensure schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return fmt.Errorf("pgstore: read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("pgstore: check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		raw, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return fmt.Errorf("pgstore: read migration %s: %w", name, err)
		}

		if err := s.applyMigration(ctx, name, string(raw)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name, sqlText string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin migration %s: %w", name, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	for _, stmt := range splitStatements(sqlText) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("pgstore: apply migration %s: %w", name, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("pgstore: record migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: commit migration %s: %w", name, err)
	}
	return nil
}

// splitStatements splits a migration file's SQL text into individual
// statements on ';'. This is deliberately simple (the migration files in
// this repo contain only plain DDL: CREATE TABLE/INDEX and COMMENT ON,
// none of which contain semicolons inside string literals), which lets us
// execute each statement with pgx's normal extended-protocol Exec instead
// of needing simple-protocol multi-statement support.
func splitStatements(sqlText string) []string {
	parts := strings.Split(sqlText, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// newID returns a random 16-byte hex identifier, matching memstore.NewID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
