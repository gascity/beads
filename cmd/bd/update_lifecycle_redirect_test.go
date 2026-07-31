//go:build cgo

// Concurrency tests for `bd update`'s lifecycle redirect.
//
// A generic update may not cross the done/non-done boundary, so
// `bd update --status closed` is replayed as a guarded update followed by a
// separate Close. That split turns what used to be a single guarded
// transaction into two, opening a window between the guard and the close. The
// tests here inject a writer into exactly that window and pin the fence that
// closes it: the close carries the row version the guard read.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// racingOps decorates the issue-operations facade so a test can commit a
// concurrent write at the exact instant the redirect sits between its guarded
// update and the lifecycle operation it redirects to. It also records every
// request, which is what makes "did the close carry the guard's version?"
// observable without a second process.
type racingOps struct {
	inner issueops.Operations

	// afterUpdate runs once, after the first successful Update, before control
	// returns to the redirect. It stands in for the concurrent writer.
	afterUpdate func()
	raced       bool

	updates        []issueops.UpdateRequest
	updateVersions []int64
	closes         []issueops.CloseRequest
	closeVersions  []int64
	reopens        []issueops.ReopenRequest
}

func (o *racingOps) Create(ctx context.Context, request issueops.CreateRequest) (issueops.CreateResult, error) {
	return o.inner.Create(ctx, request)
}

func (o *racingOps) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	o.updates = append(o.updates, request)
	result, err := o.inner.Update(ctx, request)
	if err != nil {
		return result, err
	}
	if result.Issue != nil {
		o.updateVersions = append(o.updateVersions, result.Issue.RowVersion)
	}
	if o.afterUpdate != nil && !o.raced {
		o.raced = true
		o.afterUpdate()
	}
	return result, err
}

func (o *racingOps) Close(ctx context.Context, request issueops.CloseRequest) (issueops.CloseResult, error) {
	o.closes = append(o.closes, request)
	result, err := o.inner.Close(ctx, request)
	if err == nil && result.Issue != nil {
		o.closeVersions = append(o.closeVersions, result.Issue.RowVersion)
	}
	return result, err
}

func (o *racingOps) Reopen(ctx context.Context, request issueops.ReopenRequest) (issueops.ReopenResult, error) {
	o.reopens = append(o.reopens, request)
	return o.inner.Reopen(ctx, request)
}

// installRacingOps replaces the facade the write verbs build with a recording
// decorator. newParityEnv already registered the cleanup that restores the
// package seam.
func installRacingOps(t *testing.T) *racingOps {
	t.Helper()
	ops := &racingOps{}
	newIssueOperations = func(target beads.Storage) (issueops.Operations, error) {
		if decorated, ok := target.(storage.DoltStorage); ok {
			target = storage.UnwrapStore(decorated)
		}
		inner, err := beads.NewIssueOperations(target)
		if err != nil {
			return nil, err
		}
		ops.inner = inner
		return ops, nil
	}
	return ops
}

// TestUpdateLifecycleRedirectRefusesGuardToCloseRace drives the window itself.
// The guard passes, a concurrent writer then moves the very status the guard
// asserted, and the redirected close must refuse rather than close a row whose
// precondition has since evaporated. Without the version fence the close lands
// and the command reports success.
func TestUpdateLifecycleRedirectRefusesGuardToCloseRace(t *testing.T) {
	env := newParityEnv(t)
	ops := installRacingOps(t)
	env.seed("test-race1", "Guard to close race", func(i *types.Issue) {
		i.Assignee = "worker"
	})

	// The racer commits between the guard's transaction and the close's,
	// exactly where the redirect lost its atomicity.
	ops.afterUpdate = func() {
		if err := env.store.inner.UpdateIssue(rootCtx, "test-race1",
			map[string]interface{}{"status": string(types.StatusInProgress)}, "racer"); err != nil {
			t.Fatalf("racing update: %v", err)
		}
	}

	env.setFlags(updateCmd, map[string]string{
		"if-status": "open",
		"status":    "closed",
	})
	res := env.run(updateCmd, "test-race1")

	if res.exitCode == 0 {
		t.Fatalf("exit = 0; a close that raced its own guard must fail\nstderr:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "version mismatch") {
		t.Errorf("stderr lacks the compare-and-set refusal:\n%s", res.stderr)
	}
	if got := env.get("test-race1").Status; got != types.StatusInProgress {
		t.Errorf("status = %q, want %q; the refused close must not have written", got, types.StatusInProgress)
	}
	if len(ops.closes) != 1 {
		t.Fatalf("close requests = %d, want 1", len(ops.closes))
	}
	if ops.closes[0].ExpectedVersion == nil {
		t.Error("the redirected close carried no ExpectedVersion; the guard-to-close window is open")
	}
}

// TestUpdateLifecycleRedirectPinsGuardObservedVersion is the deterministic
// proxy for the race above: with no concurrent writer the redirect still has
// to hand the close the row version its guard transaction observed, or the
// fence is decorative.
func TestUpdateLifecycleRedirectPinsGuardObservedVersion(t *testing.T) {
	env := newParityEnv(t)
	ops := installRacingOps(t)
	env.seed("test-race2", "Guard version is pinned", func(i *types.Issue) {
		i.Assignee = "worker"
	})

	env.setFlags(updateCmd, map[string]string{
		"if-assignee": "worker",
		"priority":    "1",
		"status":      "closed",
	})
	res := env.run(updateCmd, "test-race2")

	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if got := env.get("test-race2").Status; got != types.StatusClosed {
		t.Errorf("status = %q, want %q", got, types.StatusClosed)
	}
	if len(ops.closes) != 1 {
		t.Fatalf("close requests = %d, want 1", len(ops.closes))
	}
	if ops.closes[0].ExpectedVersion == nil {
		t.Fatal("the redirected close carried no ExpectedVersion")
	}
	if len(ops.updateVersions) == 0 {
		t.Fatal("no update observed a row version")
	}
	guardVersion := ops.updateVersions[len(ops.updateVersions)-1]
	if got := *ops.closes[0].ExpectedVersion; got != guardVersion {
		t.Errorf("close ExpectedVersion = %d, want %d (the version the guard transaction left behind)", got, guardVersion)
	}
}

// TestUpdateLifecycleRedirectKeepsGuardMismatchExit13 pins the boundary
// between the two failure modes. A stale guard is refused by the FIRST update,
// before the boundary refusal that triggers the redirect exists, so it still
// exits 13 with the machine-greppable sentinel and never reaches a close.
func TestUpdateLifecycleRedirectKeepsGuardMismatchExit13(t *testing.T) {
	env := newParityEnv(t)
	ops := installRacingOps(t)
	env.seed("test-race3", "Stale guard closing", func(i *types.Issue) {
		i.Assignee = "someone-else"
	})

	env.setFlags(updateCmd, map[string]string{
		"if-assignee": "not-the-holder",
		"status":      "closed",
	})
	res := env.run(updateCmd, "test-race3")

	if res.exitCode != ExitGuardMismatch {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", res.exitCode, ExitGuardMismatch, res.stderr)
	}
	if !strings.Contains(res.stderr, "assignee mismatch") {
		t.Errorf("stderr lacks the %q sentinel:\n%s", "assignee mismatch", res.stderr)
	}
	if strings.Contains(res.stderr, "version mismatch") {
		t.Errorf("a stale guard must not be reported as a version mismatch:\n%s", res.stderr)
	}
	if len(ops.closes) != 0 {
		t.Errorf("close requests = %d, want 0; a refused guard writes nothing", len(ops.closes))
	}
	if got := env.get("test-race3").Status; got != types.StatusOpen {
		t.Errorf("status = %q, want %q", got, types.StatusOpen)
	}
}

// TestUpdateLifecycleRedirectLeavesUnguardedCloseUnpinned keeps the fence
// scoped to what it restores. An unguarded `bd update --status closed` never
// had a check to be atomic with — it was last-writer-wins before the redirect
// and stays that way — so pinning it would invent a failure mode.
func TestUpdateLifecycleRedirectLeavesUnguardedCloseUnpinned(t *testing.T) {
	env := newParityEnv(t)
	ops := installRacingOps(t)
	env.seed("test-race4", "Unguarded close", nil)

	env.setFlags(updateCmd, map[string]string{"status": "closed"})
	res := env.run(updateCmd, "test-race4")

	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if got := env.get("test-race4").Status; got != types.StatusClosed {
		t.Errorf("status = %q, want %q", got, types.StatusClosed)
	}
	if len(ops.closes) != 1 {
		t.Fatalf("close requests = %d, want 1", len(ops.closes))
	}
	if ops.closes[0].ExpectedVersion != nil {
		t.Errorf("unguarded close carried ExpectedVersion = %d; want none", *ops.closes[0].ExpectedVersion)
	}
}

// TestUpdateLifecycleRedirectPinsCustomDoneStatusTail covers the longest form
// of the redirect. A custom done status is reached by closing and then setting
// the status done-to-done, so the guard's fence has to survive one more hop:
// the trailing update pins what the close left behind, or the window simply
// moves to the end of the chain.
func TestUpdateLifecycleRedirectPinsCustomDoneStatusTail(t *testing.T) {
	env := newParityEnv(t)
	ops := installRacingOps(t)
	if err := env.store.inner.SetConfig(rootCtx, "status.custom", "wontfix:done"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}
	env.seed("test-race6", "Custom done target", nil)

	env.setFlags(updateCmd, map[string]string{
		"if-status": "open",
		"status":    "wontfix",
	})
	res := env.run(updateCmd, "test-race6")

	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if got := env.get("test-race6").Status; got != types.Status("wontfix") {
		t.Fatalf("status = %q, want %q", got, "wontfix")
	}
	if len(ops.closes) != 1 || ops.closes[0].ExpectedVersion == nil {
		t.Fatalf("the redirected close did not carry a fenced version: %+v", ops.closes)
	}
	if len(ops.closeVersions) != 1 {
		t.Fatalf("close post-state versions = %d, want 1", len(ops.closeVersions))
	}
	tail := ops.updates[len(ops.updates)-1]
	if tail.ExpectedVersion == nil {
		t.Fatal("the trailing status update carried no ExpectedVersion")
	}
	if got := *tail.ExpectedVersion; got != ops.closeVersions[0] {
		t.Errorf("trailing update ExpectedVersion = %d, want %d (the version the close left behind)", got, ops.closeVersions[0])
	}
}

// TestUpdateLifecycleRedirectPinsGuardedReopen covers the other direction of
// the same split: `--status open` on a done issue redirects to Reopen, which
// takes the same version precondition.
func TestUpdateLifecycleRedirectPinsGuardedReopen(t *testing.T) {
	env := newParityEnv(t)
	ops := installRacingOps(t)
	env.seed("test-race5", "Guarded reopen", func(i *types.Issue) {
		i.Status = types.StatusClosed
	})

	env.setFlags(updateCmd, map[string]string{
		"if-status": "closed",
		"status":    "open",
	})
	res := env.run(updateCmd, "test-race5")

	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if got := env.get("test-race5").Status; got != types.StatusOpen {
		t.Errorf("status = %q, want %q", got, types.StatusOpen)
	}
	if len(ops.reopens) != 1 {
		t.Fatalf("reopen requests = %d, want 1", len(ops.reopens))
	}
	if ops.reopens[0].ExpectedVersion == nil {
		t.Fatal("the redirected reopen carried no ExpectedVersion")
	}
	guardVersion := ops.updateVersions[len(ops.updateVersions)-1]
	if got := *ops.reopens[0].ExpectedVersion; got != guardVersion {
		t.Errorf("reopen ExpectedVersion = %d, want %d", got, guardVersion)
	}
}
