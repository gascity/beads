package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ClosedBoundaryError describes a generic update that crosses the done
// lifecycle boundary. Callers that can perform lifecycle operations may use
// its direction to select the appropriate operation.
type ClosedBoundaryError struct {
	from     types.Status
	to       types.Status
	fromDone bool
	toDone   bool
}

func (e *ClosedBoundaryError) Error() string {
	return fmt.Sprintf("%s: cannot change status from %q to %q with a generic update; use CloseIssue or ReopenIssue", storage.ErrClosedBoundary, e.From(), e.To())
}

// Unwrap preserves errors.Is(err, storage.ErrClosedBoundary) compatibility.
func (e *ClosedBoundaryError) Unwrap() error { return storage.ErrClosedBoundary }

// From returns the status observed before the refused transition.
func (e *ClosedBoundaryError) From() types.Status {
	if e == nil {
		return ""
	}
	return e.from
}

// To returns the requested status from the refused transition.
func (e *ClosedBoundaryError) To() types.Status {
	if e == nil {
		return ""
	}
	return e.to
}

// EntersDone reports whether the refused transition crosses into done.
func (e *ClosedBoundaryError) EntersDone() bool {
	return e != nil && !e.fromDone && e.toDone
}

// LeavesDone reports whether the refused transition crosses out of done.
func (e *ClosedBoundaryError) LeavesDone() bool {
	return e != nil && e.fromDone && !e.toDone
}

// GuardClosedBoundaryInTx validates a generic status update without allowing it
// to enter or leave a done-category status.
func GuardClosedBoundaryInTx(ctx context.Context, tx DBTX, current types.Status, requested any) (types.Status, bool, error) {
	next, err := requestedStatus(requested)
	if err != nil {
		return "", false, err
	}
	if next == current {
		return next, false, nil
	}

	var custom []types.CustomStatus
	if !current.IsValid() || !next.IsValid() {
		custom, err = ResolveCustomStatusesDetailedInTx(ctx, tx)
		if err != nil {
			return "", false, fmt.Errorf("resolve custom statuses: %w", err)
		}
	}

	currentCategory, _ := statusCategory(current, custom)
	nextCategory, nextKnown := statusCategory(next, custom)
	if !nextKnown {
		return "", false, fmt.Errorf("invalid status for update: %q", next)
	}

	currentDone := currentCategory == types.CategoryDone
	nextDone := nextCategory == types.CategoryDone
	if currentDone != nextDone {
		return "", false, &ClosedBoundaryError{from: current, to: next, fromDone: currentDone, toDone: nextDone}
	}
	return next, currentDone && nextDone, nil
}

func requestedStatus(requested any) (types.Status, error) {
	var status types.Status
	switch value := requested.(type) {
	case string:
		status = types.Status(value)
	case types.Status:
		status = value
	default:
		return "", fmt.Errorf("status for update must be a string, got %T", requested)
	}
	if status == "" {
		return "", fmt.Errorf("status for update must not be empty")
	}
	return status, nil
}

func statusCategory(status types.Status, custom []types.CustomStatus) (types.StatusCategory, bool) {
	if status.IsValid() {
		return types.BuiltInStatusCategory(status), true
	}
	for _, configured := range custom {
		if configured.Name == string(status) {
			return configured.Category, true
		}
	}
	return types.CategoryUnspecified, false
}
