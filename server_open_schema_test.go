package beads_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// adminDB dials the shared test server as root, the way a hosting service's
// provisioner would, so a test can observe DDL out of band from the store.
func adminDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/?timeout=5s", testServerPort))
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping admin connection: %v", err)
	}
	return db
}

func createEmptyDatabase(t *testing.T, admin *sql.DB, name string) {
	t.Helper()
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE `%s`", name)); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
	})
}

func tableCount(t *testing.T, admin *sql.DB, database string) int {
	t.Helper()
	var n int
	if err := admin.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", database).Scan(&n); err != nil {
		t.Fatalf("count tables in %s: %v", database, err)
	}
	return n
}

func schemaVersion(t *testing.T, admin *sql.DB, database string) int {
	t.Helper()
	var v int
	if err := admin.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT COALESCE(MAX(version), 0) FROM `%s`.schema_migrations", database)).Scan(&v); err != nil {
		t.Fatalf("read schema version of %s: %v", database, err)
	}
	return v
}

func serverConfig(database string, ownership beads.SchemaOwnership) beads.ServerConfig {
	return beads.ServerConfig{
		Host:            "127.0.0.1",
		Port:            testServerPort,
		User:            "root",
		Database:        database,
		SchemaOwnership: ownership,
	}
}

// TestOpenServerSchemaOwnedElsewhereIssuesNoDDL is the regression pin for the
// hosted read path: an embedder that does not own a database's schema must not
// migrate it, and an open that finds no schema at all must say so rather than
// build one.
func TestOpenServerSchemaOwnedElsewhereIssuesNoDDL(t *testing.T) {
	skipIfNoDoltServer(t)
	admin := adminDB(t)
	ctx := context.Background()

	const database = "bd_schema_owned_elsewhere"
	createEmptyDatabase(t, admin, database)

	store, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedElsewhere))
	if err == nil {
		_ = store.Close()
	}

	if n := tableCount(t, admin, database); n != 0 {
		t.Fatalf("SchemaOwnedElsewhere open created %d tables in %s; it may issue no DDL", n, database)
	}
	if err == nil {
		t.Fatal("SchemaOwnedElsewhere open of an unprovisioned database returned a store; there is nothing to read and it may not create one")
	}
	if !errors.Is(err, beads.ErrNoSchema) {
		t.Fatalf("error = %v, want beads.ErrNoSchema", err)
	}
	if !errors.Is(err, dolt.ErrNoSchema) {
		t.Fatalf("beads.ErrNoSchema does not identify the error the dolt store returned; the re-export has drifted")
	}
}

// TestOpenServerSchemaOwnedHereProvisions is the control that must fail
// differently: the owning role still applies the schema, so the assertion above
// is about which role does DDL, not about the fixture failing to observe any.
func TestOpenServerSchemaOwnedHereProvisions(t *testing.T) {
	skipIfNoDoltServer(t)
	admin := adminDB(t)
	ctx := context.Background()

	const database = "bd_schema_owned_here"
	createEmptyDatabase(t, admin, database)

	store, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedHere))
	if err != nil {
		t.Fatalf("SchemaOwnedHere open: %v", err)
	}
	defer store.Close()

	if n := tableCount(t, admin, database); n == 0 {
		t.Fatalf("SchemaOwnedHere open left %s empty; the owning role provisions the schema", database)
	}
	if v := schemaVersion(t, admin, database); v == 0 {
		t.Fatalf("SchemaOwnedHere open left %s at schema version 0", database)
	}
}

// TestOpenServerSchemaOwnedElsewhereReadsAndWritesProvisionedRows pins the half
// of the contract that rules ReadOnly out: the hosted service serves board
// reads and issue mutations through one pooled store, so this role must write
// rows freely while writing no DDL.
func TestOpenServerSchemaOwnedElsewhereReadsAndWritesProvisionedRows(t *testing.T) {
	skipIfNoDoltServer(t)
	admin := adminDB(t)
	ctx := context.Background()

	const database = "bd_schema_owned_elsewhere_provisioned"
	createEmptyDatabase(t, admin, database)

	owner, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedHere))
	if err != nil {
		t.Fatalf("provisioning open: %v", err)
	}
	if err := owner.SetConfig(ctx, "issue_prefix", "t"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close provisioning store: %v", err)
	}
	before := schemaVersion(t, admin, database)

	store, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedElsewhere))
	if err != nil {
		t.Fatalf("SchemaOwnedElsewhere open of a provisioned database: %v", err)
	}
	defer store.Close()

	if after := schemaVersion(t, admin, database); after != before {
		t.Fatalf("SchemaOwnedElsewhere open moved %s from v%d to v%d", database, before, after)
	}

	issue := &beads.Issue{Title: "served write", IssueType: beads.TypeTask, Priority: 1, Status: beads.StatusOpen}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue through a SchemaOwnedElsewhere store: %v", err)
	}
	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue through a SchemaOwnedElsewhere store: %v", err)
	}
	if got.Title != issue.Title {
		t.Fatalf("round-trip title = %q, want %q", got.Title, issue.Title)
	}
}

// stampSchemaVersion rewrites a database's recorded schema version so a test can
// present a store with a version it did not migrate to. It changes the marker,
// not the tables, which is exactly the skew a deployment shows when a database
// is parked on an older bd than the binary reading it.
func stampSchemaVersion(t *testing.T, admin *sql.DB, database string, version int) {
	t.Helper()
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("DELETE FROM `%s`.schema_migrations WHERE version > ?", database), version); err != nil {
		t.Fatalf("stamp %s down to v%d: %v", database, version, err)
	}
	if _, err := admin.ExecContext(ctx,
		fmt.Sprintf("INSERT IGNORE INTO `%s`.schema_migrations (version) VALUES (?)", database), version); err != nil {
		t.Fatalf("stamp %s at v%d: %v", database, version, err)
	}
	if got := schemaVersion(t, admin, database); got != version {
		t.Fatalf("stamped %s to v%d, reads back v%d", database, version, got)
	}
}

// TestOpenServerSchemaOwnedElsewhereSkew pins the decision this role had to make
// about a version it may not change, in both directions.
//
// Behind is tolerated on purpose. A deployment holds databases deliberately
// parked below the newest bd — a writer pinned to an older version, or one still
// serving older clients — and those are precisely the ones a hosted reader must
// keep serving. Refusing would make exactly the parked databases unreadable, and
// migrating them is the harm this role exists to prevent, so the open proceeds.
//
// Ahead still refuses, unchanged: there the binary genuinely cannot know the
// shape it is querying.
func TestOpenServerSchemaOwnedElsewhereSkew(t *testing.T) {
	skipIfNoDoltServer(t)
	admin := adminDB(t)
	ctx := context.Background()

	const database = "bd_schema_owned_elsewhere_skew"
	createEmptyDatabase(t, admin, database)

	owner, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedHere))
	if err != nil {
		t.Fatalf("provisioning open: %v", err)
	}
	if err := owner.SetConfig(ctx, "issue_prefix", "t"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	provisioned := schemaVersion(t, admin, database)
	if err := owner.Close(); err != nil {
		t.Fatalf("close provisioning store: %v", err)
	}

	t.Run("behind the binary opens and does not migrate", func(t *testing.T) {
		parked := provisioned - 1
		stampSchemaVersion(t, admin, database, parked)
		t.Cleanup(func() { stampSchemaVersion(t, admin, database, provisioned) })

		store, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedElsewhere))
		if err != nil {
			t.Fatalf("SchemaOwnedElsewhere open of a database parked at v%d: %v", parked, err)
		}
		defer store.Close()

		if after := schemaVersion(t, admin, database); after != parked {
			t.Fatalf("open moved a parked database from v%d to v%d; a database parked below the binary must stay where its owner put it", parked, after)
		}
		if _, err := store.GetConfig(ctx, "issue_prefix"); err != nil {
			t.Fatalf("read through a parked database: %v", err)
		}
	})

	t.Run("ahead of the binary is refused", func(t *testing.T) {
		ahead := provisioned + 8
		stampSchemaVersion(t, admin, database, ahead)
		t.Cleanup(func() { stampSchemaVersion(t, admin, database, provisioned) })

		store, err := beads.OpenServer(ctx, serverConfig(database, beads.SchemaOwnedElsewhere))
		if err == nil {
			_ = store.Close()
			t.Fatalf("SchemaOwnedElsewhere open of a database at v%d succeeded; the binary cannot know that shape", ahead)
		}
	})
}
