package dolt

import (
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestUpdateIssueClosedBoundaryRefusesAtomically(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, test := range []struct {
		name string
		id   string
		wisp bool
	}{
		{name: "permanent", id: "boundary-permanent"},
		{name: "wisp", id: "boundary-wisp", wisp: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.wisp {
				createWisp(t, ctx, store, test.id)
			} else {
				createPerm(t, ctx, store, test.id)
			}
			before, err := store.GetIssue(ctx, test.id)
			if err != nil {
				t.Fatal(err)
			}
			events, err := store.GetEvents(ctx, test.id, 0)
			if err != nil {
				t.Fatal(err)
			}
			head := reopenDoltHead(t, ctx, store)

			if err := store.UpdateIssue(ctx, test.id, map[string]interface{}{"title": "must not persist", "status": types.StatusClosed}, "tester"); !errors.Is(err, storage.ErrClosedBoundary) {
				t.Fatalf("enter done error = %v, want ErrClosedBoundary", err)
			}
			after, err := store.GetIssue(ctx, test.id)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != before.Status || after.Title != before.Title || after.RowVersion != before.RowVersion || after.ClosedAt != nil {
				t.Fatalf("refused update changed issue: before=%+v after=%+v", before, after)
			}
			afterEvents, err := store.GetEvents(ctx, test.id, 0)
			if err != nil || len(afterEvents) != len(events) {
				t.Fatalf("events after refusal = %d, want %d; err=%v", len(afterEvents), len(events), err)
			}
			if got := reopenDoltHead(t, ctx, store); got != head {
				t.Fatalf("refused update changed HEAD from %s to %s", head, got)
			}

			if err := store.CloseIssue(ctx, test.id, "done", "tester", "session"); err != nil {
				t.Fatalf("CloseIssue: %v", err)
			}
			closed, err := store.GetIssue(ctx, test.id)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateIssue(ctx, test.id, map[string]interface{}{"status": types.StatusOpen}, "tester"); !errors.Is(err, storage.ErrClosedBoundary) {
				t.Fatalf("leave done error = %v, want ErrClosedBoundary", err)
			}
			stillClosed, err := store.GetIssue(ctx, test.id)
			if err != nil {
				t.Fatal(err)
			}
			if stillClosed.Status != closed.Status || stillClosed.RowVersion != closed.RowVersion || stillClosed.ClosedAt == nil || !stillClosed.ClosedAt.Equal(*closed.ClosedAt) {
				t.Fatalf("leave-done refusal changed issue: closed=%+v after=%+v", closed, stillClosed)
			}
		})
	}
}

func TestUpdateIssueSameStatusDoesNotCreateLifecycleMutation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "same-status-no-lifecycle-mutation"
	createPerm(t, ctx, store, id)
	before, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusOpen}, "tester"); err != nil {
		t.Fatalf("same-status UpdateIssue: %v", err)
	}
	afterSameStatus, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	afterSameStatusEvents, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if afterSameStatus.RowVersion != before.RowVersion {
		t.Errorf("same-status update changed RowVersion from %d to %d", before.RowVersion, afterSameStatus.RowVersion)
	}
	if len(afterSameStatusEvents) != len(beforeEvents) {
		t.Errorf("same-status update recorded %d events, want %d", len(afterSameStatusEvents), len(beforeEvents))
	}

	if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusOpen, "title": "renamed"}, "tester"); err != nil {
		t.Fatalf("same-status scalar UpdateIssue: %v", err)
	}
	afterScalar, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if afterScalar.Title != "renamed" {
		t.Errorf("mixed scalar update title = %q, want renamed", afterScalar.Title)
	}
	events, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var updated, statusChanged int
	for _, event := range events {
		switch event.EventType {
		case types.EventUpdated:
			updated++
		case types.EventStatusChanged:
			statusChanged++
		}
	}
	if len(events) != len(beforeEvents)+1 || updated != 1 || statusChanged != 0 {
		t.Errorf("mixed scalar same-status events = updated:%d status_changed:%d total:%d", updated, statusChanged, len(events))
	}
}

func TestDoltStoreSameStatusUpdatesDoNotPublishUnrelatedDurableChanges(t *testing.T) {
	for _, test := range []struct {
		name    string
		checked bool
	}{
		{name: "UpdateIssue"},
		{name: "UpdateIssueChecked", checked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			targetID := "store-same-status-target-" + test.name
			dirtyID := "store-same-status-dirty-" + test.name
			createPerm(t, ctx, store, targetID)
			createPerm(t, ctx, store, dirtyID)
			targetBefore, err := store.GetIssue(ctx, targetID)
			if err != nil {
				t.Fatalf("GetIssue target before: %v", err)
			}
			before := transactionWispLifecycleHead(t, ctx, store)

			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "working-only title", dirtyID); err != nil {
				t.Fatalf("stage unrelated issue edit: %v", err)
			}
			if _, err := store.db.ExecContext(ctx,
				"UPDATE events SET comment = ? WHERE issue_id = ? AND event_type = ?",
				"working-only event", dirtyID, string(types.EventCreated),
			); err != nil {
				t.Fatalf("stage unrelated event edit: %v", err)
			}

			updates := map[string]interface{}{"status": types.StatusOpen}
			if test.checked {
				version := targetBefore.RowVersion
				err = store.UpdateIssueChecked(ctx, targetID, updates, "tester", storage.UpdateIssueOptions{ExpectedVersion: &version})
			} else {
				err = store.UpdateIssue(ctx, targetID, updates, "tester")
			}
			if err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if after := transactionWispLifecycleHead(t, ctx, store); after != before {
				t.Errorf("%s same-status update changed HEAD from %s to %s", test.name, before, after)
			}
			targetAfter, err := store.GetIssue(ctx, targetID)
			if err != nil {
				t.Fatalf("GetIssue target after: %v", err)
			}
			if targetAfter.RowVersion != targetBefore.RowVersion {
				t.Errorf("%s same-status update changed RowVersion from %d to %d",
					test.name, targetBefore.RowVersion, targetAfter.RowVersion)
			}
			assertDirectSameStatusUnrelatedChangesWorkingOnly(t, ctx, store, dirtyID)
		})
	}
}

func TestUpdateIssuePreservesCallerUpdatesAfterSameStatusNormalization(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "same-status-preserves-caller-map"
	createPerm(t, ctx, store, id)
	updates := map[string]interface{}{"status": types.StatusOpen}

	if err := store.UpdateIssue(ctx, id, updates, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if got, ok := updates["status"]; !ok || got != types.StatusOpen {
		t.Fatalf("caller updates after same-status update = %#v, want status=open", updates)
	}
}

func TestUpdateIssueRetryRetainsStatusForClosedBoundary(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	const id = "retry-retains-status-boundary"
	createPerm(t, ctx, store, id)
	updates := map[string]interface{}{"status": types.StatusOpen}
	firstAttempt := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		<-firstAttempt
		closed <- store.CloseIssue(ctx, id, "done", "other", "session")
	}()

	var attempts atomic.Int32
	err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		if attempts.Add(1) == 1 {
			if _, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, "tester"); err != nil {
				return err
			}
			close(firstAttempt)
			if err := <-closed; err != nil {
				return err
			}
			return &mysql.MySQLError{Number: 1213, Message: "retry test"}
		}
		_, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, "tester")
		return err
	})
	if !errors.Is(err, storage.ErrClosedBoundary) {
		t.Fatalf("retry update error = %v, want ErrClosedBoundary", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("update attempts = %d, want 2", attempts.Load())
	}
	if got, ok := updates["status"]; !ok || got != types.StatusOpen {
		t.Fatalf("caller updates after retry = %#v, want status=open", updates)
	}
}

func TestUpdateIssueCustomDoneToDonePreservesClosureMetadata(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		targetID   = "custom-done-target"
		dependerID = "custom-done-depender"
		fromStatus = "archived"
		toStatus   = "retired"
	)
	createPerm(t, ctx, store, targetID)
	createPerm(t, ctx, store, dependerID)
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: dependerID, DependsOnID: targetID, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	for _, status := range []string{fromStatus, toStatus} {
		if _, err := store.db.ExecContext(ctx, "INSERT INTO custom_statuses (name, category) VALUES (?, ?)", status, string(types.CategoryDone)); err != nil {
			t.Fatalf("insert custom status %s: %v", status, err)
		}
	}
	if err := store.CloseIssue(ctx, targetID, "historical close", "tester", "old-session"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, targetID, map[string]interface{}{"status": fromStatus}, "tester"); err != nil {
		t.Fatalf("closed-to-first-custom-done UpdateIssue: %v", err)
	}
	before, err := store.GetIssue(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if before.ClosedAt == nil {
		t.Fatal("seed custom done issue lacks closed_at")
	}

	beforeEvents, err := store.GetEvents(ctx, targetID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateIssue(ctx, targetID, map[string]interface{}{"status": toStatus}, "tester"); err != nil {
		t.Fatalf("done-to-done UpdateIssue: %v", err)
	}
	after, err := store.GetIssue(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != types.Status(toStatus) {
		t.Errorf("status = %q, want %q", after.Status, toStatus)
	}
	if after.ClosedAt == nil || !after.ClosedAt.Equal(*before.ClosedAt) || after.CloseReason != before.CloseReason || after.ClosedBySession != before.ClosedBySession {
		t.Errorf("done-to-done update changed closure metadata: before=%+v after=%+v", before, after)
	}
	if after.RowVersion == before.RowVersion {
		t.Errorf("done-to-done status update did not change RowVersion (%d)", before.RowVersion)
	}
	events, err := store.GetEvents(ctx, targetID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var closed, reopened, statusChanged int
	for _, event := range events {
		switch event.EventType {
		case types.EventClosed:
			closed++
		case types.EventReopened:
			reopened++
		case types.EventStatusChanged:
			statusChanged++
		}
	}
	if len(events) != len(beforeEvents)+1 || statusChanged != 2 || closed != 1 || reopened != 0 {
		t.Errorf("done-to-done events = closed:%d reopened:%d status_changed:%d total:%d", closed, reopened, statusChanged, len(events))
	}
	var dependerBlocked bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", dependerID).Scan(&dependerBlocked); err != nil {
		t.Fatalf("read depender: %v", err)
	}
	if dependerBlocked {
		t.Error("done-to-done update left durable dependent blocked")
	}
}
