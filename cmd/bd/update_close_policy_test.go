//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
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
// treats a bare `--force`. The flag now carries a second override that stands
// on its own, so `--force` with no `-a` is a legitimate request: the assignee
// half is simply not asserted, and the update applies.
func TestUpdateClosePolicyDirectForceWithoutAssignee(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-ucpf", "Force without assignee", nil)

	env.setFlags(updateCmd, map[string]string{"status": "closed", "force": "true"})
	res := env.run(updateCmd, "test-ucpf")
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if strings.Contains(res.stderr, "invalid forced assignee transfer") {
		t.Errorf("--force without -a still asserts the assignee fence:\n%s", res.stderr)
	}
	if got := env.get("test-ucpf").Status; got != types.StatusClosed {
		t.Errorf("status = %q, want closed", got)
	}
}

// TestUpdateClosePolicyDirectForceStillFencesAssigneeTransfer keeps the other
// half of `--force` intact. Conditioning it on an assignee edit must not turn
// it off when there IS one: a transfer away from a live foreign claim is still
// exactly what the flag authorizes.
func TestUpdateClosePolicyDirectForceStillFencesAssigneeTransfer(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-ucpa", "Held by another actor", func(i *types.Issue) {
		i.Assignee = "someone-else"
		i.Status = types.StatusInProgress
	})

	// Without --force the fence holds.
	env.setFlags(updateCmd, map[string]string{"assignee": "thief"})
	if res := env.run(updateCmd, "test-ucpa"); res.exitCode == 0 {
		t.Fatalf("unforced transfer succeeded; the fence is gone\nstderr:\n%s", res.stderr)
	}
	if got := env.get("test-ucpa").Assignee; got != "someone-else" {
		t.Fatalf("assignee = %q after a refused transfer, want someone-else", got)
	}

	// With it, the transfer is authorized.
	env.setFlags(updateCmd, map[string]string{"assignee": "thief", "force": "true"})
	if res := env.run(updateCmd, "test-ucpa"); res.exitCode != 0 {
		t.Fatalf("forced transfer exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if got := env.get("test-ucpa").Assignee; got != "thief" {
		t.Errorf("assignee = %q, want thief", got)
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

// TestUpdateClosePolicyBatchGrammarForceToken pins the batch update grammar's
// spelling of the override, and — the part that matters — pins the allowlist
// that keeps the reserved update-map key from being client-reachable. A script
// asks for force by the grammar's own token; it can never name the transport
// key itself, which is what stops the key from becoming a policy bypass.
func TestUpdateClosePolicyBatchGrammarForceToken(t *testing.T) {
	updates, err := parseUpdateKVs([]string{"status=closed", "force=true"})
	if err != nil {
		t.Fatalf("parseUpdateKVs(force=true): %v", err)
	}
	if got := updates[issueops.OpForceClosePolicy]; got != true {
		t.Errorf("updates[%q] = %v, want true", issueops.OpForceClosePolicy, got)
	}
	if updates["status"] != "closed" {
		t.Errorf("updates[status] = %v, want closed", updates["status"])
	}

	unforced, err := parseUpdateKVs([]string{"status=closed", "force=false"})
	if err != nil {
		t.Fatalf("parseUpdateKVs(force=false): %v", err)
	}
	if got := unforced[issueops.OpForceClosePolicy]; got != false {
		t.Errorf("updates[%q] = %v, want false", issueops.OpForceClosePolicy, got)
	}

	if _, err := parseUpdateKVs([]string{"force=perhaps"}); err == nil {
		t.Error("parseUpdateKVs accepted a non-boolean force value")
	}
	if _, err := parseUpdateKVs([]string{"_force_close_policy=true"}); err == nil {
		t.Error("parseUpdateKVs accepted the reserved update-map key as a client token")
	}
	if _, err := parseUpdateKVs([]string{"description=foo"}); err == nil {
		t.Error("parseUpdateKVs stopped rejecting keys outside its allowlist")
	}
}

// TestUpdateClosePolicyProxiedSpecCarriesForce pins the proxied path's
// translation of `--force`. This is the exact mapping whose absence was
// reverted in 11382270b: the proxied caller built a spec that never carried
// the override, so a shared policy check refused it with no way to say
// otherwise. The spec must carry it, and must not invent it.
func TestUpdateClosePolicyProxiedSpecCarriesForce(t *testing.T) {
	current := &types.Issue{ID: "test-ucpp", Status: types.StatusOpen}

	forced := buildUpdateSpecForIssue(current, &updateInput{
		fields: map[string]any{"status": string(types.StatusClosed)}, force: true,
	})
	if got := forced.Fields[issueops.OpForceClosePolicy]; got != true {
		t.Errorf("spec.Fields[%q] = %v, want true", issueops.OpForceClosePolicy, got)
	}
	if got := forced.Fields["status"]; got != string(types.StatusClosed) {
		t.Errorf("spec.Fields[status] = %v, want closed", got)
	}

	unforced := buildUpdateSpecForIssue(current, &updateInput{
		fields: map[string]any{"status": string(types.StatusClosed)},
	})
	if _, ok := unforced.Fields[issueops.OpForceClosePolicy]; ok {
		t.Errorf("spec.Fields carries %q without --force", issueops.OpForceClosePolicy)
	}
}
