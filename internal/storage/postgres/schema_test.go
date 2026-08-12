package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

// TestInitSchema applies the embedded DDL into a throwaway schema on a live
// Postgres and asserts the core tables materialize. It is gated on
// BEADS_PG_TEST_URL (a pgx-parseable DSN) and skips otherwise.
func TestInitSchema(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping Postgres schema test")
	}

	schema := fmt.Sprintf("bd_schema_test_%d", time.Now().UnixNano())

	// DDL runs over a RAW (non-translating) DB; assertions below use ? bindings
	// and so need the translating DB.
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("openraw: %v", err)
	}
	db, err := pgdialect.Open(url, schema)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Registered first so they run last (cleanups are LIFO): the schema drop
	// below still has open connections when it runs.
	t.Cleanup(func() { _ = raw.Close() })
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()

	if err := InitSchema(ctx, raw, schema); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop schema %q: %v", schema, err)
		}
	})

	for _, table := range []string{"issues", "dependencies", "bd_events_journal", "bd_events_seq"} {
		if !tableExists(ctx, t, db, schema, table) {
			t.Errorf("expected table %q to exist in schema %q", table, schema)
		}
	}
}

// TestInitSchemaAddsJournalToExistingVersionOneSchema models a Hosted project
// provisioned by the pre-journal binary. PostgreSQL has no migrator in this
// integration line, so reopening the same schema version must apply the new
// additive DDL without requiring recreation or changing the version stamp.
func TestInitSchemaAddsJournalToExistingVersionOneSchema(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping Postgres schema upgrade test")
	}

	schema := fmt.Sprintf("bd_schema_journal_upgrade_%d", time.Now().UnixNano())
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("openraw: %v", err)
	}
	db, err := pgdialect.Open(url, schema)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := InitSchema(ctx, raw, schema); err != nil {
		t.Fatalf("initial InitSchema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop schema %q: %v", schema, err)
		}
	})
	if _, err := raw.ExecContext(ctx, "DROP TABLE bd_events_journal, bd_events_seq"); err != nil {
		t.Fatalf("model pre-journal schema: %v", err)
	}

	var before string
	if err := raw.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'pg_schema_version'`).Scan(&before); err != nil {
		t.Fatalf("read pre-upgrade schema version: %v", err)
	}
	if err := InitSchema(ctx, raw, schema); err != nil {
		t.Fatalf("upgrade InitSchema: %v", err)
	}
	for _, table := range []string{"bd_events_journal", "bd_events_seq"} {
		if !tableExists(ctx, t, db, schema, table) {
			t.Errorf("upgrade did not restore table %q", table)
		}
	}
	var after string
	if err := raw.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'pg_schema_version'`).Scan(&after); err != nil {
		t.Fatalf("read post-upgrade schema version: %v", err)
	}
	if before != schemaVersion || after != schemaVersion {
		t.Fatalf("schema version changed across additive journal upgrade: before=%q after=%q want=%q", before, after, schemaVersion)
	}
}

// TestInitSchemaAddsCommentPayloadToExistingJournal models the intermediate
// Hosted schema created by the contributor patch: the journal table exists,
// but predates the additive comment_json payload column. CREATE TABLE IF NOT
// EXISTS cannot repair that shape, so every open must apply an idempotent ALTER.
func TestInitSchemaAddsCommentPayloadToExistingJournal(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping Postgres journal-column upgrade test")
	}

	schema := fmt.Sprintf("bd_schema_journal_column_upgrade_%d", time.Now().UnixNano())
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("openraw: %v", err)
	}
	db, err := pgdialect.Open(url, schema)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := InitSchema(ctx, raw, schema); err != nil {
		t.Fatalf("initial InitSchema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop schema %q: %v", schema, err)
		}
	})
	if _, err := raw.ExecContext(ctx, "ALTER TABLE bd_events_journal DROP COLUMN comment_json"); err != nil {
		t.Fatalf("model pre-comment journal: %v", err)
	}
	if columnExists(ctx, t, db, schema, "bd_events_journal", "comment_json") {
		t.Fatal("pre-upgrade journal unexpectedly has comment_json")
	}

	if err := InitSchema(ctx, raw, schema); err != nil {
		t.Fatalf("upgrade InitSchema: %v", err)
	}
	if !columnExists(ctx, t, db, schema, "bd_events_journal", "comment_json") {
		t.Fatal("upgrade did not add bd_events_journal.comment_json")
	}
}

func tableExists(ctx context.Context, t *testing.T, db *sql.DB, schema, table string) bool {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?`,
		schema, table).Scan(&count)
	if err != nil {
		t.Fatalf("query information_schema.tables for %q: %v", table, err)
	}
	return count > 0
}

func columnExists(ctx context.Context, t *testing.T, db *sql.DB, schema, table, column string) bool {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = ? AND table_name = ? AND column_name = ?`,
		schema, table, column).Scan(&count)
	if err != nil {
		t.Fatalf("query information_schema.columns for %q.%q: %v", table, column, err)
	}
	return count > 0
}
