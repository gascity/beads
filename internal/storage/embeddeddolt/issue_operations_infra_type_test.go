//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedIssueOperationsCreateRoutesInfraTypesToWisps pins the facade
// create against the same infra-type routing the store's own CreateIssue
// applies: a configured infra type is ephemeral and lives in the wisp tables,
// never in issues.
func TestEmbeddedIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "infra")
	ctx := t.Context()
	if err := te.store.SetConfig(ctx, "types.custom", "agent"); err != nil {
		t.Fatalf("SetConfig(types.custom): %v", err)
	}
	if err := te.store.SetConfig(ctx, "types.infra", "agent"); err != nil {
		t.Fatalf("SetConfig(types.infra): %v", err)
	}
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	result, err := operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "infra bead", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent")},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !result.Issue.Ephemeral {
		t.Errorf("create result Ephemeral = false, want true for infra type %q", result.Issue.IssueType)
	}
	te.assertRowExists(t, ctx, "wisps", result.Issue.ID)
	te.assertRowNotExists(t, ctx, "issues", result.Issue.ID)

	// A no-history infra create keeps its no-history retention rather than
	// being upgraded to ephemeral, matching CreateIssue.
	noHistory, err := operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "infra no-history", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent"), NoHistory: true},
	})
	if err != nil {
		t.Fatalf("Create no-history: %v", err)
	}
	if noHistory.Issue.Ephemeral {
		t.Errorf("no-history infra create Ephemeral = true, want false")
	}
	te.assertRowExists(t, ctx, "wisps", noHistory.Issue.ID)
	te.assertRowNotExists(t, ctx, "issues", noHistory.Issue.ID)

	// A non-infra type is unaffected.
	durable, err := operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "durable bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create durable: %v", err)
	}
	if durable.Issue.Ephemeral {
		t.Errorf("durable create Ephemeral = true, want false")
	}
	te.assertRowExists(t, ctx, "issues", durable.Issue.ID)
	te.assertRowNotExists(t, ctx, "wisps", durable.Issue.ID)
}
