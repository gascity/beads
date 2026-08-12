package beads

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// SchemaOwnership declares which process owns the schema of the database a
// ServerConfig names — that is, which process is allowed to create it and to
// apply the engine's migrations to it.
//
// It has no permissive zero value on purpose. An embedder that says nothing
// gets an error from OpenServer rather than the migrating behavior, so a new
// call site cannot acquire DDL-on-open by omission. Before this existed, every
// open migrated, and a hosted control plane's read paths silently migrated
// tenant databases to whatever version the deployed binary happened to embed.
type SchemaOwnership int

const (
	// SchemaOwnershipUnset is the zero value and is never valid.
	SchemaOwnershipUnset SchemaOwnership = iota

	// SchemaOwnedHere opens as the schema's owner: the open creates the
	// database if CreateIfMissing is set and applies pending migrations.
	// Provisioning and deliberate upgrade paths take this role.
	SchemaOwnedHere

	// SchemaOwnedElsewhere opens a database whose schema another process
	// owns — a provisioner, or a gateway server that provisions each project
	// at its own deployed version. The open issues no DDL.
	//
	// It is not read-only: rows are read and written as usual. This says who
	// migrates, not who writes, which is what a hosted service serving both
	// board reads and issue mutations from one pooled connection needs.
	SchemaOwnedElsewhere
)

// ErrNoSchema identifies a database that carries no beads schema, reported by a
// SchemaOwnedElsewhere open because it may not create one. Callers may use
// errors.Is to tell "this project was never provisioned" apart from a
// connection or credential failure — the Dolt counterpart to
// ErrPostgresExistingSchemaNeedsRepair on the Postgres opener.
var ErrNoSchema = dolt.ErrNoSchema

// ServerConfig describes how to reach an external dolt sql-server (server mode).
// It is the programmatic equivalent of the dolt_mode="server" metadata.json
// settings, for hosts that embed bd's storage layer and manage their own
// connection details — e.g. a multi-tenant service connecting to a different
// database per tenant. Unlike OpenFromConfig, it requires no .beads directory
// or metadata.json on disk, and the password is supplied directly rather than
// via a credentials file or environment variable.
type ServerConfig struct {
	Host     string // dolt sql-server host
	Port     int    // dolt sql-server port
	User     string // MySQL user
	Password string // MySQL password (supplied directly)
	Database string // SQL database name to USE
	TLS      bool   // enable TLS (required for Hosted Dolt)

	// CreateIfMissing issues CREATE DATABASE for Database if it does not exist.
	// It requires SchemaOwnedHere: creating a database this process may not
	// then populate would leave an empty shell behind.
	CreateIfMissing bool

	// SchemaOwnership states whether this process owns Database's schema. It is
	// required — see SchemaOwnership.
	SchemaOwnership SchemaOwnership

	// WorkDir is an optional writable directory for transient server-mode
	// bookkeeping (e.g. a resolved-port file when connecting to a localhost
	// server). It is not used for remote servers. Defaults to os.TempDir().
	WorkDir string
}

// OpenServer opens a Storage backed by an external dolt sql-server using the
// connection details in cfg directly, without reading metadata.json. Use it when
// embedding bd's storage layer in a service that owns its own configuration.
//
// cfg.SchemaOwnership is required and decides whether this open may apply the
// engine's schema migrations. It is refused when unset rather than defaulted,
// so no call site migrates a database by accident.
//
// The returned Storage must be closed when no longer needed.
func OpenServer(ctx context.Context, cfg ServerConfig) (Storage, error) {
	switch cfg.SchemaOwnership {
	case SchemaOwnedHere, SchemaOwnedElsewhere:
	case SchemaOwnershipUnset:
		return nil, fmt.Errorf("beads: OpenServer requires SchemaOwnership: SchemaOwnedHere to create and migrate %q, SchemaOwnedElsewhere to open it without DDL", cfg.Database)
	default:
		return nil, fmt.Errorf("beads: OpenServer: unknown SchemaOwnership %d", cfg.SchemaOwnership)
	}
	if cfg.CreateIfMissing && cfg.SchemaOwnership != SchemaOwnedHere {
		return nil, fmt.Errorf("beads: OpenServer: CreateIfMissing needs SchemaOwnedHere; creating %q without owning its schema would leave an empty database", cfg.Database)
	}

	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}
	return dolt.New(ctx, &dolt.Config{
		ServerMode:          true,
		ServerHost:          cfg.Host,
		ServerPort:          cfg.Port,
		ServerUser:          cfg.User,
		ServerPassword:      cfg.Password,
		ServerTLS:           cfg.TLS,
		Database:            cfg.Database,
		CreateIfMissing:     cfg.CreateIfMissing,
		ExternalSchemaOwner: cfg.SchemaOwnership == SchemaOwnedElsewhere,
		BeadsDir:            workDir,
		// Path is vestigial in server mode (the server holds the data) but New
		// requires it non-empty; point it inside WorkDir.
		Path: filepath.Join(workDir, "dolt"),
	})
}
