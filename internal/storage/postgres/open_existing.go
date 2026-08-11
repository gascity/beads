package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

const existingSchemaQuery = `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)`

const existingMetadataTableQuery = `SELECT EXISTS (
	SELECT 1
	FROM pg_catalog.pg_class AS c
	JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	WHERE n.nspname = $1 AND c.relname = 'metadata' AND c.relkind IN ('r', 'p')
)`

const existingSchemaVersionQuery = `SELECT value FROM metadata WHERE key = $1`

const existingSchemaCapabilitiesQuery = `SELECT
	EXISTS (
		SELECT 1
		FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.oid
		WHERE n.nspname = $1
		  AND c.relname = 'bd_events_journal'
		  AND c.relkind IN ('r', 'p')
		  AND a.attname = 'comment_json'
		  AND a.attnum > 0
		  AND NOT a.attisdropped
	),
	EXISTS (
		SELECT 1
		FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = $2
		  AND c.relname = 'bd_events_seq'
		  AND c.relkind IN ('r', 'p')
	)`

// ErrExistingSchemaNeedsRepair identifies a schema stamped with the current
// version that is missing a known legacy capability. Callers may explicitly
// route only this error through the provisioning/repair opener.
var ErrExistingSchemaNeedsRepair = errors.New("postgres: existing schema needs known legacy repair")

// OpenExisting opens an already-provisioned Postgres workspace without applying
// DDL, seeds, migrations, or version stamps. It validates the schema version
// using SELECT-only checks, then pings the returned store before handing it to
// the caller. Database ACLs, rather than this opener, enforce later writes.
func OpenExisting(ctx context.Context, dsn, schema string) (storage.DoltStorage, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres: empty DSN")
	}
	if schema == "" {
		return nil, fmt.Errorf("postgres: empty schema")
	}
	if !schemaNameRe.MatchString(schema) {
		return nil, fmt.Errorf("postgres: invalid schema name %q", schema)
	}

	raw, err := pgdialect.OpenRaw(dsn, schema)
	if err != nil {
		return nil, fmt.Errorf("postgres: open existing (raw): %w", err)
	}
	if err := verifyExistingSchema(ctx, raw, schema); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := raw.Close(); err != nil {
		return nil, fmt.Errorf("postgres: close existing-schema verification connection: %w", err)
	}

	store, err := New(ctx, Config{DSN: dsn, Schema: schema})
	if err != nil {
		return nil, err
	}
	if err := store.DB().PingContext(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("postgres: ping existing store: %w", err)
	}
	return store, nil
}

// verifyExistingSchema proves that schema has already been provisioned by a
// compatible binary. Every database statement in this function is a SELECT.
func verifyExistingSchema(ctx context.Context, db *sql.DB, schema string) error {
	if !schemaNameRe.MatchString(schema) {
		return fmt.Errorf("postgres: invalid schema name %q", schema)
	}

	var exists bool
	if err := db.QueryRowContext(ctx, existingSchemaQuery, schema).Scan(&exists); err != nil {
		return fmt.Errorf("postgres: verify workspace schema %q: %w", schema, err)
	}
	if !exists {
		return fmt.Errorf("postgres: workspace schema %q does not exist; existing-schema open will not create it", schema)
	}
	if err := db.QueryRowContext(ctx, existingMetadataTableQuery, schema).Scan(&exists); err != nil {
		return fmt.Errorf("postgres: verify workspace schema %q metadata table: %w", schema, err)
	}
	if !exists {
		return fmt.Errorf("postgres: workspace schema %q is not provisioned: missing metadata table", schema)
	}

	var stored string
	err := db.QueryRowContext(ctx, existingSchemaVersionQuery, schemaVersionKey).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("postgres: workspace schema %q is not provisioned: missing schema version", schema)
	case err != nil:
		return fmt.Errorf("postgres: read workspace schema version: %w", err)
	case stored != schemaVersion:
		return fmt.Errorf("postgres: workspace schema version %s, this binary requires %s", stored, schemaVersion)
	}

	var journalPayload, eventSequence bool
	if err := db.QueryRowContext(ctx, existingSchemaCapabilitiesQuery, schema, schema).Scan(&journalPayload, &eventSequence); err != nil {
		return fmt.Errorf("postgres: inspect workspace schema capabilities: %w", err)
	}
	if !journalPayload {
		return fmt.Errorf("%w: missing bd_events_journal.comment_json", ErrExistingSchemaNeedsRepair)
	}
	if !eventSequence {
		return fmt.Errorf("%w: missing bd_events_seq", ErrExistingSchemaNeedsRepair)
	}
	return nil
}
