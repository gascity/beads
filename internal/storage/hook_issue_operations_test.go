package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// stubLifecycle is an inert issueops.Lifecycle used as an identity marker: a
// test can assert which lifecycle a decorator recursed into.
type stubLifecycle struct{ issueops.Lifecycle }

// lifecycleStore is a DoltStorage whose only real method is IssueLifecycle.
type lifecycleStore struct {
	DoltStorage
	lifecycle issueops.Lifecycle
	err       error
}

func (s lifecycleStore) IssueLifecycle() (issueops.Lifecycle, error) { return s.lifecycle, s.err }

// TestHookFiringStoreIssueLifecycleLayersHooksOverInner pins the recursion.
// Delegating to the inner store instead would still compile and still satisfy
// Storage, and every guarded write would silently stop firing hooks.
func TestHookFiringStoreIssueLifecycleLayersHooksOverInner(t *testing.T) {
	inner := &stubLifecycle{}
	store := &HookFiringStore{inner: lifecycleStore{lifecycle: inner}}

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() error = %v", err)
	}
	hooked, ok := lifecycle.(*hookIssueOperations)
	if !ok {
		t.Fatalf("IssueLifecycle() = %T, want *hookIssueOperations", lifecycle)
	}
	if hooked.inner != issueops.Lifecycle(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's lifecycle", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreIssueLifecyclePropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: lifecycleStore{err: want}}

	lifecycle, err := store.IssueLifecycle()
	if !errors.Is(err, want) {
		t.Fatalf("IssueLifecycle() error = %v, want %v", err, want)
	}
	if lifecycle != nil {
		t.Fatalf("IssueLifecycle() = %T, want nil", lifecycle)
	}
}

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
