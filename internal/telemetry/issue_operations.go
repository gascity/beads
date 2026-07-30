package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// WrapIssueOperations instruments guarded public issue operations with this
// storage layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapIssueOperations(inner issueops.Operations) issueops.Operations {
	return &instrumentedIssueOperations{storage: s, inner: inner}
}

type instrumentedIssueOperations struct {
	storage *InstrumentedStorage
	inner   issueops.Operations
}

func (o *instrumentedIssueOperations) Create(ctx context.Context, request issueops.CreateRequest) (result issueops.CreateResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Create")
	result, err = o.inner.Create(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}

func (o *instrumentedIssueOperations) Update(ctx context.Context, request issueops.UpdateRequest) (result issueops.UpdateResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Update")
	result, err = o.inner.Update(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}

func (o *instrumentedIssueOperations) Close(ctx context.Context, request issueops.CloseRequest) (result issueops.CloseResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Close")
	result, err = o.inner.Close(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}

func (o *instrumentedIssueOperations) Reopen(ctx context.Context, request issueops.ReopenRequest) (result issueops.ReopenResult, err error) {
	ctx, span, started := o.storage.op(ctx, "IssueOperations.Reopen")
	result, err = o.inner.Reopen(ctx, request)
	o.storage.done(ctx, span, started, err)
	return result, err
}
