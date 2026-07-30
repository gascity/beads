package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type fakeIssueOperations struct {
	calls int
	err   error
}

func (f *fakeIssueOperations) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	f.calls++
	return issueops.CreateResult{}, f.err
}
func (f *fakeIssueOperations) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	f.calls++
	return issueops.UpdateResult{}, f.err
}
func (f *fakeIssueOperations) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	f.calls++
	return issueops.CloseResult{}, f.err
}
func (f *fakeIssueOperations) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	f.calls++
	return issueops.ReopenResult{}, f.err
}

func TestInstrumentedIssueOperationsForwardsEveryAttemptOnce(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	base := WrapStorage(&fakeDoltStore{}).(*InstrumentedStorage)
	for _, test := range []struct {
		name string
		call func(issueops.Operations) error
	}{
		{"create", func(o issueops.Operations) error {
			_, e := o.Create(context.Background(), issueops.CreateRequest{})
			return e
		}},
		{"update", func(o issueops.Operations) error {
			_, e := o.Update(context.Background(), issueops.UpdateRequest{})
			return e
		}},
		{"close", func(o issueops.Operations) error {
			_, e := o.Close(context.Background(), issueops.CloseRequest{})
			return e
		}},
		{"reopen", func(o issueops.Operations) error {
			_, e := o.Reopen(context.Background(), issueops.ReopenRequest{})
			return e
		}},
	} {
		t.Run(test.name+" success", func(t *testing.T) {
			fake := &fakeIssueOperations{}
			if err := test.call(base.WrapIssueOperations(fake)); err != nil || fake.calls != 1 {
				t.Fatalf("err=%v calls=%d", err, fake.calls)
			}
		})
		t.Run(test.name+" error", func(t *testing.T) {
			want := errors.New("underlying")
			fake := &fakeIssueOperations{err: want}
			if err := test.call(base.WrapIssueOperations(fake)); !errors.Is(err, want) || fake.calls != 1 {
				t.Fatalf("err=%v calls=%d", err, fake.calls)
			}
		})
	}
}
