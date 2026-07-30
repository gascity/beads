//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *EmbeddedDoltStore) runIssueOperationTx(ctx context.Context, commitMsg string, fn func(*sql.Tx) (storageissueops.ChangedTables, error)) error {
	return s.runTransaction(ctx, commitMsg, func(tx *embeddedTransaction) error {
		tables, err := fn(tx.tx)
		if err != nil {
			return err
		}
		for table := range tables {
			tx.dirty.MarkDirty(table)
		}
		return nil
	})
}
