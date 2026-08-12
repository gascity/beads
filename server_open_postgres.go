package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/postgres"
)

// PostgresServerConfig configures OpenServerPostgres. Unlike ServerConfig
// (discrete MySQL-shaped fields), Postgres connectivity is expressed as a
// complete pgx DSN because sslmode/sslrootcert/channel_binding are DSN-native
// and pgx owns their semantics.
type PostgresServerConfig struct {
	// DSN is a complete pgx v5 connection string, e.g.
	//   postgres://user:pass@host:5432/db?sslmode=verify-full&sslrootcert=/etc/ca/zone.pem
	// The password must already be resolved: none of the CLI credential ladder
	// (BEADS_PG_PASSWORD, BEADS_PG_PASSWORD_COMMAND, credentials file) runs here.
	// Hosted embedders inject the credential from their provider.
	DSN string

	// Schema is the per-project schema. It is provisioned if missing
	// (CREATE SCHEMA IF NOT EXISTS + the engine DDL, idempotent) and pinned as
	// search_path on every connection. In hosted deployments routing decides
	// this value; clients never do.
	Schema string

	// EventsJournal enables the durable bd events journal for this project
	// instance only. It is intentionally an embedding-server setting rather
	// than a process-global or CLI config switch.
	EventsJournal bool
}

// ErrPostgresExistingSchemaNeedsRepair identifies a current-version Postgres
// schema that is missing a known legacy capability. Callers may use errors.Is
// to route only this error through OpenServerPostgres for repair.
var ErrPostgresExistingSchemaNeedsRepair = postgres.ErrExistingSchemaNeedsRepair

// OpenServerPostgres opens the beads engine against an external Postgres
// server, the Postgres sibling of OpenServer. No .beads directory,
// metadata.json, or credentials file is involved. Provisioning is implicit and
// idempotent (the CreateIfMissing analog is always on): first open creates the
// schema, applies the engine DDL, and stamps the schema version; later opens
// verify the stamp and refuse a version mismatch (the embedding server owns
// schema version).
func OpenServerPostgres(ctx context.Context, cfg PostgresServerConfig) (Storage, error) {
	if err := validatePostgresServerConfig("OpenServerPostgres", cfg); err != nil {
		return nil, err
	}
	st, err := postgres.Provision(ctx, cfg.DSN, cfg.Schema)
	if err != nil {
		return nil, err
	}
	return configurePostgresServerStore(st, cfg)
}

// OpenExistingServerPostgres opens a previously provisioned Postgres schema
// without creating or altering it. It validates the stored schema version with
// reads and pings the returned store before returning it. Use OpenServerPostgres
// for provisioning a fresh schema.
func OpenExistingServerPostgres(ctx context.Context, cfg PostgresServerConfig) (Storage, error) {
	if err := validatePostgresServerConfig("OpenExistingServerPostgres", cfg); err != nil {
		return nil, err
	}
	st, err := postgres.OpenExisting(ctx, cfg.DSN, cfg.Schema)
	if err != nil {
		return nil, err
	}
	return configurePostgresServerStore(st, cfg)
}

func validatePostgresServerConfig(op string, cfg PostgresServerConfig) error {
	if cfg.DSN == "" {
		return fmt.Errorf("beads: %s requires a DSN", op)
	}
	if cfg.Schema == "" {
		return fmt.Errorf("beads: %s requires a Schema", op)
	}
	return nil
}

func configurePostgresServerStore(st storage.DoltStorage, cfg PostgresServerConfig) (Storage, error) {
	journalConfig, ok := st.(storage.EventsJournalConfigurer)
	if !ok {
		_ = st.Close()
		return nil, fmt.Errorf("beads: PostgreSQL store does not support events-journal configuration")
	}
	journalConfig.SetEventsJournalEnabled(cfg.EventsJournal)
	return st, nil
}
