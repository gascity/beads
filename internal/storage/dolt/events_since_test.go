package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsSince verifies the durable keyset event read: (created_at, id)
// ordering, same-second id tie-break, cursor exclusivity, limit clamping, and
// durable-only scope (wisp events excluded).
func TestEventsSince(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// A durable issue to anchor events (events.issue_id → issues.id FK).
	durable := &types.Issue{ID: "es-durable", Title: "Durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("create durable issue: %v", err)
	}
	// A wisp issue whose "created" event lands in wisp_events, which the
	// durable-only feed must never surface.
	wisp := &types.Issue{ID: "es-wisp", Title: "Wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp issue: %v", err)
	}

	// Clear the auto-generated "created" event so the seeded rows are the only
	// durable events, giving a fully deterministic ordering.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM events WHERE issue_id = ?", durable.ID); err != nil {
		t.Fatalf("clear auto events: %v", err)
	}

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	type seed struct {
		id string
		at time.Time
	}
	// e1 and e2 share a second (tie broken by id ASC); e3 is one second later.
	seeds := []seed{
		{id: "00000000-0000-0000-0000-000000000001", at: base},
		{id: "00000000-0000-0000-0000-000000000002", at: base},
		{id: "00000000-0000-0000-0000-000000000003", at: base.Add(time.Second)},
	}
	for _, s := range seeds {
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, created_at) VALUES (?, ?, ?, ?, ?)",
			s.id, durable.ID, string(types.EventUpdated), "tester", s.at); err != nil {
			t.Fatalf("insert seed event %s: %v", s.id, err)
		}
	}

	ids := func(evs []*types.Event) []string {
		out := make([]string, len(evs))
		for i, e := range evs {
			out[i] = e.ID
		}
		return out
	}
	eq := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("event ids = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("event ids = %v, want %v", got, want)
			}
		}
	}

	// Empty cursor = from epoch; ordered by (created_at ASC, id ASC), durable only.
	all, err := store.EventsSince(ctx, storage.EventCursor{}, 10)
	if err != nil {
		t.Fatalf("EventsSince(epoch): %v", err)
	}
	eq(t, ids(all), []string{seeds[0].id, seeds[1].id, seeds[2].id})
	for _, e := range all {
		if e.IssueID == wisp.ID {
			t.Fatalf("durable-only feed returned wisp event for %s", wisp.ID)
		}
	}

	// Limit honored.
	page, err := store.EventsSince(ctx, storage.EventCursor{}, 2)
	if err != nil {
		t.Fatalf("EventsSince(limit=2): %v", err)
	}
	eq(t, ids(page), []string{seeds[0].id, seeds[1].id})

	// Cursor excludes its own row and orders the same-second tie by id: starting
	// at e1's (created_at, id) yields e2 then e3.
	afterE1, err := store.EventsSince(ctx, storage.EventCursor{CreatedAt: seeds[0].at, ID: seeds[0].id}, 10)
	if err != nil {
		t.Fatalf("EventsSince(after e1): %v", err)
	}
	eq(t, ids(afterE1), []string{seeds[1].id, seeds[2].id})

	// Exhausting the shared second advances to the next second's row.
	afterE2, err := store.EventsSince(ctx, storage.EventCursor{CreatedAt: seeds[1].at, ID: seeds[1].id}, 10)
	if err != nil {
		t.Fatalf("EventsSince(after e2): %v", err)
	}
	eq(t, ids(afterE2), []string{seeds[2].id})
}

// TestEventsSinceClaimedConstant verifies the real claim path writes an event
// whose type is the extracted EventClaimed constant ("claimed"), reachable
// through the durable keyset read.
func TestEventsSinceClaimedConstant(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{ID: "es-claim", Title: "Claimable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.ClaimIssue(ctx, issue.ID, "worker"); err != nil {
		t.Fatalf("claim issue: %v", err)
	}

	evs, err := store.EventsSince(ctx, storage.EventCursor{}, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.IssueID == issue.ID && e.EventType == types.EventClaimed {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q event found for claimed issue %s", types.EventClaimed, issue.ID)
	}
}
