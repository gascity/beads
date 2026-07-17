// Package beads provides a minimal public API for extending bd with custom orchestration.
//
// Most extensions should use direct SQL queries against bd's database.
// This package exports only the essential types and functions needed for
// Go-based extensions that want to use bd's storage layer programmatically.
//
// For a working extension example, see examples/bd-example-extension-go.
package beads

import (
	"context"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// Storage is the interface for beads storage operations
type Storage = beads.Storage

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = beads.Transaction

// RemoteStore provides dolt remote management and replication operations.
// Use type assertion on a Storage value to access these methods:
//
//	if rs, ok := store.(beads.RemoteStore); ok {
//	    rs.Push(ctx)
//	}
type RemoteStore = storage.RemoteStore

// SyncStore provides high-level sync operations with peers.
type SyncStore = storage.SyncStore

// DependencyQueryStore provides extended dependency queries beyond the base
// Storage interface (raw source-keyed and target-keyed dependency records,
// blocking info, counts). Type-assert a Storage value to reach it:
//
//	if dq, ok := store.(beads.DependencyQueryStore); ok {
//	    rows, err := dq.GetDependentRecords(ctx, targetID, "", 100, "")
//	}
type DependencyQueryStore = storage.DependencyQueryStore

// EventQueryStore provides keyset paging over the durable event log
// (EventsSince). Type-assert a Storage value to reach it.
type EventQueryStore = storage.EventQueryStore

// EventCursor is a keyset position in the durable events stream, ordered by
// (created_at, id). The zero value means "from the beginning".
type EventCursor = storage.EventCursor

// Claim error sentinels, re-exported (aliased, so errors.Is works across the
// package boundary) from the storage layer. A claim that fails because the
// issue is held by another actor wraps ErrAlreadyClaimed; a claim on a
// non-open issue wraps ErrNotClaimable; a read/write rejected because the
// Dolt circuit breaker is open wraps ErrCircuitOpen. Use ParseClaimConflict
// to recover the conflicting assignee/status embedded in the message.
var (
	ErrAlreadyClaimed = storage.ErrAlreadyClaimed
	ErrNotClaimable   = storage.ErrNotClaimable
	ErrCircuitOpen    = dolt.ErrCircuitOpen
)

// IssueClaimer is the atomic-claim surface of a Storage. ClaimIssue and
// ClaimReadyIssue live on the storage.BulkIssueStore extension rather than the
// base Storage interface, so callers reach them by type-assertion via
// AsIssueClaimer rather than off the Storage value directly.
type IssueClaimer interface {
	// ClaimIssue atomically claims id for actor using compare-and-swap
	// semantics (open ∧ unassigned-or-same-actor). Returns a wrapped
	// ErrAlreadyClaimed or ErrNotClaimable on conflict.
	ClaimIssue(ctx context.Context, id string, actor string) error
	// ClaimReadyIssue atomically claims the first ready issue matching filter,
	// or returns (nil, nil) when none is claimable.
	ClaimReadyIssue(ctx context.Context, filter WorkFilter, actor string) (*Issue, error)
}

// AsIssueClaimer returns the IssueClaimer view of s when the backing store
// supports atomic claim (Dolt-backed stores do), and (nil, false) otherwise.
// Assert once at startup and fail loud: a Storage decorator that does not
// forward the claim surface makes this return false.
func AsIssueClaimer(s Storage) (IssueClaimer, bool) {
	c, ok := s.(IssueClaimer)
	return c, ok
}

// VersionControlReader provides read-only version control operations.
// Write operations (Branch, Checkout, Merge, DeleteBranch) are not yet
// part of the public API. If you need them, please open an issue.
type VersionControlReader interface {
	CurrentBranch(ctx context.Context) (string, error)
	ListBranches(ctx context.Context) ([]string, error)
	CommitExists(ctx context.Context, commitHash string) (bool, error)
	GetCurrentCommit(ctx context.Context) (string, error)
	Status(ctx context.Context) (*VCStatus, error)
	Log(ctx context.Context, limit int) ([]CommitInfo, error)
}

// Replication and version control types from internal/storage
type (
	RemoteInfo  = storage.RemoteInfo
	SyncResult  = storage.SyncResult
	SyncStatus  = storage.SyncStatus
	Conflict    = storage.Conflict
	CommitInfo  = storage.CommitInfo
	VCStatus    = storage.Status
	StatusEntry = storage.StatusEntry
)

// Open opens a Dolt-backed beads database at the given path.
// This always opens in embedded mode. Use OpenFromConfig to respect
// server mode settings from metadata.json.
func Open(ctx context.Context, dbPath string) (Storage, error) {
	return dolt.New(ctx, &dolt.Config{Path: dbPath, CreateIfMissing: true})
}

// OpenFromConfig opens a beads database using configuration from metadata.json.
// Unlike Open, this respects Dolt server mode settings and database name
// configuration, connecting to the Dolt SQL server when dolt_mode is "server".
// beadsDir is the path to the .beads directory.
func OpenFromConfig(ctx context.Context, beadsDir string) (Storage, error) {
	return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
}

// FindDatabasePath finds the beads database in the current directory tree
func FindDatabasePath() string {
	return beads.FindDatabasePath()
}

// FindBeadsDir finds the .beads/ directory in the current directory tree.
// Returns empty string if not found.
func FindBeadsDir() string {
	return beads.FindBeadsDir()
}

// DatabaseInfo contains information about a beads database
type DatabaseInfo = beads.DatabaseInfo

// FindAllDatabases finds all beads databases in the system
func FindAllDatabases() []DatabaseInfo {
	return beads.FindAllDatabases()
}

// RedirectInfo contains information about a beads directory redirect
type RedirectInfo = beads.RedirectInfo

// GetRedirectInfo checks if the current beads directory is redirected.
// Returns RedirectInfo with IsRedirected=true if a redirect is active.
func GetRedirectInfo() RedirectInfo {
	return beads.GetRedirectInfo()
}

// Core types from internal/types
type (
	Issue                       = types.Issue
	Status                      = types.Status
	IssueType                   = types.IssueType
	Dependency                  = types.Dependency
	DependencyType              = types.DependencyType
	Label                       = types.Label
	Comment                     = types.Comment
	Event                       = types.Event
	EventType                   = types.EventType
	BlockedIssue                = types.BlockedIssue
	TreeNode                    = types.TreeNode
	IssueFilter                 = types.IssueFilter
	WorkFilter                  = types.WorkFilter
	StaleFilter                 = types.StaleFilter
	DependencyCounts            = types.DependencyCounts
	IssueWithCounts             = types.IssueWithCounts
	IssueWithDependencyMetadata = types.IssueWithDependencyMetadata
	SortPolicy                  = types.SortPolicy
	EpicStatus                  = types.EpicStatus
	WispFilter                  = types.WispFilter
)

// Status constants
const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusBlocked    = types.StatusBlocked
	StatusDeferred   = types.StatusDeferred
	StatusClosed     = types.StatusClosed
)

// IssueType constants
const (
	TypeBug     = types.TypeBug
	TypeFeature = types.TypeFeature
	TypeTask    = types.TypeTask
	TypeEpic    = types.TypeEpic
	TypeChore   = types.TypeChore
)

// DependencyType constants
const (
	DepBlocks            = types.DepBlocks
	DepRelated           = types.DepRelated
	DepParentChild       = types.DepParentChild
	DepDiscoveredFrom    = types.DepDiscoveredFrom
	DepConditionalBlocks = types.DepConditionalBlocks // B runs only if A fails (bd-kzda)
)

// SortPolicy constants
const (
	SortPolicyHybrid   = types.SortPolicyHybrid
	SortPolicyPriority = types.SortPolicyPriority
	SortPolicyOldest   = types.SortPolicyOldest
)

// EventType constants
const (
	EventCreated           = types.EventCreated
	EventUpdated           = types.EventUpdated
	EventClaimed           = types.EventClaimed
	EventStatusChanged     = types.EventStatusChanged
	EventCommented         = types.EventCommented
	EventClosed            = types.EventClosed
	EventReopened          = types.EventReopened
	EventDependencyAdded   = types.EventDependencyAdded
	EventDependencyRemoved = types.EventDependencyRemoved
	EventLabelAdded        = types.EventLabelAdded
	EventLabelRemoved      = types.EventLabelRemoved
	EventCompacted         = types.EventCompacted
)
