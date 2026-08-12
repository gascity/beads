//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournal_EmbeddedConcurrentGaplessNoDup is the FINDING 1 proof for
// the embedded engine (the default local workspace). Unlike the SQL server —
// which resolves concurrent writers optimistically with a serialization abort at
// commit — the embedded engine serializes writers on the counter row. Either way
// the counter-drawn seq must come out gapless, commit-ordered, and duplicate-free
// under concurrent real mutations. N goroutines each create an issue through the
// real store path (store.CreateIssue -> issueops.CreateIssueInTx ->
// insertEventRow -> nextEventSeq); the journal must end with exactly one
// contiguous seq per create.
func TestEventsJournal_EmbeddedConcurrentGaplessNoDup(t *testing.T) {
	env := newTestEnv(t, "ecw")
	store := env.store
	store.SetEventsJournalEnabled(true)

	const writers = 12
	var wg sync.WaitGroup
	errs := make([]error, writers)
	done := make(chan struct{})
	go func() {
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				iss := &types.Issue{ID: fmt.Sprintf("ecw-%d", k), Title: "t", IssueType: types.TypeTask, Status: types.StatusOpen}
				errs[k] = store.CreateIssue(ctx, iss, "actor")
			}(i)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("concurrent CreateIssue timed out (deadlock?)")
	}
	for k, e := range errs {
		if e != nil {
			t.Fatalf("create %d failed: %v", k, e)
		}
	}

	rows, err := store.ReadEventsJournal(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(rows) != writers {
		t.Fatalf("journal rows = %d, want %d", len(rows), writers)
	}
	seen := map[int64]bool{}
	var prev int64
	for i, r := range rows {
		if seen[r.Seq] {
			t.Fatalf("duplicate seq %d", r.Seq)
		}
		seen[r.Seq] = true
		if i > 0 && r.Seq != prev+1 {
			t.Fatalf("seqs must be gapless and ordered: %d then %d", prev, r.Seq)
		}
		prev = r.Seq
	}
}
