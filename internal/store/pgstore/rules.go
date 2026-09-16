package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Raven020/stableBank/internal/store"
)

// pgUniqueViolation is the SQLSTATE for unique_violation.
const pgUniqueViolation = "23505"

// InsertRuleVersion appends v. If a currently-open version (effective_to IS
// NULL) exists for the same rule_id, its effective_to is set to
// v.EffectiveFrom in the same transaction, then v is inserted. A duplicate
// (rule_id, version) is reported as store.ErrConflict and leaves existing
// rows untouched.
func (s *Store) InsertRuleVersion(ctx context.Context, v store.RuleVersion) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin insert rule version: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(ctx, `
		UPDATE rule_versions
		SET effective_to = $1
		WHERE rule_id = $2 AND effective_to IS NULL
	`, v.EffectiveFrom, v.RuleID); err != nil {
		return fmt.Errorf("pgstore: close previous rule version: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO rule_versions
			(rule_id, version, rule_type, content, content_hash, source_commit,
			 author, change_note, effective_from, effective_to, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, v.RuleID, v.Version, v.RuleType, string(v.Content), v.ContentHash,
		v.SourceCommit, v.Author, v.ChangeNote, v.EffectiveFrom, v.EffectiveTo, v.CreatedAt,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return store.ErrConflict
		}
		return fmt.Errorf("pgstore: insert rule version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: commit insert rule version: %w", err)
	}
	return nil
}

// ListRuleVersions returns all versions for ruleID ascending by Version. An
// empty ruleID returns every version of every rule, grouped by rule_id
// ascending and, within a rule, by version ascending.
func (s *Store) ListRuleVersions(ctx context.Context, ruleID string) ([]store.RuleVersion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rule_id, version, rule_type, content, content_hash, source_commit,
		       author, change_note, effective_from, effective_to, created_at
		FROM rule_versions
		WHERE $1 = '' OR rule_id = $1
		ORDER BY rule_id ASC, version ASC
	`, ruleID)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list rule versions: %w", err)
	}
	defer rows.Close()

	var out []store.RuleVersion
	for rows.Next() {
		v, content, err := scanRuleVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("pgstore: scan rule version: %w", err)
		}
		v.Content = []byte(content)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: list rule versions: %w", err)
	}
	return out, nil
}

// rowScanner is satisfied by both pgx.Rows and pgx.Row.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRuleVersion(row rowScanner) (store.RuleVersion, string, error) {
	var v store.RuleVersion
	var content string
	err := row.Scan(&v.RuleID, &v.Version, &v.RuleType, &content, &v.ContentHash,
		&v.SourceCommit, &v.Author, &v.ChangeNote, &v.EffectiveFrom, &v.EffectiveTo, &v.CreatedAt)
	return v, content, err
}

// GetRuleVersion returns one version or store.ErrNotFound.
func (s *Store) GetRuleVersion(ctx context.Context, ruleID string, version int) (store.RuleVersion, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT rule_id, version, rule_type, content, content_hash, source_commit,
		       author, change_note, effective_from, effective_to, created_at
		FROM rule_versions
		WHERE rule_id = $1 AND version = $2
	`, ruleID, version)

	v, content, err := scanRuleVersion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.RuleVersion{}, store.ErrNotFound
		}
		return store.RuleVersion{}, fmt.Errorf("pgstore: get rule version: %w", err)
	}
	v.Content = []byte(content)
	return v, nil
}

// InsertRuleApplication appends to the audit log, assigning ID/RecordedAt
// when empty.
func (s *Store) InsertRuleApplication(ctx context.Context, a store.RuleApplication) (store.RuleApplication, error) {
	if a.ID == "" {
		a.ID = newID()
	}
	if a.RecordedAt.IsZero() {
		a.RecordedAt = time.Now().UTC()
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO rule_applications
			(id, rule_id, version, entity_type, entity_id, inputs, outcome, applied_at, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, a.ID, a.RuleID, a.Version, a.EntityType, a.EntityID,
		[]byte(a.Inputs), []byte(a.Outcome), a.AppliedAt, a.RecordedAt,
	)
	if err != nil {
		return store.RuleApplication{}, fmt.Errorf("pgstore: insert rule application: %w", err)
	}
	return a, nil
}

// ListRuleApplications filters by ruleID and/or entityID (either may be
// empty), newest first (by recorded_at, the order in which InsertRuleApplication
// wrote them), at most limit rows (0 = 100).
func (s *Store) ListRuleApplications(ctx context.Context, ruleID, entityID string, limit int) ([]store.RuleApplication, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, rule_id, version, entity_type, entity_id, inputs, outcome, applied_at, recorded_at
		FROM rule_applications
		WHERE ($1 = '' OR rule_id = $1)
		  AND ($2 = '' OR entity_id = $2)
		ORDER BY recorded_at DESC, id DESC
		LIMIT $3
	`, ruleID, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list rule applications: %w", err)
	}
	defer rows.Close()

	var out []store.RuleApplication
	for rows.Next() {
		var a store.RuleApplication
		var inputs, outcome []byte
		if err := rows.Scan(&a.ID, &a.RuleID, &a.Version, &a.EntityType, &a.EntityID,
			&inputs, &outcome, &a.AppliedAt, &a.RecordedAt); err != nil {
			return nil, fmt.Errorf("pgstore: scan rule application: %w", err)
		}
		a.Inputs = json.RawMessage(inputs)
		a.Outcome = json.RawMessage(outcome)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: list rule applications: %w", err)
	}
	return out, nil
}
