package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/pgdialect"
	"github.com/steveyegge/beads/internal/types"
)

// TestPostgresEventsJournalContract exercises the PostgreSQL implementation
// against the same six-operation contract that bd events exposes. It is gated
// because it requires an isolated local/test PostgreSQL instance, never a
// Hosted project database.
func TestPostgresEventsJournalContract(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping live Postgres journal test")
	}

	ctx := context.Background()
	schema := fmt.Sprintf("journal_%d", time.Now().UnixNano())
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitSchema(ctx, raw, schema); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	_ = raw.Close()

	st, err := New(ctx, Config{DSN: url, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
		_ = st.Close()
	})
	if err := st.SetConfig(ctx, "issue_prefix", "pgj"); err != nil {
		t.Fatal(err)
	}

	st.SetEventsJournalEnabled(true)
	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, IssueType: "task", Status: types.StatusOpen, Priority: 2}
	}
	if err := st.CreateIssue(ctx, newIssue("pgj-1"), "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateIssue(ctx, newIssue("pgj-2"), "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.RunInTransaction(ctx, "journal transaction labels", func(tx storage.Transaction) error {
		if err := tx.AddLabel(ctx, "pgj-1", "demo", "tester"); err != nil {
			return err
		}
		return tx.RemoveLabel(ctx, "pgj-1", "demo", "tester")
	}); err != nil {
		t.Fatal(err)
	}
	dep := &types.Dependency{IssueID: "pgj-2", DependsOnID: "pgj-1", Type: types.DepBlocks}
	if err := st.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveDependency(ctx, "pgj-2", "pgj-1", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateIssue(ctx, "pgj-1", map[string]any{"title": "updated"}, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIssue(ctx, "pgj-1", "done", "tester", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteIssue(ctx, "pgj-2"); err != nil {
		t.Fatal(err)
	}

	rows, err := st.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOps := []string{
		"create", "create",
		"update", "update", // transaction label add/remove
		"dep_add",
		"update", // derived is_blocked flip after dependency removal
		"dep_remove",
		"update", "close", "delete",
	}
	if len(rows) != len(wantOps) {
		t.Fatalf("journal rows=%d, want %d", len(rows), len(wantOps))
	}
	for i, row := range rows {
		if row.Seq != int64(i+1) || row.Op != wantOps[i] {
			t.Fatalf("row %d = seq=%d op=%q, want seq=%d op=%q", i, row.Seq, row.Op, i+1, wantOps[i])
		}
		if row.Op == "delete" {
			if row.IssueJSON != "" {
				t.Fatalf("delete payload = %q, want SQL NULL", row.IssueJSON)
			}
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, row.TS); err != nil || !strings.HasSuffix(row.TS, "Z") {
			t.Fatalf("row %d timestamp = %q, want RFC3339Nano UTC (parse err=%v)", i, row.TS, err)
		}
		var issue types.Issue
		if err := json.Unmarshal([]byte(row.IssueJSON), &issue); err != nil {
			t.Fatalf("row %d issue payload is not canonical issue JSON: %v", i, err)
		}
	}
	var derived types.Issue
	if err := json.Unmarshal([]byte(rows[5].IssueJSON), &derived); err != nil {
		t.Fatalf("derived readiness payload: %v", err)
	}
	if derived.ID != "pgj-2" || derived.IsBlocked {
		t.Fatalf("derived readiness update = id %q blocked=%v, want pgj-2 unblocked", derived.ID, derived.IsBlocked)
	}

	// Mutation and journal insert share one SQL transaction: rolling back both
	// cannot leave either a changed issue or an event row behind.
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "rolled-back", "pgj-1"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := issueops.RecordEventInTx(issueops.WithEventsJournal(ctx, true), tx, issueops.EventUpdate, "pgj-1"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	rowsAfterRollback, err := st.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAfterRollback) != len(rows) {
		t.Fatalf("rollback added journal rows: got %d, want %d", len(rowsAfterRollback), len(rows))
	}

	// Recovery must seed the counter from the journal high-water mark rather
	// than restart at one. This is the PostgreSQL crash/operator-repair path;
	// a stale/missing counter must never collide with an existing source cursor.
	if _, err := st.DB().ExecContext(ctx, "DELETE FROM bd_events_seq"); err != nil {
		t.Fatalf("remove counter for recovery proof: %v", err)
	}
	if err := st.CreateIssue(ctx, newIssue("pgj-recovered-counter"), "tester"); err != nil {
		t.Fatalf("create after counter recovery: %v", err)
	}
	recoveredRows, err := st.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recoveredRows[len(recoveredRows)-1].Seq, int64(len(rows)+1); got != want {
		t.Fatalf("recovered counter seq = %d, want journal high-water + 1 = %d", got, want)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- st.CreateIssue(ctx, newIssue(fmt.Sprintf("pgj-concurrent-%d", i)), "tester")
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent journaled create: %v", err)
		}
	}
	all, err := st.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	for i, row := range all {
		if row.Seq != int64(i+1) {
			t.Fatalf("gap or duplicate at row %d: seq=%d", i, row.Seq)
		}
	}
}
