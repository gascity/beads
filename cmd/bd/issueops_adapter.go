package main

import (
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// newIssueOperations builds the guarded issue-operations facade for a store.
// It is a variable purely as a test seam: the write-verb parity tests
// substitute a counting facade so "which operations did this command perform"
// stays observable. Production always calls beads.NewIssueOperations.
var newIssueOperations = beads.NewIssueOperations

// writeOps returns the issue-operations facade for st, the store a write verb
// is actually writing to.
//
// Callers build it per write site — the global store, a RoutedResult's store,
// or a --repo target — rather than once per command, because the facade
// inherits whatever decorator stack its store carries. Building it over the
// store that owns the issue is what keeps a routed write hookless and a local
// write hooked, exactly as the direct store calls did.
func writeOps(st storage.DoltStorage) (issueops.Lifecycle, error) {
	return newIssueOperations(st)
}
