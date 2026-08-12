package db

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// journalRow is one decoded bd_events_journal row.
type journalRow struct {
	Seq      int64
	Op       string
	IssueID  string
	Issue    *types.Issue
	Dep      *issueops.EventDep
	HasIssue bool
}

// enableJournalForTest turns the journal on, clears any rows left by earlier
// tests (the table is dolt-ignored, so DOLT_RESET does not clear it), and turns
// it back off on cleanup.
func (s *testSuite) enableJournalForTest() {
	s.journalEnabled = true
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM bd_events_journal")
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.journalEnabled = false })
}

func (s *testSuite) readJournal() []journalRow {
	rows, err := s.Runner().QueryContext(s.Ctx(),
		`SELECT seq, op, issue_id, issue_json, dep_json FROM bd_events_journal ORDER BY seq ASC`)
	s.Require().NoError(err)
	defer rows.Close()

	var out []journalRow
	for rows.Next() {
		var (
			jr      journalRow
			issueJS []byte
			depJS   []byte
		)
		s.Require().NoError(rows.Scan(&jr.Seq, &jr.Op, &jr.IssueID, &issueJS, &depJS))
		if len(issueJS) > 0 {
			jr.HasIssue = true
			var iss types.Issue
			s.Require().NoError(json.Unmarshal(issueJS, &iss))
			jr.Issue = &iss
		}
		if len(depJS) > 0 {
			var d issueops.EventDep
			s.Require().NoError(json.Unmarshal(depJS, &d))
			jr.Dep = &d
		}
		out = append(out, jr)
	}
	s.Require().NoError(rows.Err())
	return out
}

// TestEventsJournal_UOWPlumbing drives every op kind through the unit-of-work
// repository write path (which reimplements create/update/claim/delete/dep/label
// and delegates close/reopen to issueops) against real Dolt, and asserts the
// journal records each op with an engine-assigned monotonic seq.
func (s *testSuite) TestEventsJournal_UOWPlumbing() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()
	lr := s.labelRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-mj-1", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-mj-2", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Update(ctx, "bd-mj-1", map[string]any{"title": "renamed"}, "actor", domain.IssueTableOpts{}))
	s.Require().NoError(lr.Insert(ctx, "bd-mj-1", "urgent", "actor", domain.LabelOpts{}))
	_, err := ir.Claim(ctx, "bd-mj-1", "worker", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{IssueID: "bd-mj-1", DependsOnID: "bd-mj-2", Type: types.DepBlocks}, "actor", domain.DepInsertOpts{}))
	_, err = dr.Delete(ctx, "bd-mj-1", "bd-mj-2", "actor", domain.DepInsertOpts{})
	s.Require().NoError(err)
	_, err = ir.Close(ctx, "bd-mj-1", domain.CloseRowParams{Reason: "done"}, "actor", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(ir.Delete(ctx, "bd-mj-2", domain.IssueTableOpts{}))

	got := s.readJournal()
	wantOps := []string{
		"create", "create", "update", "update", "update", "dep_add",
		"update", // derived is_blocked flip after dependency removal
		"dep_remove", "close", "delete",
	}
	s.Require().Len(got, len(wantOps), "journal rows: %+v", got)

	var prev int64
	for i, want := range wantOps {
		s.Equalf(want, got[i].Op, "row %d op", i)
		s.Greaterf(got[i].Seq, prev, "row %d seq must be strictly increasing", i)
		prev = got[i].Seq
	}
	// Update snapshot reflects the post-mutation title.
	s.Require().True(got[2].HasIssue)
	s.Equal("renamed", got[2].Issue.Title)
	// dep_add carries the edge details.
	s.Require().NotNil(got[5].Dep)
	s.Equal(string(types.DepBlocks), got[5].Dep.Kind)
	s.Equal("bd-mj-2", got[5].Dep.Target)
	// Dependency removal changes the persisted readiness projection before its
	// edge delta, so cursor consumers receive the derived source update too.
	s.Require().True(got[6].HasIssue)
	s.False(got[6].Issue.IsBlocked)
	// dep_remove carries the edge details.
	s.Require().NotNil(got[7].Dep)
	s.Equal("bd-mj-2", got[7].Dep.Target)
	// delete carries a null issue payload.
	s.Equal("delete", got[9].Op)
	s.False(got[9].HasIssue, "delete row must have null issue")
}

// TestEventsJournal_CascadeDelete asserts a cascade delete journals every
// affected bead — the finding the decorator design could not cover.
func (s *testSuite) TestEventsJournal_CascadeDelete() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-cd-parent", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-cd-child", "t"), "actor", domain.InsertIssueOpts{}))
	// child depends on parent via parent-child, so deleting the parent cascades.
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{IssueID: "bd-cd-child", DependsOnID: "bd-cd-parent", Type: types.DepParentChild}, "actor", domain.DepInsertOpts{}))

	// clear the setup rows so we assert only on the cascade delete.
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	uc := s.issueUseCase()
	res, err := uc.DeleteIssues(ctx, domain.DeleteIssuesParams{IDs: []string{"bd-cd-parent"}, Cascade: true}, "actor")
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(res.DeletedCount, 2)

	deleted := map[string]bool{}
	for _, r := range s.readJournal() {
		if r.Op == "delete" {
			deleted[r.IssueID] = true
		}
	}
	s.True(deleted["bd-cd-parent"], "parent delete must be journaled")
	s.True(deleted["bd-cd-child"], "cascade-deleted child must be journaled, got %v", deleted)
}

// TestEventsJournal_DeleteRelationshipDeltas proves a cursor consumer sees
// every edge removal before the nodes disappear, including both incoming and
// outgoing relationships in a multi-node UOW delete.
func (s *testSuite) TestEventsJournal_DeleteRelationshipDeltas() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()

	for _, id := range []string{"bd-del-center", "bd-del-in", "bd-del-out"} {
		s.Require().NoError(ir.Insert(ctx, newTestIssue(id, "t"), "actor", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-del-center", DependsOnID: "bd-del-out", Type: types.DepBlocks,
	}, "actor", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-del-in", DependsOnID: "bd-del-center", Type: types.DepRelated,
	}, "actor", domain.DepInsertOpts{}))

	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	_, err = s.issueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
		IDs: []string{"bd-del-center", "bd-del-in", "bd-del-out"},
	}, "actor")
	s.Require().NoError(err)

	got := s.readJournal()
	s.Require().Len(got, 5, "two edge removals followed by three node deletes: %+v", got)
	for i := 0; i < 2; i++ {
		s.Equal("dep_remove", got[i].Op, "edge delta %d must precede node deletes", i)
	}
	for i := 2; i < len(got); i++ {
		s.Equal("delete", got[i].Op, "row %d", i)
	}
	s.Require().NotNil(got[0].Dep)
	s.Require().NotNil(got[1].Dep)
	s.Equal("bd-del-center", got[0].IssueID)
	s.Equal("bd-del-out", got[0].Dep.Target)
	s.Equal("bd-del-in", got[1].IssueID)
	s.Equal("bd-del-center", got[1].Dep.Target)
}

// TestEventsJournal_NoPhantomDeletes asserts DeleteByIDs journals a delete
// only for ids that actually removed a row — never a phantom delete for an id
// that matched nothing (the batched DELETE reports only a per-batch total).
func (s *testSuite) TestEventsJournal_NoPhantomDeletes() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-pd-1", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-pd-2", "t"), "actor", domain.InsertIssueOpts{}))
	// clear setup rows so we assert only on the delete.
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	// Delete a mix: two real ids and two that do not exist.
	n, err := ir.DeleteByIDs(ctx, []string{"bd-pd-1", "bd-pd-missing-a", "bd-pd-2", "bd-pd-missing-b"}, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(2, n, "only the two present ids are deleted")

	deleted := map[string]bool{}
	for _, r := range s.readJournal() {
		s.Equal("delete", r.Op, "only delete rows expected: %+v", r)
		deleted[r.IssueID] = true
	}
	s.Equal(map[string]bool{"bd-pd-1": true, "bd-pd-2": true}, deleted,
		"journal must record a delete only for ids that removed a row, no phantoms")
}

// TestEventsJournal_DisabledWritesNothing asserts the default-off knob writes
// no rows.
func (s *testSuite) TestEventsJournal_DisabledWritesNothing() {
	s.journalEnabled = false
	ctx := s.Ctx()
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-off-1", "t"), "actor", domain.InsertIssueOpts{}))

	var n int
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&n))
	s.Equal(0, n, "disabled journal must write nothing")
}

// TestEventsJournal_UOWDependencySnapshotsFollowBlockedState mirrors the
// direct-store guard at the UOW repository seam. The event snapshot must be
// taken after is_blocked maintenance, including same-type metadata refreshes.
func (s *testSuite) TestEventsJournal_UOWDependencySnapshotsFollowBlockedState() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-uj-source", "bd-uj-target"} {
		s.Require().NoError(ir.Insert(ctx, newTestIssue(id, id), "actor", domain.InsertIssueOpts{}))
	}
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	add := func(metadata string) {
		s.T().Helper()
		s.Require().NoError(dr.Insert(ctx, &types.Dependency{
			IssueID: "bd-uj-source", DependsOnID: "bd-uj-target", Type: types.DepBlocks, Metadata: metadata,
		}, "actor", domain.DepInsertOpts{}))
	}
	last := func() journalRow {
		s.T().Helper()
		rows := s.readJournal()
		s.Require().NotEmpty(rows)
		return rows[len(rows)-1]
	}

	add(`{"revision":"A"}`)
	row := last()
	s.Require().True(row.Issue.IsBlocked)
	s.Require().NotNil(row.Dep)
	s.Equal(`{"revision":"A"}`, row.Dep.Metadata)

	add(`{"revision":"B"}`)
	row = last()
	s.Require().True(row.Issue.IsBlocked)
	s.Require().NotNil(row.Dep)
	s.Equal(`{"revision":"B"}`, row.Dep.Metadata)

	_, err = dr.Delete(ctx, "bd-uj-source", "bd-uj-target", "actor", domain.DepInsertOpts{})
	s.Require().NoError(err)
	row = last()
	s.Require().False(row.Issue.IsBlocked)
	s.Require().NotNil(row.Dep)
	s.Equal(`{"revision":"B"}`, row.Dep.Metadata)
}
