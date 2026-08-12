package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournal_EmbeddedPlumbing drives mutations through the DoltStore
// (the embedded/DoltStorage write plumbing, which bottoms out in the issueops
// *InTx functions) against real Dolt and asserts the journal at the issueops
// seam records each op with a counter-assigned monotonic seq. This is the second
// of the two plumbings; the first is exercised in domain/db.
func TestEventsJournal_EmbeddedPlumbing(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	enableJournalForTest(t, store)
	if _, err := store.db.ExecContext(ctx, "DELETE FROM bd_events_journal"); err != nil {
		t.Fatalf("clear journal: %v", err)
	}

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	must(store.CreateIssue(ctx, mk("bd-emb-1"), "actor"), "create 1")
	must(store.CreateIssue(ctx, mk("bd-emb-2"), "actor"), "create 2")
	must(store.UpdateIssue(ctx, "bd-emb-1", map[string]interface{}{"title": "renamed"}, "actor"), "update")
	must(store.AddLabel(ctx, "bd-emb-1", "urgent", "actor"), "add label")
	must(store.ClaimIssue(ctx, "bd-emb-1", "worker"), "claim")
	must(store.AddDependency(ctx, &types.Dependency{IssueID: "bd-emb-1", DependsOnID: "bd-emb-2", Type: types.DepBlocks}, "actor"), "add dep")
	must(store.RemoveDependency(ctx, "bd-emb-1", "bd-emb-2", "actor"), "remove dep")
	must(store.CloseIssue(ctx, "bd-emb-1", "done", "actor", ""), "close")
	must(store.DeleteIssue(ctx, "bd-emb-2"), "delete")

	type row struct {
		seq int64
		op  string
		id  string
	}
	rows, err := store.db.QueryContext(ctx,
		`SELECT seq, op, issue_id FROM bd_events_journal ORDER BY seq ASC`)
	must(err, "query journal")
	defer rows.Close()

	var got []row
	for rows.Next() {
		var r row
		must(rows.Scan(&r.seq, &r.op, &r.id), "scan")
		got = append(got, r)
	}
	must(rows.Err(), "rows err")

	wantOps := []string{
		"create", "create", "update", "update", "update", "dep_add",
		"update", // derived is_blocked flip after dependency removal
		"dep_remove", "close", "delete",
	}
	if len(got) != len(wantOps) {
		t.Fatalf("expected %d journal rows, got %d: %+v", len(wantOps), len(got), got)
	}
	var prev int64
	for i, want := range wantOps {
		if got[i].op != want {
			t.Fatalf("row %d: op %q, want %q (%+v)", i, got[i].op, want, got)
		}
		if got[i].seq <= prev {
			t.Fatalf("row %d: seq %d not strictly greater than prev %d", i, got[i].seq, prev)
		}
		prev = got[i].seq
	}
}

// TestEventsJournal_NoPhantomDeletes asserts the bulk delete (DeleteIssuesInTx)
// journals a delete only for ids that actually removed a row — never a phantom
// for an id that matched nothing.
func TestEventsJournal_NoPhantomDeletes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	if err := store.CreateIssue(ctx, mk("bd-pd-1"), "actor"); err != nil {
		t.Fatalf("create 1: %v", err)
	}
	if err := store.CreateIssue(ctx, mk("bd-pd-2"), "actor"); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM bd_events_journal"); err != nil {
		t.Fatalf("clear journal: %v", err)
	}

	// Delete a mix of present and absent ids; force avoids the dependent gate.
	if _, err := store.DeleteIssues(ctx, []string{"bd-pd-1", "bd-pd-missing-a", "bd-pd-2", "bd-pd-missing-b"}, false, true, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows, err := store.db.QueryContext(ctx, "SELECT op, issue_id FROM bd_events_journal ORDER BY seq ASC")
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	defer rows.Close()
	deleted := map[string]bool{}
	for rows.Next() {
		var op, id string
		if err := rows.Scan(&op, &id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if op != "delete" {
			t.Fatalf("unexpected op %q for %s", op, id)
		}
		deleted[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(deleted) != 2 || !deleted["bd-pd-1"] || !deleted["bd-pd-2"] {
		t.Fatalf("journal must record deletes only for present ids, got %v", deleted)
	}
}

// TestEventsJournal_RunInTransactionMixedBuckets proves one public
// RunInTransaction callback can journal both a durable issue and a wisp. That
// plumbing uses separate regular and ignored SQL transactions internally, so
// both mutations must still share one ordered journal without contending with
// each other on bd_events_seq.
func TestEventsJournal_RunInTransactionMixedBuckets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	enableJournalForTest(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	regular := &types.Issue{
		ID: "bd-jtx-regular", Title: "regular", IssueType: types.TypeTask, Status: types.StatusOpen,
	}
	wisp := &types.Issue{
		ID: "bd-jtx-wisp", Title: "wisp", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
	}
	if err := store.RunInTransaction(ctx, "test: journal mixed buckets", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{regular, wisp}, "actor")
	}); err != nil {
		t.Fatalf("RunInTransaction mixed journaled create: %v", err)
	}

	rows := readJournalRows(t, store)
	if len(rows) != 2 {
		t.Fatalf("mixed journal rows = %+v, want two creates", rows)
	}
	if rows[0].seq != 1 || rows[0].op != "create" || rows[0].id != regular.ID {
		t.Fatalf("mixed journal row 0 = %+v, want create for %s at seq 1", rows[0], regular.ID)
	}
	if rows[1].seq != 2 || rows[1].op != "create" || rows[1].id != wisp.ID {
		t.Fatalf("mixed journal row 1 = %+v, want create for %s at seq 2", rows[1], wisp.ID)
	}
}

// TestEventsJournal_RunInTransactionWispOnly guards the no-versioned-tables
// case. The journal-enabled transaction still has to persist its ignored wisp
// and journal row when there is nothing for the following Dolt commit to stage.
func TestEventsJournal_RunInTransactionWispOnly(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	enableJournalForTest(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wisp := &types.Issue{
		ID: "bd-jtx-wisp-only", Title: "wisp", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
	}
	if err := store.RunInTransaction(ctx, "test: journal wisp only", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, wisp, "actor")
	}); err != nil {
		t.Fatalf("RunInTransaction journaled wisp create: %v", err)
	}

	if got, err := store.GetIssue(ctx, wisp.ID); err != nil || !got.Ephemeral {
		t.Fatalf("journaled wisp persisted = (%+v, %v), want active wisp", got, err)
	}
	rows := readJournalRows(t, store)
	if len(rows) != 1 || rows[0].seq != 1 || rows[0].op != "create" || rows[0].id != wisp.ID {
		t.Fatalf("wisp-only journal rows = %+v, want one create at seq 1", rows)
	}
}
