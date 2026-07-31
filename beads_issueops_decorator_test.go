package beads

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/telemetry"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

type fakeIssueOperations struct {
	calls   []string
	changed bool
	err     error
}

func (f *fakeIssueOperations) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	f.calls = append(f.calls, "create")
	return issueops.CreateResult{}, f.err
}
func (f *fakeIssueOperations) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	f.calls = append(f.calls, "update")
	return issueops.UpdateResult{Changed: f.changed}, f.err
}
func (f *fakeIssueOperations) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	f.calls = append(f.calls, "close")
	return issueops.CloseResult{Changed: f.changed}, f.err
}
func (f *fakeIssueOperations) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	f.calls = append(f.calls, "reopen")
	return issueops.ReopenResult{Changed: f.changed}, f.err
}

// recordingIssueOperationHooks records which completion hooks hookIssueOperations
// fired, so tests can assert the per-verb firing rules without a hook runner.
type recordingIssueOperationHooks struct {
	completions []string
}

func (r *recordingIssueOperationHooks) CompleteIssueOperationCreate(context.Context, *types.Issue, []*types.Dependency) {
	r.completions = append(r.completions, "create")
}
func (r *recordingIssueOperationHooks) CompleteIssueOperationUpdate(*types.Issue) {
	r.completions = append(r.completions, "update")
}
func (r *recordingIssueOperationHooks) CompleteIssueOperationClose(*types.Issue) {
	r.completions = append(r.completions, "close")
}

type unknownIssueOperationsWrapper struct {
	storage.DoltStorage
}

func (s *unknownIssueOperationsWrapper) Unwrap() storage.DoltStorage { return s.DoltStorage }

func TestNewIssueOperationsRebuildsKnownDecoratorOrder(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &dolt.DoltStore{}
	telemetryStore, ok := telemetry.WrapStorage(raw).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	operations, err := NewIssueOperations(storage.NewHookFiringStore(telemetryStore, nil))
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	outer, ok := operations.(*hookIssueOperations)
	if !ok {
		t.Fatalf("operations outer layer = %T, want *hookIssueOperations", operations)
	}
	if got := reflect.TypeOf(outer.inner).String(); got != "*telemetry.instrumentedIssueOperations" {
		t.Fatalf("operations inner layer = %s, want telemetry wrapper", got)
	}
}

func TestNewIssueOperationsPreservesTelemetryOutsideHooks(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &dolt.DoltStore{}
	hooked := storage.NewHookFiringStore(raw, nil)
	operations, err := NewIssueOperations(telemetry.WrapStorage(hooked))
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	if got := reflect.TypeOf(operations).String(); got != "*telemetry.instrumentedIssueOperations" {
		t.Fatalf("operations outer layer = %s, want telemetry wrapper", got)
	}
}

func TestNewIssueOperationsRebuildsRepeatedKnownDecorators(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &dolt.DoltStore{}
	firstTelemetry := telemetry.WrapStorage(raw)
	firstHook := storage.NewHookFiringStore(firstTelemetry, nil)
	secondTelemetry := telemetry.WrapStorage(firstHook)
	secondHook := storage.NewHookFiringStore(secondTelemetry, nil)

	operations, err := NewIssueOperations(secondHook)
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	if _, ok := operations.(*hookIssueOperations); !ok {
		t.Fatalf("operations outer layer = %T, want *hookIssueOperations", operations)
	}
}

func TestNewIssueOperationsRejectsUnknownOrBrokenDecorators(t *testing.T) {
	tests := []struct {
		name  string
		store Storage
	}{
		{
			name:  "unknown wrapper",
			store: &unknownIssueOperationsWrapper{DoltStorage: &dolt.DoltStore{}},
		},
		{
			name:  "known wrapper with nil inner store",
			store: storage.NewHookFiringStore(nil, nil),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations, err := NewIssueOperations(test.store)
			if operations != nil {
				t.Fatalf("NewIssueOperations() operations = %T, want nil", operations)
			}
			var unsupported *storage.ErrUnsupported
			if !errors.As(err, &unsupported) {
				t.Fatalf("NewIssueOperations() error = %v, want *storage.ErrUnsupported", err)
			}
		})
	}
}

func TestHookIssueOperationsForwardsEveryVerbExactlyOnceAndPreservesErrors(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name string
		call func(issueops.Lifecycle) error
	}{
		{"create", func(ops issueops.Lifecycle) error { _, err := ops.Create(ctx, issueops.CreateRequest{}); return err }},
		{"update", func(ops issueops.Lifecycle) error { _, err := ops.Update(ctx, issueops.UpdateRequest{}); return err }},
		{"close", func(ops issueops.Lifecycle) error { _, err := ops.Close(ctx, issueops.CloseRequest{}); return err }},
		{"reopen", func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err }},
	} {
		t.Run(test.name+" success", func(t *testing.T) {
			fake := &fakeIssueOperations{changed: true}
			if err := test.call(&hookIssueOperations{inner: fake, hooks: storage.NewHookFiringStore(nil, nil)}); err != nil || len(fake.calls) != 1 {
				t.Fatalf("forward = %v, calls=%v", err, fake.calls)
			}
		})
		t.Run(test.name+" error", func(t *testing.T) {
			want := errors.New("underlying")
			fake := &fakeIssueOperations{changed: true, err: want}
			if err := test.call(&hookIssueOperations{inner: fake, hooks: storage.NewHookFiringStore(nil, nil)}); !errors.Is(err, want) || len(fake.calls) != 1 {
				t.Fatalf("error=%v calls=%v", err, fake.calls)
			}
		})
	}
}

func TestHookIssueOperationsFiresCompletionHooksPerVerbRules(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		fake    *fakeIssueOperations
		call    func(issueops.Lifecycle) error
		wantErr bool
		want    []string
	}{
		{
			// Reopen mirrors hookTrackingLifecycleTransaction.ReopenIssueWithResult
			// (internal/storage/hook_decorator.go), which queues no hook when the
			// reopen changed nothing.
			name: "reopen no-op fires nothing",
			fake: &fakeIssueOperations{changed: false},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err },
			want: nil,
		},
		{
			name: "reopen change fires update",
			fake: &fakeIssueOperations{changed: true},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err },
			want: []string{"update"},
		},
		{
			name:    "reopen error fires nothing",
			fake:    &fakeIssueOperations{changed: true, err: errors.New("underlying")},
			call:    func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err },
			wantErr: true,
			want:    nil,
		},
		{
			// Update fires on every success, no-op included, mirroring
			// HookFiringStore.UpdateIssueChecked (internal/storage/hook_decorator.go).
			// Do not "unify" this with reopen's gating.
			name: "update no-op still fires update",
			fake: &fakeIssueOperations{changed: false},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Update(ctx, issueops.UpdateRequest{}); return err },
			want: []string{"update"},
		},
		{
			// Close fires on every success, including the idempotent re-close,
			// mirroring HookFiringStore.CloseIssueChecked
			// (internal/storage/hook_decorator.go). Do not "unify" this either.
			name: "close no-op still fires close",
			fake: &fakeIssueOperations{changed: false},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Close(ctx, issueops.CloseRequest{}); return err },
			want: []string{"close"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			err := test.call(&hookIssueOperations{inner: test.fake, hooks: recorder})
			if (err != nil) != test.wantErr {
				t.Fatalf("call error = %v, wantErr = %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(recorder.completions, test.want) {
				t.Fatalf("completion hooks = %#v, want %#v", recorder.completions, test.want)
			}
		})
	}
}
