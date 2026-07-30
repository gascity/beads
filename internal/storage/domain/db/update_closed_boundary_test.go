package db

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) issueUpdateClosedBoundary() {
	ctx := s.Ctx()
	issue := newTestIssue("boundary-repository", "before")
	s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))

	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	repo := NewIssueSQLRepository(tx)
	before, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	err = repo.Update(ctx, issue.ID, map[string]any{"title": "must not persist", "status": types.StatusClosed}, "tester", domain.IssueTableOpts{})
	s.ErrorIs(err, storage.ErrClosedBoundary)
	after, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(before.Title, after.Title)
	s.Equal(before.Status, after.Status)
	s.Equal(before.RowVersion, after.RowVersion)
	s.Require().NoError(tx.Rollback())
}

func (s *testSuite) issueUpdateCustomDoneToDone() {
	ctx := s.Ctx()
	const (
		targetID   = "boundary-custom-done-target"
		dependerID = "boundary-custom-done-depender"
		fromStatus = "archived"
		toStatus   = "retired"
	)
	for _, issue := range []*types.Issue{newTestIssue(targetID, targetID), newTestIssue(dependerID, dependerID)} {
		s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(s.depRepo().Insert(ctx, &types.Dependency{IssueID: dependerID, DependsOnID: targetID, Type: types.DepBlocks}, "tester", domain.DepInsertOpts{}))
	for _, status := range []string{fromStatus, toStatus} {
		_, err := s.Runner().ExecContext(ctx, "INSERT INTO custom_statuses (name, category) VALUES (?, ?)", status, string(types.CategoryDone))
		s.Require().NoError(err)
	}
	_, err := s.issueUseCase().CloseIssue(ctx, targetID, domain.CloseIssueParams{Reason: "historical close", Session: "old-session"}, "tester")
	s.Require().NoError(err)
	s.Require().NoError(s.issueRepo().Update(ctx, targetID, map[string]any{"status": fromStatus}, "tester", domain.IssueTableOpts{}))
	before, err := s.issueRepo().Get(ctx, targetID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NotNil(before.ClosedAt)
	var beforeEvents int
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", targetID).Scan(&beforeEvents))

	s.Require().NoError(s.issueRepo().Update(ctx, targetID, map[string]any{"status": toStatus}, "tester", domain.IssueTableOpts{}))
	after, err := s.issueRepo().Get(ctx, targetID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(types.Status(toStatus), after.Status)
	s.Require().NotNil(after.ClosedAt)
	s.True(after.ClosedAt.Equal(*before.ClosedAt))
	s.Equal(before.CloseReason, after.CloseReason)
	s.Equal(before.ClosedBySession, after.ClosedBySession)
	s.NotEqual(before.RowVersion, after.RowVersion)
	var closed, reopened, statusChanged, total int
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", targetID, string(types.EventClosed)).Scan(&closed))
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", targetID, string(types.EventReopened)).Scan(&reopened))
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", targetID, string(types.EventStatusChanged)).Scan(&statusChanged))
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", targetID).Scan(&total))
	s.Equal(beforeEvents+1, total)
	s.Equal(1, closed)
	s.Equal(0, reopened)
	s.Equal(2, statusChanged)
	var dependerBlocked bool
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", dependerID).Scan(&dependerBlocked))
	s.False(dependerBlocked)
}

func (s *testSuite) issueUpdateSameStatusInUnitOfWork() {
	ctx := s.Ctx()
	issue := newTestIssue("same-status-uow", "before")
	s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))
	var eventsBefore int
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issue.ID).Scan(&eventsBefore))

	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	repo := NewIssueSQLRepository(tx)
	before, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(repo.Update(ctx, issue.ID, map[string]any{"status": types.StatusOpen}, "tester", domain.IssueTableOpts{}))
	afterNoop, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(before.RowVersion, afterNoop.RowVersion)
	var eventsAfterNoop, statusChangedAfterNoop int
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issue.ID).Scan(&eventsAfterNoop))
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", issue.ID, string(types.EventStatusChanged)).Scan(&statusChangedAfterNoop))
	s.Equal(eventsBefore, eventsAfterNoop)
	s.Zero(statusChangedAfterNoop)

	s.Require().NoError(repo.Update(ctx, issue.ID, map[string]any{"status": types.StatusOpen, "title": "after"}, "tester", domain.IssueTableOpts{}))
	afterScalar, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("after", afterScalar.Title)
	s.NotEqual(afterNoop.RowVersion, afterScalar.RowVersion)
	var updated, statusChanged int
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", issue.ID, string(types.EventUpdated)).Scan(&updated))
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", issue.ID, string(types.EventStatusChanged)).Scan(&statusChanged))
	s.Equal(1, updated)
	s.Zero(statusChanged)
	s.Require().NoError(tx.Commit())
}

func (s *testSuite) issueUpdatePreservesCallerMap() {
	ctx := s.Ctx()
	issue := newTestIssue("preserve-caller-map", "before")
	s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))
	updates := map[string]any{"status": types.StatusOpen}

	s.Require().NoError(s.issueRepo().Update(ctx, issue.ID, updates, "tester", domain.IssueTableOpts{}))
	s.Equal(map[string]any{"status": types.StatusOpen}, updates)
}

func (s *testSuite) TestIssueUseCase_ClosedBoundaryRollback() {
	ctx := s.Ctx()
	issue := newTestIssue("boundary-uow", "before")
	s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))

	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	uc := boundaryIssueUseCase(tx)
	_, err = uc.ApplyUpdate(ctx, issue.ID, domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"title": "must not persist", "status": types.StatusClosed},
	}, "tester")
	if !errors.Is(err, storage.ErrClosedBoundary) {
		s.T().Fatalf("ApplyUpdate error = %v, want ErrClosedBoundary", err)
	}
	s.Require().NoError(tx.Rollback())

	got, err := s.issueRepo().Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("before", got.Title)
	s.Equal(types.StatusOpen, got.Status)
	s.Empty(got.Assignee, "claim must roll back with the boundary refusal")

	closed := newTestIssue("boundary-uow-leave", "closed before update")
	s.Require().NoError(s.issueRepo().Insert(ctx, closed, "tester", domain.InsertIssueOpts{}))
	_, err = s.issueUseCase().CloseIssue(ctx, closed.ID, domain.CloseIssueParams{Reason: "done", Session: "session"}, "tester")
	s.Require().NoError(err)

	leaveTx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	leaveUC := boundaryIssueUseCase(leaveTx)
	_, err = leaveUC.ApplyUpdate(ctx, closed.ID, domain.UpdateSpec{Fields: map[string]any{"title": "must not persist", "status": types.StatusOpen}}, "tester")
	s.ErrorIs(err, storage.ErrClosedBoundary)
	s.Require().NoError(leaveTx.Rollback())
	stillClosed, err := s.issueRepo().Get(ctx, closed.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("closed before update", stillClosed.Title)
	s.Equal(types.StatusClosed, stillClosed.Status)
	s.Require().NotNil(stillClosed.ClosedAt)
}

func boundaryIssueUseCase(runner Runner) domain.IssueUseCase {
	issueRepo := NewIssueSQLRepository(runner)
	depRepo := NewDependencySQLRepository(runner)
	labelRepo := NewLabelSQLRepository(runner)
	labelUseCase := domain.NewLabelUseCase(labelRepo)
	dependencyUseCase := domain.NewDependencyUseCase(depRepo)
	return domain.NewIssueUseCase(
		issueRepo,
		depRepo,
		labelRepo,
		NewChildCounterSQLRepository(runner),
		NewCommentSQLRepository(runner),
		NewConfigSQLRepository(runner),
		NewEventsSQLRepository(runner),
		labelUseCase,
		dependencyUseCase,
	)
}
