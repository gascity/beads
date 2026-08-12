package beads

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
	"github.com/steveyegge/beads/internal/storage/pgdialect"
	"github.com/steveyegge/beads/internal/types"
)

// pgTestURL returns BEADS_PG_TEST_URL or self-skips, matching the convention in
// internal/storage/postgres/conformance_test.go.
func pgTestURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping live Postgres test")
	}
	return url
}

var openPGSchemaSeq int64

func uniquePGSchema(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), atomic.AddInt64(&openPGSchemaSeq, 1))
}

// withRawPG runs fn against a raw (non-translating) connection to url with
// search_path set to schema, mirroring how the postgres package itself performs
// out-of-band DDL/metadata SQL (the translating store handle would mangle it).
func withRawPG(t *testing.T, url, schema string, fn func(db *sql.DB)) {
	t.Helper()
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	fn(raw)
}

func dropPGSchema(url, schema string) {
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		return
	}
	defer func() { _ = raw.Close() }()
	_, _ = raw.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
}

func TestOpenServerPostgres(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		// Empty DSN / empty Schema must error before any dial — no server needed.
		if _, err := OpenServerPostgres(context.Background(), PostgresServerConfig{DSN: "", Schema: "s"}); err == nil {
			t.Fatal("expected error for empty DSN, got nil")
		} else if !strings.Contains(err.Error(), "DSN") {
			t.Fatalf("empty-DSN error = %q, want it to mention DSN", err)
		}
		if _, err := OpenServerPostgres(context.Background(), PostgresServerConfig{DSN: "postgres://x", Schema: ""}); err == nil {
			t.Fatal("expected error for empty Schema, got nil")
		} else if !strings.Contains(err.Error(), "Schema") {
			t.Fatalf("empty-Schema error = %q, want it to mention Schema", err)
		}
	})

	t.Run("smoke_lifecycle", func(t *testing.T) {
		url := pgTestURL(t)
		ctx := context.Background()
		schema := uniquePGSchema("open_smoke")
		t.Cleanup(func() { dropPGSchema(url, schema) })

		// First open provisions a fresh schema.
		s1, err := OpenServerPostgres(ctx, PostgresServerConfig{DSN: url, Schema: schema})
		if err != nil {
			t.Fatalf("OpenServerPostgres (fresh): %v", err)
		}
		st1 := s1.(storage.DoltStorage)
		if err := st1.SetConfig(ctx, "issue_prefix", "open"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		iss := &types.Issue{
			ID: "open-1", Title: "hello", IssueType: types.IssueType("task"),
			Status: types.Status("open"), Priority: 2, CreatedBy: "tester",
		}
		if err := st1.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		got, err := st1.GetIssue(ctx, "open-1")
		if err != nil || got == nil {
			t.Fatalf("GetIssue: %v (got=%v)", err, got)
		}
		if got.Title != "hello" {
			t.Fatalf("GetIssue title = %q, want %q", got.Title, "hello")
		}
		if err := st1.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Reopening the SAME schema is idempotent (InitSchema re-runs) and the
		// issue persists.
		s2, err := OpenServerPostgres(ctx, PostgresServerConfig{DSN: url, Schema: schema})
		if err != nil {
			t.Fatalf("OpenServerPostgres (reopen): %v", err)
		}
		defer func() { _ = s2.Close() }()
		st2 := s2.(storage.DoltStorage)
		got2, err := st2.GetIssue(ctx, "open-1")
		if err != nil || got2 == nil {
			t.Fatalf("GetIssue after reopen: %v (got=%v)", err, got2)
		}
		if got2.Title != "hello" {
			t.Fatalf("persisted title = %q, want %q", got2.Title, "hello")
		}

		// The pg_schema_version stamp exists in the workspace metadata table.
		withRawPG(t, url, schema, func(db *sql.DB) {
			var stamp string
			q := fmt.Sprintf(`SELECT value FROM %q.metadata WHERE key = 'pg_schema_version'`, schema)
			if err := db.QueryRowContext(ctx, q).Scan(&stamp); err != nil {
				t.Fatalf("read pg_schema_version stamp: %v", err)
			}
			if stamp == "" {
				t.Fatal("pg_schema_version stamp is empty")
			}
		})
	})

	t.Run("version_fence", func(t *testing.T) {
		url := pgTestURL(t)
		ctx := context.Background()
		schema := uniquePGSchema("open_fence")
		t.Cleanup(func() { dropPGSchema(url, schema) })

		s1, err := OpenServerPostgres(ctx, PostgresServerConfig{DSN: url, Schema: schema})
		if err != nil {
			t.Fatalf("OpenServerPostgres (fresh): %v", err)
		}
		if err := s1.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Doctor the stamp to a version this binary does not accept.
		withRawPG(t, url, schema, func(db *sql.DB) {
			q := fmt.Sprintf(`UPDATE %q.metadata SET value = '999' WHERE key = 'pg_schema_version'`, schema)
			res, err := db.ExecContext(ctx, q)
			if err != nil {
				t.Fatalf("doctor stamp: %v", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				t.Fatalf("doctor stamp affected %d rows, want 1", n)
			}
		})

		// Reopen must fail closed on the version mismatch (no migrator in the wedge).
		s2, err := OpenServerPostgres(ctx, PostgresServerConfig{DSN: url, Schema: schema})
		if err == nil {
			_ = s2.Close()
			t.Fatal("expected reopen to fail on schema-version mismatch, got nil error")
		}
		if !strings.Contains(err.Error(), "version") {
			t.Fatalf("version-fence error = %q, want it to mention version", err)
		}
	})
}

func TestOpenExistingServerPostgresValidation(t *testing.T) {
	for _, cfg := range []PostgresServerConfig{{Schema: "workspace"}, {DSN: "postgres://x"}} {
		if _, err := OpenExistingServerPostgres(context.Background(), cfg); err == nil {
			t.Fatalf("OpenExistingServerPostgres(%+v) succeeded", cfg)
		}
	}
}

// TestOpenServerPostgresEventsJournalIsProjectScoped proves Hosted enables the
// durable journal on the explicitly selected project only. Opening another
// project in the same process must not inherit that activation.
func TestOpenServerPostgresEventsJournalIsProjectScoped(t *testing.T) {
	url := pgTestURL(t)
	ctx := context.Background()
	enabledSchema := uniquePGSchema("open_journal_enabled")
	disabledSchema := uniquePGSchema("open_journal_disabled")
	t.Cleanup(func() {
		dropPGSchema(url, enabledSchema)
		dropPGSchema(url, disabledSchema)
	})

	enabled, err := OpenServerPostgres(ctx, PostgresServerConfig{
		DSN: url, Schema: enabledSchema, EventsJournal: true,
	})
	if err != nil {
		t.Fatalf("OpenServerPostgres(enabled): %v", err)
	}
	defer func() { _ = enabled.Close() }()
	disabled, err := OpenServerPostgres(ctx, PostgresServerConfig{DSN: url, Schema: disabledSchema})
	if err != nil {
		t.Fatalf("OpenServerPostgres(disabled): %v", err)
	}
	defer func() { _ = disabled.Close() }()

	enabledJournal, ok := enabled.(EventsJournalAccessor)
	if !ok {
		t.Fatal("OpenServerPostgres result does not expose the public events-journal capability")
	}
	disabledJournal, ok := disabled.(EventsJournalAccessor)
	if !ok {
		t.Fatal("disabled OpenServerPostgres result does not expose the public events-journal capability")
	}
	enabledStore := enabled.(storage.DoltStorage)
	disabledStore := disabled.(storage.DoltStorage)
	for _, tc := range []struct {
		name  string
		store storage.DoltStorage
	}{
		{"enabled", enabledStore},
		{"disabled", disabledStore},
	} {
		if err := tc.store.SetConfig(ctx, "issue_prefix", tc.name); err != nil {
			t.Fatalf("SetConfig(%s): %v", tc.name, err)
		}
		if err := tc.store.CreateIssue(ctx, &types.Issue{ID: tc.name + "-1", Title: tc.name, IssueType: "task", Status: types.StatusOpen, Priority: 2}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", tc.name, err)
		}
	}

	rows, err := enabledJournal.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("enabled ReadEventsJournal: %v", err)
	}
	if len(rows) != 1 || rows[0].IssueID != "enabled-1" {
		t.Fatalf("enabled journal rows = %#v, want its one project-local mutation", rows)
	}
	rows, err = disabledJournal.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("disabled ReadEventsJournal: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("disabled project inherited journal activation: %#v", rows)
	}
}

// TestConformanceViaOpenServerPostgres runs the backend-agnostic storage
// conformance suite through the *hosted* entrypoint (OpenServerPostgres) rather
// than the internal postgres package the CI already covers, certifying the public
// seam end-to-end. Gated on BEADS_PG_TEST_URL.
func TestConformanceViaOpenServerPostgres(t *testing.T) {
	url := pgTestURL(t)
	conformance.RunAll(t, openServerPGConformanceFactory(url))
}

// openServerPGConformanceFactory mirrors postgres.pgConformanceFactory but mints
// each isolated schema through OpenServerPostgres, seeded with issue_prefix as
// `bd init` leaves it.
func openServerPGConformanceFactory(url string) conformance.Factory {
	base := time.Now().UnixNano()
	var seq int64
	return func(t *testing.T) storage.DoltStorage {
		ctx := context.Background()
		schema := fmt.Sprintf("open_conf_%d_%d", base, atomic.AddInt64(&seq, 1))

		s, err := OpenServerPostgres(ctx, PostgresServerConfig{DSN: url, Schema: schema})
		if err != nil {
			t.Fatalf("OpenServerPostgres: %v", err)
		}
		st := s.(storage.DoltStorage)
		if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("SetConfig(issue_prefix): %v", err)
		}
		t.Cleanup(func() {
			_ = st.Close()
			dropPGSchema(url, schema)
		})
		return st
	}
}
