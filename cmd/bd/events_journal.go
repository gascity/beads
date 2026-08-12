package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// validateEventsJournalBackend rejects activation on SQL-family backends that
// have not implemented the journal contract. Disabled configurations remain
// accepted so existing MySQL and SQLite workspaces continue to open normally.
func validateEventsJournalBackend(backend string, enabled bool) error {
	if !enabled {
		return nil
	}
	switch backend {
	case "", configfile.BackendDolt, configfile.BackendPostgres:
		return nil
	default:
		return fmt.Errorf("events journal is not supported by the %s backend", backend)
	}
}

func eventsJournalEnabled() bool {
	return config.GetBool("events-journal")
}

// withConfiguredEventsJournal scopes proxied UOW operations to this command's
// selected workspace. The value follows rootCtx into every repository call and
// cannot affect another store or project in the process.
func withConfiguredEventsJournal(ctx context.Context, enabled bool) context.Context {
	return issueops.WithEventsJournal(ctx, enabled)
}

// configureEventsJournalStore applies activation to one concrete direct store.
// Enabled backends fail closed if they do not expose the capability.
func configureEventsJournalStore(s storage.DoltStorage, enabled bool) error {
	configurer, ok := storage.UnwrapStore(s).(storage.EventsJournalConfigurer)
	if !ok {
		if enabled {
			return fmt.Errorf("storage backend does not support the events journal")
		}
		return nil
	}
	configurer.SetEventsJournalEnabled(enabled)
	return nil
}
