package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// closePolicyVerdict classifies what a close-policy evaluation decided, so the
// standalone gate and the checked close can be compared without depending on
// the wording of either refusal.
type closePolicyVerdict string

const (
	verdictAllowed      closePolicyVerdict = "allowed"
	verdictOpenChildren closePolicyVerdict = "open children"
	verdictBlocked      closePolicyVerdict = "blocked"
)

func classifyClosePolicy(err error) closePolicyVerdict {
	var openChildren *storage.CloseOpenChildrenError
	switch {
	case err == nil:
		return verdictAllowed
	case errors.As(err, &openChildren):
		return verdictOpenChildren
	case errors.Is(err, storage.ErrCloseBlocked):
		return verdictBlocked
	}
	return closePolicyVerdict("unexpected: " + err.Error())
}

// inRolledBackTx runs body in its own transaction and always rolls it back, so
// a fixture survives being evaluated by both policy entry points in turn.
func inRolledBackTx(t *testing.T, ctx context.Context, store *DoltStore, body func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	return body(tx)
}

// TestEnforceClosePolicyInTxMatchesCheckedClose proves the extracted gate is
// the same policy the checked close applies, not a second implementation of it.
// Both entry points are run against identical fixtures in throwaway
// transactions and must reach the same verdict every time — that equivalence is
// the whole justification for letting a generic status update reuse the gate.
func TestEnforceClosePolicyInTxMatchesCheckedClose(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	mkParentWithOpenChild := func(prefix string) string {
		parent, child := prefix+"-parent", prefix+"-child"
		createPerm(t, ctx, store, parent)
		createPerm(t, ctx, store, child)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		return parent
	}
	mkBlocked := func(prefix string) string {
		blocker, target := prefix+"-blocker", prefix+"-target"
		createPerm(t, ctx, store, blocker)
		createPerm(t, ctx, store, target)
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: target, DependsOnID: blocker, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if !getIsBlocked(t, ctx, store, "issues", target) {
			t.Fatalf("%s should be is_blocked = 1", target)
		}
		return target
	}

	// An already-closed target whose blocker is still open. Closing recomputes
	// is_blocked to 0, so the column is put back by hand: that is the stale
	// column a re-close actually meets, and it makes the case discriminating —
	// the blocker list is live, so a gate that ran the blocker check here would
	// refuse.
	closedBlocked := mkBlocked("gpc-closed")
	if _, err := store.CloseIssueChecked(ctx, closedBlocked, "tester", storage.CloseIssueOptions{Reason: "done", Force: true}); err != nil {
		t.Fatalf("force close %s: %v", closedBlocked, err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", closedBlocked); err != nil {
		t.Fatalf("restore is_blocked on %s: %v", closedBlocked, err)
	}
	if !getIsBlocked(t, ctx, store, "issues", closedBlocked) {
		t.Fatalf("%s should carry is_blocked = 1 for this case", closedBlocked)
	}

	for _, tc := range []struct {
		name  string
		id    string
		force bool
		want  closePolicyVerdict
	}{
		{"open children refuse", mkParentWithOpenChild("gpc-oc"), false, verdictOpenChildren},
		{"open children forced", mkParentWithOpenChild("gpc-ocf"), true, verdictAllowed},
		{"live blocker refuses", mkBlocked("gpc-lb"), false, verdictBlocked},
		{"live blocker forced", mkBlocked("gpc-lbf"), true, verdictAllowed},
		{"already closed skips the blocker check", closedBlocked, false, verdictAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := classifyClosePolicy(inRolledBackTx(t, ctx, store, func(tx *sql.Tx) error {
				_, err := issueops.EnforceClosePolicyInTx(ctx, tx, tc.id, tc.force)
				return err
			}))
			checked := classifyClosePolicy(inRolledBackTx(t, ctx, store, func(tx *sql.Tx) error {
				_, err := issueops.CloseIssueCheckedInTx(ctx, tx, tc.id, "done", "tester", "", tc.force, nil)
				return err
			}))
			if gate != tc.want {
				t.Errorf("EnforceClosePolicyInTx verdict = %q, want %q", gate, tc.want)
			}
			if gate != checked {
				t.Errorf("gate verdict %q != checked-close verdict %q; the extraction changed policy", gate, checked)
			}
		})
	}
}

// TestEnforceClosePolicyInTxRoutesDualResidentToDurableRow pins the gate's
// table routing to the checked close's. An ID present in BOTH issues and wisps
// must have its children counted against the durable row; deriving the target
// from an is-wisp flag instead would silently count zero and wave the refusal
// through on exactly the ambiguous IDs that need it most.
func TestEnforceClosePolicyInTxRoutesDualResidentToDurableRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const parent, child = "gpd-parent", "gpd-child"
	createPerm(t, ctx, store, parent)
	createPerm(t, ctx, store, child)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Reproduce the post-promotion state where one ID holds a row in both
	// tables. Only the durable row carries the parent-child edge.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := issueops.InsertIssueStrictInTx(ctx, tx, "wisps", &types.Issue{
		ID: parent, Title: "wisp twin", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed wisp twin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit wisp twin: %v", err)
	}

	gate := classifyClosePolicy(inRolledBackTx(t, ctx, store, func(tx *sql.Tx) error {
		_, err := issueops.EnforceClosePolicyInTx(ctx, tx, parent, false)
		return err
	}))
	checked := classifyClosePolicy(inRolledBackTx(t, ctx, store, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueCheckedInTx(ctx, tx, parent, "done", "tester", "", false, nil)
		return err
	}))
	if gate != verdictOpenChildren {
		t.Errorf("dual-resident gate verdict = %q, want %q (durable row's open child)", gate, verdictOpenChildren)
	}
	if gate != checked {
		t.Errorf("dual-resident gate verdict %q != checked-close verdict %q; routing diverged", gate, checked)
	}
}

// TestCrossesIntoDoneCategoryInTx pins the trigger for the gate. It fires on a
// move INTO the done category from outside it — for a configured done status
// exactly as for the built-in closed — and on nothing else.
func TestCrossesIntoDoneCategoryInTx(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "status.custom", "review:wip,archived:done"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}

	for _, tc := range []struct {
		name    string
		old     types.Status
		updates map[string]interface{}
		want    bool
	}{
		{"open to built-in closed", types.StatusOpen, map[string]interface{}{"status": "closed"}, true},
		{"open to configured done", types.StatusOpen, map[string]interface{}{"status": "archived"}, true},
		{"typed status value", types.StatusOpen, map[string]interface{}{"status": types.StatusClosed}, true},
		{"wip to done", types.StatusInProgress, map[string]interface{}{"status": "closed"}, true},
		{"configured wip is not done", types.StatusOpen, map[string]interface{}{"status": "review"}, false},
		{"open to open", types.StatusOpen, map[string]interface{}{"status": "in_progress"}, false},
		{"done to done", types.StatusClosed, map[string]interface{}{"status": "closed"}, false},
		{"closed to configured done", types.StatusClosed, map[string]interface{}{"status": "archived"}, false},
		{"configured done to closed", types.Status("archived"), map[string]interface{}{"status": "closed"}, false},
		{"no status update", types.StatusOpen, map[string]interface{}{"priority": 1}, false},
		{"non-string status value", types.StatusOpen, map[string]interface{}{"status": 7}, false},
		{"unknown status is unspecified", types.StatusOpen, map[string]interface{}{"status": "not-configured"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got bool
			if err := inRolledBackTx(t, ctx, store, func(tx *sql.Tx) error {
				var err error
				got, err = issueops.CrossesIntoDoneCategoryInTx(ctx, tx, tc.old, tc.updates)
				return err
			}); err != nil {
				t.Fatalf("CrossesIntoDoneCategoryInTx: %v", err)
			}
			if got != tc.want {
				t.Errorf("CrossesIntoDoneCategoryInTx(%q, %v) = %v, want %v", tc.old, tc.updates, got, tc.want)
			}
		})
	}
}
