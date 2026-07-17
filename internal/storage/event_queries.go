package storage

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// EventCursor is a keyset position in the durable events stream, ordered by
// (created_at, id). The zero value (zero time, empty id) means "from the
// beginning".
type EventCursor struct {
	CreatedAt time.Time
	ID        string
}

// EventQueryStore provides keyset paging over the durable event log, beyond
// the base Storage interface's time-only GetAllEventsSince. Callers that need
// it type-assert to this interface.
type EventQueryStore interface {
	// EventsSince returns durable events strictly after cursor, ordered by
	// (created_at ASC, id ASC) and bounded by limit (0 = a store default,
	// capped). It reads the durable events table only — wisp events are not
	// included — so a change feed built on it stays durable-only.
	EventsSince(ctx context.Context, cursor EventCursor, limit int) ([]*types.Event, error)
}
