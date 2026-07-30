package storage

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
)

// TestHookFiringStoreCompleteIssueOperationsFireOncePerCall pins the completion
// entry points to one hook event per call, in the caller's order. Deciding
// whether a committed change warrants a hook belongs to the caller
// (hookIssueOperations in the root package), not to these methods.
func TestHookFiringStoreCompleteIssueOperationsFireOncePerCall(t *testing.T) {
	runner := &recordingHookRunner{}
	store := &HookFiringStore{runner: runner}
	issue := &types.Issue{ID: "hook-issue"}

	store.CompleteIssueOperationCreate(context.Background(), issue, nil)
	store.CompleteIssueOperationUpdate(issue)
	store.CompleteIssueOperationClose(issue)

	if !reflect.DeepEqual(runner.events, []string{hooks.EventCreate, hooks.EventUpdate, hooks.EventClose}) {
		t.Fatalf("hook events = %#v, want create/update/close", runner.events)
	}
}

func TestHookFiringStoreCompleteIssueOperationCreateFiresReverseDependencyUpdate(t *testing.T) {
	runner := &recordingHookRunner{}
	reverse := &types.Dependency{IssueID: "existing-source", DependsOnID: "created", Type: types.DepRelatesTo, Metadata: `{"key":"value"}`, ThreadID: "thread"}
	inner := fakeHookStore{issues: map[string]*types.Issue{
		"created":         {ID: "created"},
		"existing-source": {ID: "existing-source", Dependencies: []*types.Dependency{reverse}},
	}}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	created := &types.Issue{ID: "created"}

	store.CompleteIssueOperationCreate(context.Background(), created, []*types.Dependency{{IssueID: "existing-source", DependsOnID: "created", Type: types.DepRelatesTo}})

	if !reflect.DeepEqual(runner.events, []string{hooks.EventCreate, hooks.EventUpdate}) {
		t.Fatalf("hook events = %#v, want create then dependency update", runner.events)
	}
	if runner.issues[1].ID != "existing-source" || !reflect.DeepEqual(runner.issues[1].Dependencies, []*types.Dependency{reverse}) {
		t.Fatalf("dependency hook snapshot = %#v", runner.issues[1])
	}
	if created.Dependencies != nil {
		t.Fatalf("created result was mutated: %#v", created.Dependencies)
	}
}
