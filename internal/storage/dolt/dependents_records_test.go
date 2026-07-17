package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependentRecords verifies the target-keyed raw dependents read:
// direction correctness (rows whose target is X, not whose source is X), the
// dep-type filter, deterministic issue_id ordering, keyset paging, and that it
// does not drop rows based on source status (raw, no source hydration).
func TestGetDependentRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Target X (the "epic"/group), the sources pointing AT it, and a decoy that
	// X itself points at (to prove direction). ids are chosen so issue_id ASC is
	// [s1, s2, s3, s4].
	mk := func(id string) *types.Issue {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return iss
	}
	target := mk("dr-target")
	block := mk("dr-s1-block")   // blocks -> X
	childA := mk("dr-s2-childa") // parent-child -> X
	childB := mk("dr-s3-childb") // parent-child -> X
	closed := mk("dr-s4-closed") // parent-child -> X, then closed
	other := mk("dr-other")      // X -> other (decoy: source is X)

	addDep := func(src, tgt string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s -> %s (%s): %v", src, tgt, typ, err)
		}
	}
	addDep(block.ID, target.ID, types.DepBlocks)
	addDep(childA.ID, target.ID, types.DepParentChild)
	addDep(childB.ID, target.ID, types.DepParentChild)
	addDep(closed.ID, target.ID, types.DepParentChild)
	addDep(target.ID, other.ID, types.DepBlocks) // decoy: target is the SOURCE here
	if err := store.CloseIssue(ctx, closed.ID, "done", "tester", ""); err != nil {
		t.Fatalf("close %s: %v", closed.ID, err)
	}

	srcIDs := func(deps []*types.Dependency) []string {
		out := make([]string, len(deps))
		for i, d := range deps {
			out[i] = d.IssueID
		}
		return out
	}

	// Direction + raw: all inbound edges of X, regardless of source status. The
	// decoy (target-is-X-as-source) must not appear; the closed source must.
	all, err := store.GetDependentRecords(ctx, target.ID, "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(all): %v", err)
	}
	wantAll := []string{block.ID, childA.ID, childB.ID, closed.ID}
	if got := srcIDs(all); !sameOrderedIDs(got, wantAll) {
		t.Fatalf("inbound sources = %v, want %v (ordered issue_id ASC)", got, wantAll)
	}
	for _, d := range all {
		if d.DependsOnID != target.ID {
			t.Fatalf("row %s has target %s, want %s", d.IssueID, d.DependsOnID, target.ID)
		}
		if d.IssueID == target.ID {
			t.Fatalf("direction violation: returned a row whose SOURCE is the target")
		}
	}

	// The decoy edge is discoverable from the OTHER direction (target=other).
	fromOther, err := store.GetDependentRecords(ctx, other.ID, "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(other): %v", err)
	}
	if got := srcIDs(fromOther); !sameOrderedIDs(got, []string{target.ID}) {
		t.Fatalf("inbound sources of other = %v, want %v", got, []string{target.ID})
	}

	// Type filter: only parent-child edges.
	pc, err := store.GetDependentRecords(ctx, target.ID, string(types.DepParentChild), 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(parent-child): %v", err)
	}
	if got := srcIDs(pc); !sameOrderedIDs(got, []string{childA.ID, childB.ID, closed.ID}) {
		t.Fatalf("parent-child sources = %v, want %v", got, []string{childA.ID, childB.ID, closed.ID})
	}

	// Keyset paging: page size 1 walks all four inbound sources in issue_id
	// order with no gaps or duplicates.
	var paged []string
	after := ""
	for {
		page, err := store.GetDependentRecords(ctx, target.ID, "", 1, after)
		if err != nil {
			t.Fatalf("GetDependentRecords(page after %q): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		paged = append(paged, page[0].IssueID)
		after = page[0].IssueID
	}
	if !sameOrderedIDs(paged, wantAll) {
		t.Fatalf("paged sources = %v, want %v", paged, wantAll)
	}
}

// sameOrderedIDs compares two string slices positionally (order-sensitive), since
// GetDependentRecords returns rows in issue_id ASC order.
func sameOrderedIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
