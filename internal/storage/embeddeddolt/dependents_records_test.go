//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependentRecordsEmbedded mirrors the dolt-suite target-keyed dependents
// coverage on the embedded backend: direction, two-table span (durable + wisp
// sources), the type filter, row-id keyset paging with no drop/dup, and
// CountDependentRecords totals.
func TestGetDependentRecordsEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "dr")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "dr-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-s1", Title: "s1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-s2", Title: "s2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-w", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "dr-s1", DependsOnID: "dr-target", Type: types.DepBlocks},
		{IssueID: "dr-s2", DependsOnID: "dr-target", Type: types.DepParentChild},
		{IssueID: "dr-w", DependsOnID: "dr-target", Type: types.DepBlocks}, // wisp source -> wisp_dependencies
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	set := func(deps []*types.Dependency) map[string]bool {
		m := map[string]bool{}
		for _, d := range deps {
			if d.ID == "" {
				t.Fatalf("dependent row missing ID (keyset cursor): %+v", d)
			}
			m[d.IssueID] = true
		}
		return m
	}

	all, err := te.store.GetDependentRecords(ctx, "dr-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	got := set(all)
	if len(got) != 3 || !got["dr-s1"] || !got["dr-s2"] || !got["dr-w"] {
		t.Fatalf("dependents = %v, want {dr-s1, dr-s2, dr-w} (spanning both tables)", got)
	}

	// Row-id keyset paging across the two-table boundary.
	seen := map[string]bool{}
	after := ""
	for i := 0; i < 10; i++ {
		page, err := te.store.GetDependentRecords(ctx, "dr-target", "", 1, after)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
	}
	if len(seen) != 3 {
		t.Fatalf("paged sources = %v, want 3 distinct", seen)
	}

	// CountDependentRecords totals span both tables and honor the type filter.
	if n, err := te.store.CountDependentRecords(ctx, "dr-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords(all) = %d, want 3", n)
	}
	if n, err := te.store.CountDependentRecords(ctx, "dr-target", string(types.DepBlocks)); err != nil {
		t.Fatalf("CountDependentRecords(blocks): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(blocks) = %d, want 2 (dr-s1 + dr-w)", n)
	}
}
