//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Close policy on the generic status update. `bd close` refuses an issue that
// still has open parent-child children or a live direct blocker; the cases
// below pin what `bd update --status closed` does at that same boundary, on
// each surface that can reach a write funnel carrying a status of its own.
//
// These start as characterizations of today's permissive behavior. They are
// inverted in the commit that moves the policy into the funnels, so the
// behavior change reads as an explicit diff rather than a new file appearing
// beside untouched old assertions.

// seedClosePolicyFixture creates a parent with one open child and a target with
// one live direct blocker, and returns the two IDs a status update is aimed at.
func seedClosePolicyFixture(t *testing.T, env *parityEnv, prefix string) (parentID, blockedID string) {
	t.Helper()
	parentID = prefix + "-parent"
	childID := prefix + "-child"
	blockerID := prefix + "-blocker"
	blockedID = prefix + "-blocked"
	for _, id := range []string{parentID, childID, blockerID, blockedID} {
		env.seed(id, id, nil)
	}
	for _, dep := range []*types.Dependency{
		{IssueID: childID, DependsOnID: parentID, Type: types.DepParentChild},
		{IssueID: blockedID, DependsOnID: blockerID, Type: types.DepBlocks},
	} {
		if err := env.store.inner.AddDependency(rootCtx, dep, "parity-seed"); err != nil {
			t.Fatalf("seed dependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	if blocked, _, err := env.store.inner.IsBlocked(rootCtx, blockedID); err != nil || !blocked {
		t.Fatalf("%s should be blocked (blocked=%v err=%v)", blockedID, blocked, err)
	}
	env.store.reset()
	return parentID, blockedID
}

// TestUpdateClosePolicyDirectCrossesIntoDone drives the direct (non-proxied)
// `bd update` path, which reaches the embedded write funnel through the
// issue-operations facade.
func TestUpdateClosePolicyDirectCrossesIntoDone(t *testing.T) {
	env := newParityEnv(t)
	parentID, blockedID := seedClosePolicyFixture(t, env, "test-ucp")

	// CHARACTERIZATION: an open child does not stop the crossing.
	env.setFlags(updateCmd, map[string]string{"status": "closed"})
	res := env.run(updateCmd, parentID)
	if res.exitCode != 0 {
		t.Fatalf("update %s into done: exit = %d, want 0\nstderr:\n%s", parentID, res.exitCode, res.stderr)
	}
	if got := env.get(parentID).Status; got != types.StatusClosed {
		t.Errorf("%s status = %q, want closed", parentID, got)
	}

	// CHARACTERIZATION: neither does a live direct blocker.
	res = env.run(updateCmd, blockedID)
	if res.exitCode != 0 {
		t.Fatalf("update %s into done: exit = %d, want 0\nstderr:\n%s", blockedID, res.exitCode, res.stderr)
	}
	if got := env.get(blockedID).Status; got != types.StatusClosed {
		t.Errorf("%s status = %q, want closed", blockedID, got)
	}

	// A done-to-done restatement is filtered out as a no-op before any policy
	// could observe it.
	res = env.run(updateCmd, parentID)
	if res.exitCode != 0 {
		t.Fatalf("restate %s as done: exit = %d, want 0\nstderr:\n%s", parentID, res.exitCode, res.stderr)
	}
}

// TestUpdateClosePolicyDirectForceWithoutAssignee pins how the direct path
// treats a bare `--force`. The flag's only meaning today is the assignee
// fence, and the CLI hands it to the facade unconditionally, so a `--force`
// with no `-a` to authorize is rejected as an invalid request.
func TestUpdateClosePolicyDirectForceWithoutAssignee(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-ucpf", "Force without assignee", nil)

	env.setFlags(updateCmd, map[string]string{"status": "closed", "force": "true"})
	res := env.run(updateCmd, "test-ucpf")
	// CHARACTERIZATION: rejected, and the status never lands.
	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stderr, "invalid forced assignee transfer") {
		t.Errorf("stderr lacks the assignee-fence validation refusal:\n%s", res.stderr)
	}
	if got := env.get("test-ucpf").Status; got != types.StatusOpen {
		t.Errorf("status = %q after a rejected request, want open", got)
	}
}

// TestUpdateClosePolicyBatchCrossesIntoDone drives `bd batch update`, whose
// transaction reaches the same embedded write funnel without going through the
// facade at all.
func TestUpdateClosePolicyBatchCrossesIntoDone(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "tbc")
	ctx := context.Background()

	seedBatchTestIssues(t, ctx, st, "tbc-parent", "tbc-child", "tbc-blocker", "tbc-blocked")
	for _, dep := range []*types.Dependency{
		{IssueID: "tbc-child", DependsOnID: "tbc-parent", Type: types.DepParentChild},
		{IssueID: "tbc-blocked", DependsOnID: "tbc-blocker", Type: types.DepBlocks},
	} {
		if err := st.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("seed dependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	// CHARACTERIZATION: the batch grammar crosses both boundaries unimpeded.
	script := "update tbc-parent status=closed\nupdate tbc-blocked status=closed\n"
	if err := runBatchScriptInTx(t, ctx, st, script); err != nil {
		t.Fatalf("batch update into done: %v", err)
	}
	for _, id := range []string{"tbc-parent", "tbc-blocked"} {
		got, err := st.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue %s: %v", id, err)
		}
		if got.Status != types.StatusClosed {
			t.Errorf("%s status = %q, want closed", id, got.Status)
		}
	}
}

// TestUpdateClosePolicyBatchGrammarRejectsForce pins the batch update
// grammar's allowlist. It is the surface that keeps a reserved update-map key
// from being client-reachable, so it must reject anything it does not name.
func TestUpdateClosePolicyBatchGrammarRejectsForce(t *testing.T) {
	// CHARACTERIZATION: force is not part of the grammar.
	if _, err := parseUpdateKVs([]string{"status=closed", "force=true"}); err == nil {
		t.Fatal("parseUpdateKVs accepted force=true; the grammar has no override token yet")
	}
	if _, err := parseUpdateKVs([]string{"_force_close_policy=true"}); err == nil {
		t.Fatal("parseUpdateKVs accepted a reserved update-map key as a client token")
	}
}

// TestUpdateClosePolicyProxiedSpecCarriesNoForce pins the proxied path's
// translation of `--force`. This is the exact shape of the mapping that was
// reverted in 11382270b: the proxied caller built a spec that never carried
// the override, so a shared policy check would refuse it with no way to say
// otherwise.
func TestUpdateClosePolicyProxiedSpecCarriesNoForce(t *testing.T) {
	current := &types.Issue{ID: "test-ucpp", Status: types.StatusOpen}
	in := &updateInput{fields: map[string]any{"status": string(types.StatusClosed)}, force: true}

	spec := buildUpdateSpecForIssue(current, in)

	// CHARACTERIZATION: --force reaches no further than the reassign fence.
	// Spelled as a literal because no override key exists yet.
	if _, ok := spec.Fields["_force_close_policy"]; ok {
		t.Error("spec.Fields carries a close-policy override; the proxied path has none yet")
	}
	if got := spec.Fields["status"]; got != string(types.StatusClosed) {
		t.Errorf("spec.Fields[status] = %v, want closed", got)
	}
}
