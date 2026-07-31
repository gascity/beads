package uow

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestIssueOperationsCreateRoutesInfraTypesToWisps pins the unit-of-work facade
// create against the same infra-type routing the direct and embedded backends
// apply: a configured infra type is ephemeral and lives in the wisp tables,
// never in issues.
func TestIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	setUOWConfig(t, ctx, provider, map[string]string{"types.custom": "agent", "types.infra": "agent"})

	result, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{Title: "infra bead", Status: issueops.StatusOpen, Priority: 2, IssueType: issueops.IssueType("agent")},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !result.Issue.Ephemeral {
		t.Errorf("create result Ephemeral = false, want true for infra type %q", result.Issue.IssueType)
	}
	assertUOWRowExists(t, ctx, provider, "wisps", result.Issue.ID)
	assertUOWRowMissing(t, ctx, provider, "issues", result.Issue.ID)

	// A no-history infra create keeps its no-history retention rather than being
	// upgraded to ephemeral, matching CreateIssue.
	noHistory, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{Title: "infra no-history", Status: issueops.StatusOpen, Priority: 2, IssueType: issueops.IssueType("agent"), NoHistory: true},
	})
	if err != nil {
		t.Fatalf("Create no-history: %v", err)
	}
	if noHistory.Issue.Ephemeral {
		t.Errorf("no-history infra create Ephemeral = true, want false")
	}
	assertUOWRowExists(t, ctx, provider, "wisps", noHistory.Issue.ID)
	assertUOWRowMissing(t, ctx, provider, "issues", noHistory.Issue.ID)

	// A non-infra type is unaffected.
	durable, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{Title: "durable bead", Status: issueops.StatusOpen, Priority: 2, IssueType: issueops.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create durable: %v", err)
	}
	if durable.Issue.Ephemeral {
		t.Errorf("durable create Ephemeral = true, want false")
	}
	assertUOWRowExists(t, ctx, provider, "issues", durable.Issue.ID)
	assertUOWRowMissing(t, ctx, provider, "wisps", durable.Issue.ID)
}

// setUOWConfig commits config keys through their own unit of work so a later
// operation reads them as committed state.
func setUOWConfig(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, entries map[string]string) {
	t.Helper()
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		for key, value := range entries {
			if err := uw.ConfigUseCase().SetConfig(ctx, key, value); err != nil {
				return "", fmt.Errorf("set %s: %w", key, err)
			}
		}
		return "configure issue operations", nil
	}); err != nil {
		t.Fatalf("set config %v: %v", entries, err)
	}
}

// countUOWRows counts rows for one issue ID in table.
func countUOWRows(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, table, id string) int {
	t.Helper()
	count, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (int, error) {
		//nolint:gosec // G201: table is a test-supplied constant
		result, err := uw.RawSQLUseCase().Query(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id)
		if err != nil {
			return 0, err
		}
		if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
			return 0, fmt.Errorf("unexpected count rows: %#v", result.Rows)
		}
		return strconv.Atoi(fmt.Sprint(result.Rows[0][0]))
	})
	if err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	return count
}

func assertUOWRowExists(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, table, id string) {
	t.Helper()
	if got := countUOWRows(t, ctx, provider, table, id); got != 1 {
		t.Errorf("%s rows for %s = %d, want 1", table, id, got)
	}
}

func assertUOWRowMissing(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, table, id string) {
	t.Helper()
	if got := countUOWRows(t, ctx, provider, table, id); got != 0 {
		t.Errorf("%s rows for %s = %d, want 0", table, id, got)
	}
}
