package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// GetEventsInTx retrieves events for an issue. If limit <= 0, all events are returned.
//
//nolint:gosec // G201: table is hardcoded via WispTableRouting
func GetEventsInTx(ctx context.Context, tx *sql.Tx, issueID string, limit int) ([]*types.Event, error) {
	_, _, eventTable, _ := WispTableRouting(IsActiveWispInTx(ctx, tx, issueID))

	query := fmt.Sprintf(`
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at DESC
	`, eventTable)
	args := []interface{}{issueID}

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// GetAllEventsSinceInTx returns all events created after the given time,
// querying both events and wisp_events tables.
func GetAllEventsSinceInTx(ctx context.Context, tx *sql.Tx, since time.Time) ([]*types.Event, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE created_at > ?
		UNION ALL
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events
		WHERE created_at > ?
		ORDER BY created_at ASC
	`, since, since)
	if err != nil {
		return nil, fmt.Errorf("get events since %v: %w", since, err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// Event keyset-read bounds. The API's change feed pages the durable events
// table with a bounded limit (default when the caller passes <= 0, hard cap
// otherwise).
const (
	defaultEventsSinceLimit = 100
	maxEventsSinceLimit     = 500
)

// EventsSinceInTx returns durable events strictly after the (createdAt, id)
// keyset cursor, ordered by (created_at ASC, id ASC) and bounded by limit.
//
// The cursor predicate `(created_at > ?) OR (created_at = ? AND id > ?)`
// excludes the cursor row itself and orders same-second ties by id, so a
// caller can page forward by feeding the last returned event's CreatedAt/ID
// back in. The zero cursor (zero time, empty id) starts from the epoch.
//
// Scope is the durable `events` table only — wisp_events are deliberately not
// unioned in, unlike GetAllEventsSince, so the feed stays durable-only.
func EventsSinceInTx(ctx context.Context, tx *sql.Tx, cursorCreatedAt time.Time, cursorID string, limit int) ([]*types.Event, error) {
	if limit <= 0 {
		limit = defaultEventsSinceLimit
	}
	if limit > maxEventsSinceLimit {
		limit = maxEventsSinceLimit
	}

	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE (created_at > ?) OR (created_at = ? AND id > ?)
		ORDER BY created_at ASC, id ASC
		LIMIT %d
	`, limit), cursorCreatedAt, cursorCreatedAt, cursorID)
	if err != nil {
		return nil, fmt.Errorf("events since cursor (%v, %q): %w", cursorCreatedAt, cursorID, err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]*types.Event, error) {
	var events []*types.Event
	for rows.Next() {
		var event types.Event
		var oldValue, newValue, comment sql.NullString
		if err := rows.Scan(&event.ID, &event.IssueID, &event.EventType, &event.Actor,
			&oldValue, &newValue, &comment, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if oldValue.Valid {
			event.OldValue = &oldValue.String
		}
		if newValue.Valid {
			event.NewValue = &newValue.String
		}
		if comment.Valid {
			event.Comment = &comment.String
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}
