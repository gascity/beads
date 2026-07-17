package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// engineReadsStore provisions a fresh, isolated MySQL database (schema +
// issue_prefix, as `bd init` leaves it) and returns the store with its raw
// *sql.DB for deterministic event seeding. Gated on BEADS_MYSQL_TEST_URL (a
// server DSN, e.g. user:pass@tcp(host:3306)/).
func engineReadsStore(t *testing.T) (storage.DoltStorage, *sql.DB) {
	t.Helper()
	url := os.Getenv("BEADS_MYSQL_TEST_URL")
	if url == "" {
		t.Skip("BEADS_MYSQL_TEST_URL not set")
	}
	ctx := context.Background()
	database := fmt.Sprintf("engreads_%d", time.Now().UnixNano())
	st, err := Provision(ctx, url, database)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := st.SetConfig(ctx, "issue_prefix", "er"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	t.Cleanup(func() {
		if serverDSN, e := withDatabase(url, ""); e == nil {
			if srv, e2 := sql.Open("mysql", serverDSN); e2 == nil {
				_, _ = srv.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+database+"`")
				_ = srv.Close()
			}
		}
		_ = st.Close()
	})
	s, ok := st.(*Store)
	if !ok {
		t.Fatalf("Provision returned %T, want *mysql.Store", st)
	}
	return st, s.DB()
}

// TestEventsSinceMySQL is the per-backend regression that the shared sqlkit
// EventsSince read behaves under the MySQL dialect: (created_at, id) ordering
// with a same-second id tie-break, cursor exclusivity, limit clamping, the
// per-issue scope, and durable-only span (wisp events excluded).
func TestEventsSinceMySQL(t *testing.T) {
	st, db := engineReadsStore(t)
	ctx := context.Background()

	durable := &types.Issue{ID: "es-durable", Title: "d", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := st.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("create durable: %v", err)
	}
	wisp := &types.Issue{ID: "es-wisp", Title: "w", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := st.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	// Clear the auto "created" event so the seeded rows are the only durable
	// events, for fully deterministic ordering.
	if _, err := db.ExecContext(ctx, "DELETE FROM events WHERE issue_id = ?", durable.ID); err != nil {
		t.Fatalf("clear auto events: %v", err)
	}

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seeds := []struct {
		id string
		at time.Time
	}{
		{"00000000-0000-0000-0000-000000000001", base},                  // e1, e2 share a second
		{"00000000-0000-0000-0000-000000000002", base},                  // tie broken by id ASC
		{"00000000-0000-0000-0000-000000000003", base.Add(time.Second)}, // one second later
	}
	for _, s := range seeds {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, created_at) VALUES (?, ?, ?, ?, ?)",
			s.id, durable.ID, string(types.EventUpdated), "tester", s.at); err != nil {
			t.Fatalf("insert seed %s: %v", s.id, err)
		}
	}

	ids := func(evs []*types.Event) []string {
		out := make([]string, len(evs))
		for i, e := range evs {
			out[i] = e.ID
		}
		return out
	}
	eq := func(got, want []string) {
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

	// Empty cursor: (created_at ASC, id ASC), durable only.
	all, err := st.EventsSince(ctx, storage.EventCursor{}, "", 10)
	if err != nil {
		t.Fatalf("EventsSince(epoch): %v", err)
	}
	eq(ids(all), []string{seeds[0].id, seeds[1].id, seeds[2].id})
	for _, e := range all {
		if e.IssueID == wisp.ID {
			t.Fatalf("durable-only feed returned a wisp event")
		}
	}

	// Limit honored.
	page, err := st.EventsSince(ctx, storage.EventCursor{}, "", 2)
	if err != nil {
		t.Fatalf("EventsSince(limit=2): %v", err)
	}
	eq(ids(page), []string{seeds[0].id, seeds[1].id})

	// Cursor excludes its own row and breaks the same-second tie by id.
	afterE1, err := st.EventsSince(ctx, storage.EventCursor{CreatedAt: seeds[0].at, ID: seeds[0].id}, "", 10)
	if err != nil {
		t.Fatalf("EventsSince(after e1): %v", err)
	}
	eq(ids(afterE1), []string{seeds[1].id, seeds[2].id})

	// Per-issue scope: only the target issue's events.
	scoped, err := st.EventsSince(ctx, storage.EventCursor{}, durable.ID, 10)
	if err != nil {
		t.Fatalf("EventsSince(issue): %v", err)
	}
	if len(scoped) != 3 {
		t.Fatalf("scoped feed len = %d, want 3", len(scoped))
	}
	other, err := st.EventsSince(ctx, storage.EventCursor{}, "es-nope", 10)
	if err != nil {
		t.Fatalf("EventsSince(issue=es-nope): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("feed for an issue with no events = %d, want 0", len(other))
	}
}

// TestDependentRecordsMySQL is the per-backend regression for the shared sqlkit
// target-keyed dependents reads under the MySQL dialect: direction, two-table
// span (durable + wisp sources), the type filter, row-id keyset paging, and the
// distinct-by-id count.
func TestDependentRecordsMySQL(t *testing.T) {
	st, _ := engineReadsStore(t)
	ctx := context.Background()

	mk := func(id string, ephemeral bool) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := st.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("dr-target", false)
	mk("dr-s1", false)
	mk("dr-s2", false)
	mk("dr-w", true)
	mk("dr-other", false)
	add := func(src, tgt string, typ types.DependencyType) {
		if err := st.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s: %v", src, tgt, err)
		}
	}
	add("dr-s1", "dr-target", types.DepBlocks)
	add("dr-s2", "dr-target", types.DepParentChild)
	add("dr-w", "dr-target", types.DepBlocks)     // wisp source -> wisp_dependencies
	add("dr-target", "dr-other", types.DepBlocks) // decoy: target is the source

	srcSet := func(deps []*types.Dependency) map[string]bool {
		out := map[string]bool{}
		for _, d := range deps {
			if d.ID == "" {
				t.Fatalf("dependent row missing ID (keyset cursor): %+v", d)
			}
			if d.DependsOnID != "dr-target" {
				t.Fatalf("row target = %q, want dr-target", d.DependsOnID)
			}
			out[d.IssueID] = true
		}
		return out
	}

	all, err := st.GetDependentRecords(ctx, "dr-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	got := srcSet(all)
	if len(got) != 3 || !got["dr-s1"] || !got["dr-s2"] || !got["dr-w"] {
		t.Fatalf("dependents = %v, want {dr-s1, dr-s2, dr-w}", got)
	}

	pc, err := st.GetDependentRecords(ctx, "dr-target", string(types.DepParentChild), 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(parent-child): %v", err)
	}
	if s := srcSet(pc); len(s) != 1 || !s["dr-s2"] {
		t.Fatalf("parent-child dependents = %v, want {dr-s2}", s)
	}

	// Row-id keyset paging across the two-table boundary.
	seen := map[string]bool{}
	after := ""
	for i := 0; i < 10; i++ {
		p, err := st.GetDependentRecords(ctx, "dr-target", "", 1, after)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if len(p) == 0 {
			break
		}
		if seen[p[0].IssueID] {
			t.Fatalf("duplicate source %q across pages", p[0].IssueID)
		}
		seen[p[0].IssueID] = true
		after = p[0].ID
	}
	if len(seen) != 3 {
		t.Fatalf("paged sources = %v, want 3 distinct", seen)
	}

	if n, err := st.CountDependentRecords(ctx, "dr-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords(all) = %d, want 3", n)
	}
	if n, err := st.CountDependentRecords(ctx, "dr-target", string(types.DepBlocks)); err != nil {
		t.Fatalf("CountDependentRecords(blocks): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(blocks) = %d, want 2 (dr-s1 + dr-w)", n)
	}
}
