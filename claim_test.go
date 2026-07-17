package beads

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestClaimSentinelsWrapPreservation proves that errors produced the way the
// engine's claim/circuit paths produce them still satisfy errors.Is against the
// ROOT package's re-exported sentinels — i.e. the alias re-export preserves the
// wrap chain across the package boundary. The wrapped forms mirror
// issueops/claim.go and storage/dolt/circuit.go exactly.
func TestClaimSentinelsWrapPreservation(t *testing.T) {
	t.Parallel()

	// Mirrors issueops.ClaimIssueInTx: fmt.Errorf("%w by %s", ErrAlreadyClaimed, assignee),
	// then an outer context wrap as a real caller would add.
	alreadyClaimed := fmt.Errorf("claim dr-1: %w", fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, "alice"))
	if !errors.Is(alreadyClaimed, ErrAlreadyClaimed) {
		t.Errorf("wrapped already-claimed error does not match root beads.ErrAlreadyClaimed")
	}

	notClaimable := fmt.Errorf("claim dr-1: %w", fmt.Errorf("%w: status %s", storage.ErrNotClaimable, "closed"))
	if !errors.Is(notClaimable, ErrNotClaimable) {
		t.Errorf("wrapped not-claimable error does not match root beads.ErrNotClaimable")
	}

	circuit := fmt.Errorf("read issue: %w", dolt.ErrCircuitOpen)
	if !errors.Is(circuit, ErrCircuitOpen) {
		t.Errorf("wrapped circuit error does not match root beads.ErrCircuitOpen")
	}

	// A conflict must not cross-match the wrong sentinel.
	if errors.Is(alreadyClaimed, ErrNotClaimable) {
		t.Errorf("already-claimed error unexpectedly matched ErrNotClaimable")
	}
}

func TestParseClaimConflict(t *testing.T) {
	t.Parallel()

	t.Run("already claimed recovers assignee", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("claim dr-1: %w", fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, "alice"))
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for an ErrAlreadyClaimed error")
		}
		if got.CurrentAssignee != "alice" {
			t.Errorf("CurrentAssignee = %q, want %q", got.CurrentAssignee, "alice")
		}
		if got.CurrentStatus != "" {
			t.Errorf("CurrentStatus = %q, want empty", got.CurrentStatus)
		}
	})

	t.Run("not claimable recovers status", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("%w: status %s", storage.ErrNotClaimable, "in_progress")
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for an ErrNotClaimable error")
		}
		if got.CurrentStatus != "in_progress" {
			t.Errorf("CurrentStatus = %q, want %q", got.CurrentStatus, "in_progress")
		}
		if got.CurrentAssignee != "" {
			t.Errorf("CurrentAssignee = %q, want empty", got.CurrentAssignee)
		}
	})

	t.Run("unrelated error returns false", func(t *testing.T) {
		t.Parallel()
		if got, ok := ParseClaimConflict(errors.New("boom")); ok {
			t.Errorf("ParseClaimConflict(unrelated) = (%+v, true), want ok=false", got)
		}
		if got, ok := ParseClaimConflict(nil); ok {
			t.Errorf("ParseClaimConflict(nil) = (%+v, true), want ok=false", got)
		}
	})

	t.Run("is-match with unparseable message still ok", func(t *testing.T) {
		t.Parallel()
		// Wraps the sentinel without the " by <assignee>" tail.
		err := fmt.Errorf("weird wrap: %w", storage.ErrAlreadyClaimed)
		got, ok := ParseClaimConflict(err)
		if !ok {
			t.Fatalf("ParseClaimConflict returned ok=false for a wrapped ErrAlreadyClaimed")
		}
		if got.CurrentAssignee != "" {
			t.Errorf("CurrentAssignee = %q, want empty for unparseable message", got.CurrentAssignee)
		}
	})
}
