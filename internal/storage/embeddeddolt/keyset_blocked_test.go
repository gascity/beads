//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestSearchIssuesKeysetEmbedded is the §13.12 regression on the embedded Dolt
// backend: a same-second group larger than a page pages completely under the
// (created_at DESC, id ASC) keyset with no drop/dup, routed through the public
// SearchIssues.
func TestSearchIssuesKeysetEmbedded(t *testing.T) {
	te := newTestEnv(t, "ks")
	ctx := t.Context()

	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	seeds := []struct {
		id string
		at time.Time
	}{
		{"k-newer", base.Add(time.Second)},
		{"k-a1", base}, {"k-a2", base}, {"k-a3", base}, {"k-a4", base}, {"k-a5", base},
		{"k-older", base.Add(-time.Second)},
	}
	for _, s := range seeds {
		iss := &types.Issue{ID: s.id, Title: s.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: s.at}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	want := []string{"k-newer", "k-a1", "k-a2", "k-a3", "k-a4", "k-a5", "k-older"}
	ids := func(issues []*types.Issue) []string {
		out := make([]string, len(issues))
		for i, iss := range issues {
			out[i] = iss.ID
		}
		return out
	}
	eq := func(got, exp []string) bool {
		if len(got) != len(exp) {
			return false
		}
		for i := range exp {
			if got[i] != exp[i] {
				return false
			}
		}
		return true
	}

	full, err := te.store.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: 100})
	if err != nil {
		t.Fatalf("SearchIssues(full): %v", err)
	}
	if got := ids(full); !eq(got, want) {
		t.Fatalf("full order = %v, want %v", got, want)
	}

	const pageSize = 2
	var collected []string
	seen := map[string]bool{}
	var afterCreatedAt *time.Time
	afterID := ""
	for i := 0; i < 100; i++ {
		page, err := te.store.SearchIssues(ctx, "", types.IssueFilter{
			IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: pageSize,
			AfterCreatedAt: afterCreatedAt, AfterID: afterID,
		})
		if err != nil {
			t.Fatalf("SearchIssues(page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		for _, iss := range page {
			if seen[iss.ID] {
				t.Fatalf("duplicate id %q across pages — same-second overflow lost", iss.ID)
			}
			seen[iss.ID] = true
			collected = append(collected, iss.ID)
		}
		last := page[len(page)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !eq(collected, want) {
		t.Fatalf("keyset paged order = %v, want %v (no drop/dup)", collected, want)
	}
}

// TestIsBlockedBatchEmbedded is the §13.7 parity regression on the embedded Dolt
// backend: the batch transitive is_blocked read agrees with per-row IsBlocked and
// reflects inherited blockedness with an empty direct-blocker set.
func TestIsBlockedBatchEmbedded(t *testing.T) {
	te := newTestEnv(t, "ib")
	ctx := t.Context()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s: %v", src, tgt, err)
		}
	}
	mk("ib-blk")
	mk("ib-parent")
	add("ib-parent", "ib-blk", types.DepBlocks)
	mk("ib-child")
	add("ib-child", "ib-parent", types.DepParentChild)
	mk("ib-free")

	ids := []string{"ib-blk", "ib-parent", "ib-child", "ib-free"}
	batch, err := te.store.IsBlockedBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	for _, id := range ids {
		want, _, err := te.store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		if batch[id] != want {
			t.Fatalf("IsBlockedBatch[%s] = %v, want %v (per-row IsBlocked)", id, batch[id], want)
		}
	}
	blocked, blockers, err := te.store.IsBlocked(ctx, "ib-child")
	if err != nil {
		t.Fatalf("IsBlocked(ib-child): %v", err)
	}
	if !blocked || len(blockers) != 0 {
		t.Fatalf("ib-child IsBlocked = (%v, %v), want (true, []) — inherited block, empty direct blockers", blocked, blockers)
	}
	if !batch["ib-child"] || !batch["ib-parent"] || batch["ib-free"] {
		t.Fatalf("IsBlockedBatch = %v, want child+parent true, free false", batch)
	}
}
