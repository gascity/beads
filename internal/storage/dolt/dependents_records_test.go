package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependentRecords verifies the target-keyed raw dependents read:
// direction correctness (rows whose target is X, not whose source is X), the
// dep-type filter, that rows span BOTH the durable and wisp dependency tables,
// row-id keyset paging with no drop/dup across the two-table boundary, and that
// it does not drop rows on source status (raw, no source hydration). Ordering is
// by the dependency row's primary id (a UUIDv5), so assertions are set-based.
func TestGetDependentRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string, ephemeral bool) *types.Issue {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return iss
	}
	target := mk("dr-target", false)
	block := mk("dr-s1-block", false)   // blocks -> X (durable, `dependencies`)
	childA := mk("dr-s2-childa", false) // parent-child -> X (durable)
	closed := mk("dr-s3-closed", false) // parent-child -> X, then closed (durable)
	wispDep := mk("dr-s4-wisp", true)   // blocks -> X from a WISP source (`wisp_dependencies`)
	other := mk("dr-other", false)      // X -> other (decoy: source is X)

	addDep := func(src, tgt string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s -> %s (%s): %v", src, tgt, typ, err)
		}
	}
	addDep(block.ID, target.ID, types.DepBlocks)
	addDep(childA.ID, target.ID, types.DepParentChild)
	addDep(closed.ID, target.ID, types.DepParentChild)
	addDep(wispDep.ID, target.ID, types.DepBlocks) // wisp source -> durable target (wisp_dependencies)
	addDep(target.ID, other.ID, types.DepBlocks)   // decoy: target is the SOURCE here
	if err := store.CloseIssue(ctx, closed.ID, "done", "tester", ""); err != nil {
		t.Fatalf("close %s: %v", closed.ID, err)
	}

	srcSet := func(deps []*types.Dependency) map[string]bool {
		out := map[string]bool{}
		for _, d := range deps {
			out[d.IssueID] = true
		}
		return out
	}
	eqSet := func(t *testing.T, got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("dependent sources = %v, want %v", got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("dependent sources = %v, missing %q", got, w)
			}
		}
	}

	// Direction + raw + two-table span: all inbound edges of X regardless of
	// source status, INCLUDING the wisp source; the decoy must not appear.
	all, err := store.GetDependentRecords(ctx, target.ID, "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(all): %v", err)
	}
	eqSet(t, srcSet(all), block.ID, childA.ID, closed.ID, wispDep.ID)
	for _, d := range all {
		if d.ID == "" {
			t.Fatalf("dependent row has empty ID (the keyset cursor): %+v", d)
		}
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
	eqSet(t, srcSet(fromOther), target.ID)

	// Type filter: only parent-child edges (the wisp edge is 'blocks', excluded).
	pc, err := store.GetDependentRecords(ctx, target.ID, string(types.DepParentChild), 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(parent-child): %v", err)
	}
	eqSet(t, srcSet(pc), childA.ID, closed.ID)

	// Row-id keyset paging across the two-table boundary: page size 1 walks all
	// four inbound sources (three durable + one wisp) with no gaps or duplicates.
	seen := map[string]bool{}
	var pages int
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
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages (keyset drop/dup)", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
		if pages++; pages > 10 {
			t.Fatalf("paging did not terminate")
		}
	}
	eqSet(t, seen, block.ID, childA.ID, closed.ID, wispDep.ID)
}

// TestCountDependentRecords verifies the un-paged total matches the paged read's
// membership, spans both tables, and honors the type filter.
func TestCountDependentRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string, ephemeral bool) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("cd-target", false)
	mk("cd-s1", false)
	mk("cd-s2", false)
	mk("cd-w", true)
	add := func(src string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: "cd-target", Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s: %v", src, err)
		}
	}
	add("cd-s1", types.DepBlocks)
	add("cd-s2", types.DepParentChild)
	add("cd-w", types.DepBlocks) // wisp source (wisp_dependencies)

	if n, err := store.CountDependentRecords(ctx, "cd-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords(all) = %d, want 3 (2 durable + 1 wisp)", n)
	}
	if n, err := store.CountDependentRecords(ctx, "cd-target", string(types.DepBlocks)); err != nil {
		t.Fatalf("CountDependentRecords(blocks): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(blocks) = %d, want 2 (cd-s1 durable + cd-w wisp)", n)
	}
	if n, err := store.CountDependentRecords(ctx, "cd-target", string(types.DepParentChild)); err != nil {
		t.Fatalf("CountDependentRecords(parent-child): %v", err)
	} else if n != 1 {
		t.Fatalf("CountDependentRecords(parent-child) = %d, want 1", n)
	}
	// A target with no dependents counts zero, not an error.
	if n, err := store.CountDependentRecords(ctx, "cd-s1", ""); err != nil {
		t.Fatalf("CountDependentRecords(leaf): %v", err)
	} else if n != 0 {
		t.Fatalf("CountDependentRecords(leaf) = %d, want 0", n)
	}
}

// TestGetDependentRecordsLimitClamp verifies the self-clamp (default 100, cap 500).
func TestGetDependentRecordsLimitClamp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CreateIssue(ctx, &types.Issue{ID: "lc-target", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("create target: %v", err)
	}
	// 120 durable dependents -> more than the default clamp (100), fewer than
	// the cap (500), so we can observe the default kick in without seeding 500.
	const n = 120
	for i := 0; i < n; i++ {
		id := "lc-s" + pad(i)
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: "lc-target", Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dep %s: %v", id, err)
		}
	}

	// limit <= 0 => issueops.defaultDependentRecordsLimit (100).
	def, err := store.GetDependentRecords(ctx, "lc-target", "", 0, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(limit=0): %v", err)
	}
	if len(def) != 100 {
		t.Fatalf("default clamp: got %d rows, want 100", len(def))
	}
	// A limit above the total returns the total (and is under the 500 cap).
	full, err := store.GetDependentRecords(ctx, "lc-target", "", 100000, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(limit=100000): %v", err)
	}
	if len(full) != n {
		t.Fatalf("clamp with limit>total: got %d rows, want %d", len(full), n)
	}
}

// pad renders i as a zero-padded 4-digit string for stable id construction.
func pad(i int) string {
	s := ""
	for _, d := range []int{1000, 100, 10, 1} {
		s += string(rune('0' + (i/d)%10))
	}
	return s
}
