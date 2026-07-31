package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueRefusesUnpoppedClosePolicyOverride pins the fail-loud half of
// the reserved-key transport on the embedded write funnel. A well-formed
// override is popped and leaves no trace; a malformed one survives the pop and
// is refused by name, so a caller that spells the override wrong learns about
// it instead of silently running unforced.
func TestUpdateIssueRefusesUnpoppedClosePolicyOverride(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "ufc-target"
	createPerm(t, ctx, store, id)

	err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"priority":                  1,
		issueops.OpForceClosePolicy: "yes",
	}, "tester")
	if err == nil {
		t.Fatal("UpdateIssue accepted a malformed close-policy override")
	}
	if !strings.Contains(err.Error(), "invalid field for update") || !strings.Contains(err.Error(), issueops.OpForceClosePolicy) {
		t.Fatalf("error = %v, want an \"invalid field for update\" refusal naming %q", err, issueops.OpForceClosePolicy)
	}
	issue, getErr := store.GetIssue(ctx, id)
	if getErr != nil {
		t.Fatalf("GetIssue: %v", getErr)
	}
	if issue.Priority != 2 {
		t.Errorf("priority = %d after a refused update, want the seeded 2", issue.Priority)
	}

	// A well-formed override is transport, not a column: it is popped, the rest
	// of the update applies, and nothing about it reaches the row.
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"priority":                  1,
		issueops.OpForceClosePolicy: true,
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue with a well-formed override: %v", err)
	}
	issue, getErr = store.GetIssue(ctx, id)
	if getErr != nil {
		t.Fatalf("GetIssue: %v", getErr)
	}
	if issue.Priority != 1 {
		t.Errorf("priority = %d, want 1", issue.Priority)
	}
	if issue.Status != types.StatusOpen {
		t.Errorf("status = %q, want open", issue.Status)
	}
}
