package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateRefusesUnpoppedClosePolicyOverride is the proxied funnel's half of
// the reserved-key transport pin. This repository's field allowlist is a
// separate map from the embedded funnel's, so it gets its own proof that the
// override is not a column here either: a malformed one survives the pop and is
// refused by name, a well-formed one is popped and leaves no trace.
func (s *testSuite) TestUpdateRefusesUnpoppedClosePolicyOverride() {
	created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			ID:        "bd-domain-unpopped-override",
			Title:     "override transport",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}, "tester")
	s.Require().NoError(err)
	id := created.Issue.ID

	err = NewIssueSQLRepository(s.Runner()).Update(s.Ctx(), id, map[string]any{
		"priority":                  1,
		issueops.OpForceClosePolicy: "yes",
	}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err, "Update accepted a malformed close-policy override")
	s.Contains(err.Error(), "is not allowed")
	s.Contains(err.Error(), issueops.OpForceClosePolicy)

	unchanged, err := s.issueUseCase().GetIssue(s.Ctx(), id)
	s.Require().NoError(err)
	s.Equal(2, unchanged.Priority, "a refused update must write nothing")

	s.Require().NoError(NewIssueSQLRepository(s.Runner()).Update(s.Ctx(), id, map[string]any{
		"priority":                  1,
		issueops.OpForceClosePolicy: true,
	}, "tester", domain.IssueTableOpts{}))
	applied, err := s.issueUseCase().GetIssue(s.Ctx(), id)
	s.Require().NoError(err)
	s.Equal(1, applied.Priority)
	s.Equal(types.StatusOpen, applied.Status)
}
