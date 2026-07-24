package main

import (
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/telemetry"
)

// wireStorageDecorators composes the storage chain in the order the rest of
// bd expects:
//
//	caller → PrefixMintingStore (outer) → HookFiringStore → InstrumentedStorage → raw DoltStorage
//
// telemetry.WrapStorage is a no-op when telemetry is disabled, so the
// instrumentation layer is only present when BD_OTEL_ENABLED=true (or a
// legacy BD_OTEL_* selector is set). The hook layer sits above it so storage
// spans measure pure DB time without hook-firing overhead.
//
// PrefixMintingStore sits outermost so it stamps the workspace mint prefix
// BEFORE the issue is persisted (and thus before its ID is minted), and the
// hook layer then fires on the fully-minted issue. It is a pure passthrough
// when workspacePrefix is empty (the common single-workspace case), so the
// chain is byte-for-byte unchanged unless a workspace opts into shared-DB
// prefix minting.
//
// Extracted from main.go's PersistentPreRunE so the chain composition is
// unit-testable — the bug this PR fixes was a missing WrapStorage call,
// and the regression class deserves test coverage.
func wireStorageDecorators(store storage.DoltStorage, hookRunner *hooks.Runner, hooksDisabled bool, workspacePrefix string) storage.DoltStorage {
	if store == nil {
		return nil
	}
	store = telemetry.WrapStorage(store)
	if hookRunner != nil && !hooksDisabled {
		store = storage.NewHookFiringStore(store, hookRunner)
	}
	store = storage.NewPrefixMintingStore(store, workspacePrefix)
	return store
}
