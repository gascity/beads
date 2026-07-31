package beads

import (
	"context"
	"reflect"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/telemetry"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// ErrUnsupported reports that an operation is unavailable for a storage backend.
type ErrUnsupported = storage.ErrUnsupported

// NewIssueOperations returns the guarded issue-mutation surface for store.
func NewIssueOperations(store Storage) (issueops.Lifecycle, error) {
	if isNilIssueOperationsStore(store) {
		return nil, &storage.ErrUnsupported{Op: "NewIssueOperations", Backend: "nil"}
	}
	var layers []issueOperationsLayer
	var current any = store
	for {
		switch backend := current.(type) {
		case *storage.HookFiringStore:
			if backend == nil || isNilIssueOperationsStore(backend.Unwrap()) {
				return unsupportedIssueOperationsBackend(current)
			}
			layers = append(layers, issueOperationsLayer{hooks: backend})
			current = backend.Unwrap()
		case *telemetry.InstrumentedStorage:
			if backend == nil || isNilIssueOperationsStore(backend.Unwrap()) {
				return unsupportedIssueOperationsBackend(current)
			}
			layers = append(layers, issueOperationsLayer{telemetry: backend})
			current = backend.Unwrap()
		case *dolt.DoltStore:
			operations, err := dolt.NewIssueOperations(backend)
			if err != nil {
				return nil, err
			}
			return applyIssueOperationsLayers(operations, layers), nil
		default:
			if _, unwraps := current.(storage.Unwrapper); unwraps {
				return unsupportedIssueOperationsBackend(current)
			}
			operations, err := newEmbeddedIssueOperations(current)
			if err != nil {
				return nil, err
			}
			return applyIssueOperationsLayers(operations, layers), nil
		}
	}
}

type issueOperationsLayer struct {
	hooks     *storage.HookFiringStore
	telemetry *telemetry.InstrumentedStorage
}

func applyIssueOperationsLayers(operations issueops.Lifecycle, layers []issueOperationsLayer) issueops.Lifecycle {
	for index := len(layers) - 1; index >= 0; index-- {
		layer := layers[index]
		if layer.hooks != nil {
			operations = &hookIssueOperations{inner: operations, hooks: layer.hooks}
			continue
		}
		operations = layer.telemetry.WrapIssueOperations(operations)
	}
	return operations
}

func unsupportedIssueOperationsBackend(store any) (issueops.Lifecycle, error) {
	return nil, &storage.ErrUnsupported{Op: "NewIssueOperations", Backend: reflect.TypeOf(store).String()}
}

// issueOperationHooks is the completion-hook surface hookIssueOperations needs
// from *storage.HookFiringStore. Depending on the interface instead of the
// concrete decorator makes "did this verb fire a hook?" observable at this
// boundary, so the per-verb firing rules below can be tested without a real
// filesystem-backed async hook runner.
type issueOperationHooks interface {
	CompleteIssueOperationCreate(ctx context.Context, issue *types.Issue, dependencies []*types.Dependency)
	CompleteIssueOperationUpdate(issue *types.Issue)
	CompleteIssueOperationClose(issue *types.Issue)
}

var _ issueOperationHooks = (*storage.HookFiringStore)(nil)

type hookIssueOperations struct {
	inner issueops.Lifecycle
	hooks issueOperationHooks
}

func (o *hookIssueOperations) Create(ctx context.Context, request issueops.CreateRequest) (issueops.CreateResult, error) {
	snapshot := storageissueops.CloneCreateRequest(request)
	result, err := o.inner.Create(ctx, snapshot)
	if err == nil && result.Issue != nil {
		o.hooks.CompleteIssueOperationCreate(ctx, result.Issue, storage.CreatePublicCreateDependencies(result.Issue.ID, snapshot))
	}
	return result, err
}

func (o *hookIssueOperations) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	result, err := o.inner.Update(ctx, request)
	if err == nil {
		o.hooks.CompleteIssueOperationUpdate(result.Issue)
	}
	return result, err
}

func (o *hookIssueOperations) Close(ctx context.Context, request issueops.CloseRequest) (issueops.CloseResult, error) {
	result, err := o.inner.Close(ctx, request)
	if err == nil {
		o.hooks.CompleteIssueOperationClose(result.Issue)
	}
	return result, err
}

// Reopen suppresses the update hook on a no-op, matching the legacy reopen
// path (hookTrackingLifecycleTransaction.ReopenIssueWithResult). Close and
// Update stay unconditional on success, also matching their legacy paths.
func (o *hookIssueOperations) Reopen(ctx context.Context, request issueops.ReopenRequest) (issueops.ReopenResult, error) {
	result, err := o.inner.Reopen(ctx, request)
	if err == nil && result.Changed {
		o.hooks.CompleteIssueOperationUpdate(result.Issue)
	}
	return result, err
}

func isNilIssueOperationsStore(store Storage) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
