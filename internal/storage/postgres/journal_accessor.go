package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Postgres implements the journal capability directly, rather than exposing a
// raw database to callers. This keeps the stable bd events contract at the
// storage boundary and gives reads/prunes the same transaction behavior as
// other backends.
var _ storage.EventsJournalAccessor = (*Store)(nil)

// ReadEventsJournal returns committed journal rows after since in sequence
// order. The SQL-family pool uses the Postgres translating dialect, so the
// shared query remains the source of the wire-format behavior.
func (s *Store) ReadEventsJournal(ctx context.Context, since int64, limit int) ([]storage.EventsJournalRow, error) {
	tx, err := s.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("postgres events journal: begin read transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return issueops.ReadEventsInTx(ctx, tx, since, limit)
}

// PruneEventsJournal removes only rows permitted by the shared retention-floor
// calculation. The counter is intentionally never reset by pruning.
func (s *Store) PruneEventsJournal(ctx context.Context, before int64, retainDays, retainRows int) (int64, error) {
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("postgres events journal: begin prune transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	n, err := issueops.PruneEventsInTx(ctx, tx, before, retainDays, retainRows, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("postgres events journal: commit prune: %w", err)
	}
	return n, nil
}
