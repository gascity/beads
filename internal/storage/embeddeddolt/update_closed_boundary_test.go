//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestEmbeddedUpdateClosedBoundaryRefusesAtomically(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "boundary")
	ctx := t.Context()

	for _, test := range []struct {
		id        string
		ephemeral bool
	}{
		{id: "boundary-permanent"},
		{id: "boundary-wisp", ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: test.id, Title: test.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: test.ephemeral}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", test.id, err)
		}
		before, err := te.store.GetIssue(ctx, test.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := te.store.UpdateIssue(ctx, test.id, map[string]interface{}{"title": "must not persist", "status": types.StatusClosed}, "tester"); !errors.Is(err, storage.ErrClosedBoundary) {
			t.Fatalf("enter done %s error = %v, want ErrClosedBoundary", test.id, err)
		}
		after, err := te.store.GetIssue(ctx, test.id)
		if err != nil || after.Status != before.Status || after.Title != before.Title || after.RowVersion != before.RowVersion || after.ClosedAt != nil {
			t.Fatalf("enter-done refusal changed %s: before=%+v after=%+v err=%v", test.id, before, after, err)
		}
		if err := te.store.CloseIssue(ctx, test.id, "done", "tester", "session"); err != nil {
			t.Fatalf("CloseIssue(%s): %v", test.id, err)
		}
		closed, err := te.store.GetIssue(ctx, test.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := te.store.UpdateIssue(ctx, test.id, map[string]interface{}{"status": types.StatusOpen}, "tester"); !errors.Is(err, storage.ErrClosedBoundary) {
			t.Fatalf("leave done %s error = %v, want ErrClosedBoundary", test.id, err)
		}
		stillClosed, err := te.store.GetIssue(ctx, test.id)
		if err != nil || stillClosed.Status != closed.Status || stillClosed.RowVersion != closed.RowVersion || stillClosed.ClosedAt == nil || !stillClosed.ClosedAt.Equal(*closed.ClosedAt) {
			t.Fatalf("leave-done refusal changed %s: before=%+v after=%+v err=%v", test.id, closed, stillClosed, err)
		}
	}
}

func TestEmbeddedUpdateCustomDoneToDonePreservesClosureMetadata(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "customdone")
	ctx := t.Context()
	const (
		targetID   = "embedded-custom-done-target"
		dependerID = "embedded-custom-done-depender"
		fromStatus = "archived"
		toStatus   = "retired"
	)
	for _, issue := range []*types.Issue{
		{ID: targetID, Title: targetID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: dependerID, Title: dependerID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: dependerID, DependsOnID: targetID, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	for _, status := range []string{fromStatus, toStatus} {
		te.exec(t, ctx, "INSERT INTO custom_statuses (name, category) VALUES (?, ?)", status, string(types.CategoryDone))
	}
	if err := te.store.CloseIssue(ctx, targetID, "historical close", "tester", "old-session"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if err := te.store.UpdateIssue(ctx, targetID, map[string]interface{}{"status": fromStatus}, "tester"); err != nil {
		t.Fatalf("closed-to-first-custom-done UpdateIssue: %v", err)
	}
	before, err := te.store.GetIssue(ctx, targetID)
	if err != nil || before.ClosedAt == nil {
		t.Fatalf("GetIssue before = %+v, %v", before, err)
	}
	beforeEvents, err := te.store.GetEvents(ctx, targetID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := te.store.UpdateIssue(ctx, targetID, map[string]interface{}{"status": toStatus}, "tester"); err != nil {
		t.Fatalf("done-to-done UpdateIssue: %v", err)
	}
	after, err := te.store.GetIssue(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != types.Status(toStatus) || after.ClosedAt == nil || !after.ClosedAt.Equal(*before.ClosedAt) || after.CloseReason != before.CloseReason || after.ClosedBySession != before.ClosedBySession || after.RowVersion == before.RowVersion {
		t.Fatalf("done-to-done result: before=%+v after=%+v", before, after)
	}
	events, err := te.store.GetEvents(ctx, targetID, 0)
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
	if len(events) != len(beforeEvents)+1 || closed != 1 || reopened != 0 || statusChanged != 2 {
		t.Fatalf("events = closed:%d reopened:%d status_changed:%d total:%d", closed, reopened, statusChanged, len(events))
	}
	var dependerBlocked bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{dependerID}, &dependerBlocked)
	if dependerBlocked {
		t.Fatal("durable depender remained blocked after done-to-done update")
	}
}
