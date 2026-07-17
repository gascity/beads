package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIsBlockedBatch is the §13.7 regression on the reference Dolt backend: the
// batch transitive is_blocked read agrees with per-row IsBlocked for every id
// (direct blocker, inherited parent-child block, and unblocked control), and
// reflects inherited blockedness with an EMPTY direct-blocker set — the same
// denormalized column IsBlocked reads, in one round-trip, with no recompute.
func TestIsBlockedBatch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s: %v", src, tgt, err)
		}
	}
	// ib-blk (open) blocks ib-parent; ib-child is a parent-child of ib-parent so
	// it inherits is_blocked with no direct blocking edge; ib-free is unblocked.
	mk("ib-blk")
	mk("ib-parent")
	add("ib-parent", "ib-blk", types.DepBlocks)
	mk("ib-child")
	add("ib-child", "ib-parent", types.DepParentChild)
	mk("ib-free")

	ids := []string{"ib-blk", "ib-parent", "ib-child", "ib-free"}
	batch, err := store.IsBlockedBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}

	// Batch value must equal per-row IsBlocked for every id.
	for _, id := range ids {
		want, _, err := store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		if batch[id] != want {
			t.Fatalf("IsBlockedBatch[%s] = %v, want %v (per-row IsBlocked)", id, batch[id], want)
		}
	}

	// Inherited block: the child is transitively blocked with an EMPTY direct
	// blocker set, and the batch reflects it.
	blocked, blockers, err := store.IsBlocked(ctx, "ib-child")
	if err != nil {
		t.Fatalf("IsBlocked(ib-child): %v", err)
	}
	if !blocked || len(blockers) != 0 {
		t.Fatalf("ib-child IsBlocked = (%v, %v), want (true, []) — inherited block, empty direct blockers", blocked, blockers)
	}
	if !batch["ib-child"] || !batch["ib-parent"] {
		t.Fatalf("IsBlockedBatch child=%v parent=%v, want both true", batch["ib-child"], batch["ib-parent"])
	}
	if batch["ib-free"] {
		t.Fatalf("IsBlockedBatch[ib-free] = true, want false")
	}

	// Missing id is absent from the map (callers treat absent as not-blocked).
	miss, err := store.IsBlockedBatch(ctx, []string{"ib-nope"})
	if err != nil {
		t.Fatalf("IsBlockedBatch(missing): %v", err)
	}
	if _, ok := miss["ib-nope"]; ok {
		t.Fatalf("IsBlockedBatch returned an entry for a missing id: %v", miss)
	}
}
