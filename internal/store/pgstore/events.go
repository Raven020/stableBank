package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Raven020/stableBank/internal/store"
)

// Append persists events in order, assigning Seq (via the events.seq
// BIGSERIAL) and ID/RecordedAt when empty, all in a single transaction.
func (s *Store) Append(ctx context.Context, events ...store.Event) ([]store.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgstore: begin append: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	out := make([]store.Event, len(events))
	for i, e := range events {
		if e.ID == "" {
			e.ID = newID()
		}
		if e.RecordedAt.IsZero() {
			e.RecordedAt = time.Now().UTC()
		}

		row := tx.QueryRow(ctx, `
			INSERT INTO events (id, type, aggregate_id, occurred_at, recorded_at, payload)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING seq, recorded_at
		`, e.ID, e.Type, e.AggregateID, e.OccurredAt, e.RecordedAt, []byte(e.Payload))
		if err := row.Scan(&e.Seq, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("pgstore: insert event: %w", err)
		}
		out[i] = e
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("pgstore: commit append: %w", err)
	}
	return out, nil
}

// List returns events matching filter ordered by Seq ascending.
func (s *Store) List(ctx context.Context, f store.EventFilter) ([]store.Event, error) {
	q := `
		SELECT seq, id, type, aggregate_id, occurred_at, recorded_at, payload
		FROM events
		WHERE seq > $1
	`
	args := []any{f.AfterSeq}

	if f.AggregateID != "" {
		args = append(args, f.AggregateID)
		q += fmt.Sprintf(" AND aggregate_id = $%d", len(args))
	}
	if len(f.Types) > 0 {
		args = append(args, f.Types)
		q += fmt.Sprintf(" AND type = ANY($%d)", len(args))
	}
	q += " ORDER BY seq ASC"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list events: %w", err)
	}
	defer rows.Close()

	var out []store.Event
	for rows.Next() {
		var e store.Event
		var payload []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.AggregateID, &e.OccurredAt, &e.RecordedAt, &payload); err != nil {
			return nil, fmt.Errorf("pgstore: scan event: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: list events: %w", err)
	}
	return out, nil
}
