package tracker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestApplyPullIssueUpdateUsesLifecycleAtDoneBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		initial   types.Status
		target    types.Status
		wantClose int
		wantOpen  int
	}{
		{name: "close", initial: types.StatusOpen, target: types.StatusClosed, wantClose: 1},
		{name: "reopen", initial: types.StatusClosed, target: types.StatusOpen, wantOpen: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, boundaryErr := issueops.GuardClosedBoundaryInTx(context.Background(), nil, test.initial, test.target)
			if !errors.Is(boundaryErr, storage.ErrClosedBoundary) {
				t.Fatalf("boundary error = %v", boundaryErr)
			}
			issue := &types.Issue{ID: "pull-boundary", Title: "local", Status: test.initial}
			if test.initial == types.StatusClosed {
				now := time.Now().UTC()
				issue.ClosedAt = &now
			}
			tx := &pullBoundaryTransaction{issue: issue, boundaryErr: boundaryErr}
			err := applyPullIssueUpdate(context.Background(), tx, issue.ID, map[string]interface{}{
				"title":  "remote",
				"status": test.target,
			}, nil, "sync")
			if err != nil {
				t.Fatalf("applyPullIssueUpdate: %v", err)
			}
			if tx.closeCalls != test.wantClose || tx.reopenCalls != test.wantOpen {
				t.Fatalf("lifecycle calls = close:%d reopen:%d, want close:%d reopen:%d", tx.closeCalls, tx.reopenCalls, test.wantClose, test.wantOpen)
			}
			if tx.issue.Status != test.target || tx.issue.Title != "remote" {
				t.Fatalf("updated issue = %+v, want remote %q", tx.issue, test.target)
			}
			if test.target == types.StatusClosed && tx.issue.ClosedAt == nil {
				t.Fatal("close fallback did not set closed_at")
			}
			if test.target == types.StatusOpen && tx.issue.ClosedAt != nil {
				t.Fatal("reopen fallback retained closed_at")
			}
		})
	}
}

func TestApplyPullIssueUpdateRejectsUntypedBoundary(t *testing.T) {
	tx := &pullBoundaryTransaction{issue: &types.Issue{ID: "pull-boundary", Status: types.StatusOpen}, boundaryErr: storage.ErrClosedBoundary}
	err := applyPullIssueUpdate(context.Background(), tx, tx.issue.ID, map[string]interface{}{"status": types.StatusClosed}, nil, "sync")
	if !errors.Is(err, storage.ErrClosedBoundary) {
		t.Fatalf("error = %v, want ErrClosedBoundary", err)
	}
	if tx.closeCalls != 0 || tx.reopenCalls != 0 || tx.issue.Status != types.StatusOpen {
		t.Fatalf("untyped boundary mutated transaction: %+v", tx)
	}
}

type pullBoundaryTransaction struct {
	storage.Transaction
	issue       *types.Issue
	boundaryErr error
	addLabelErr error
	closeCalls  int
	reopenCalls int
}

func (t *pullBoundaryTransaction) UpdateIssue(_ context.Context, _ string, updates map[string]interface{}, _ string) error {
	if _, hasStatus := updates["status"]; hasStatus && t.boundaryErr != nil {
		return t.boundaryErr
	}
	if title, ok := updates["title"].(string); ok {
		t.issue.Title = title
	}
	if status, ok := updates["status"]; ok {
		switch value := status.(type) {
		case types.Status:
			t.issue.Status = value
		case string:
			t.issue.Status = types.Status(value)
		}
	}
	return nil
}

func (t *pullBoundaryTransaction) CloseIssue(_ context.Context, _ string, _ string, _ string, _ string) error {
	t.closeCalls++
	now := time.Now().UTC()
	t.issue.Status = types.StatusClosed
	t.issue.ClosedAt = &now
	return nil
}

func (t *pullBoundaryTransaction) ReopenIssueWithResult(_ context.Context, _ string, _ string, _ string) (bool, error) {
	t.reopenCalls++
	t.issue.Status = types.StatusOpen
	t.issue.ClosedAt = nil
	return true, nil
}

func (t *pullBoundaryTransaction) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return t.issue, nil
}

func (t *pullBoundaryTransaction) GetLabels(context.Context, string) ([]string, error) {
	return append([]string(nil), t.issue.Labels...), nil
}
func (t *pullBoundaryTransaction) AddLabel(_ context.Context, _ string, label string, _ string) error {
	if t.addLabelErr != nil {
		return t.addLabelErr
	}
	for _, existing := range t.issue.Labels {
		if existing == label {
			return nil
		}
	}
	t.issue.Labels = append(t.issue.Labels, label)
	return nil
}
func (t *pullBoundaryTransaction) RemoveLabel(_ context.Context, _ string, label string, _ string) error {
	for i, existing := range t.issue.Labels {
		if existing == label {
			t.issue.Labels = append(t.issue.Labels[:i], t.issue.Labels[i+1:]...)
			break
		}
	}
	return nil
}

type pullFailureStore struct {
	*pureTestStore
	tx      *pullBoundaryTransaction
	commits int
}

func (s *pullFailureStore) RunInTransaction(_ context.Context, _ string, fn func(storage.Transaction) error) error {
	before := *s.tx.issue
	if err := fn(s.tx); err != nil {
		*s.tx.issue = before
		return err
	}
	s.commits++
	return nil
}

func (s *pullFailureStore) RunInIssueLifecycleTransaction(_ context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	before := *s.tx.issue
	if err := fn(s.tx); err != nil {
		*s.tx.issue = before
		return err
	}
	s.commits++
	return nil
}

func (s *pullFailureStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	for _, issue := range s.issues {
		if issue.ExternalRef != nil && *issue.ExternalRef == ref {
			return issue, nil
		}
	}
	return nil, nil
}

func TestEngineSyncFailedPullIsNotPushed(t *testing.T) {
	remoteUpdated := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name         string
		localID      string
		identifier   string
		externalRef  string
		localUpdated time.Time
	}{
		{
			name:         "create-eligible",
			localID:      "pull-boundary-create",
			identifier:   "EXT-CREATE",
			localUpdated: remoteUpdated,
		},
		{
			name:         "update-eligible",
			localID:      "pull-boundary-update",
			identifier:   "EXT-UPDATE",
			externalRef:  "https://test.test/EXT-UPDATE",
			localUpdated: remoteUpdated.Add(time.Hour),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			local := &types.Issue{
				ID:        test.localID,
				Title:     "local",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				UpdatedAt: test.localUpdated,
			}
			if test.externalRef != "" {
				ref := test.externalRef
				local.ExternalRef = &ref
			}
			_, _, boundaryErr := issueops.GuardClosedBoundaryInTx(ctx, nil, types.StatusOpen, types.StatusClosed)
			store := &pullFailureStore{
				pureTestStore: newPureTestStore(local),
				tx: &pullBoundaryTransaction{
					issue: local, boundaryErr: boundaryErr, addLabelErr: errors.New("label write failed"),
				},
			}
			mock := newMockTracker("test")
			mock.issues = []TrackerIssue{{
				ID: test.identifier, Identifier: test.identifier, Title: "remote", UpdatedAt: remoteUpdated,
			}}
			mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
				return &IssueConversion{Issue: &types.Issue{
					ID: test.localID, Title: "remote", Status: types.StatusClosed,
					Priority: 2, IssueType: types.TypeTask, Labels: []string{"remote"},
				}}
			}}

			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, Push: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if result.PullStats.Errors != 1 || result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
				t.Fatalf("PullStats = %+v, want one error and no create/update", result.PullStats)
			}
			if result.PushStats.Created != 0 || result.PushStats.Updated != 0 || len(mock.created) != 0 || len(mock.updated) != 0 {
				t.Fatalf("%s failed pull was pushed: PushStats=%+v create calls=%d update calls=%d",
					test.name, result.PushStats, len(mock.created), len(mock.updated))
			}
			if store.commits != 0 || store.tx.closeCalls != 1 || local.Status != types.StatusOpen ||
				local.Title != "local" || len(local.Labels) != 0 || !local.UpdatedAt.Equal(test.localUpdated) {
				t.Fatalf("%s failed pull committed state: commits=%d closes=%d issue=%+v",
					test.name, store.commits, store.tx.closeCalls, local)
			}
			if test.externalRef == "" {
				if local.ExternalRef != nil {
					t.Fatalf("create-eligible failed pull left external_ref %q", *local.ExternalRef)
				}
			} else if local.ExternalRef == nil || *local.ExternalRef != test.externalRef {
				t.Fatalf("update-eligible failed pull changed external_ref to %v", local.ExternalRef)
			}
		})
	}
}

func TestReimportIssuePreservesLabelsAcrossDoneBoundary(t *testing.T) {
	ctx := context.Background()
	local := &types.Issue{
		ID:        "reimport-boundary",
		Title:     "local",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"keep-local-label"},
	}
	_, _, boundaryErr := issueops.GuardClosedBoundaryInTx(ctx, nil, types.StatusOpen, types.StatusClosed)
	store := &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullBoundaryTransaction{issue: local, boundaryErr: boundaryErr},
	}
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{ID: "EXT-2", Identifier: "EXT-2", Title: "remote"}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			Title: "remote", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask,
		}}
	}}

	engine := NewEngine(mock, store, "sync")
	engine.reimportIssue(ctx, Conflict{IssueID: local.ID, ExternalIdentifier: "EXT-2"})

	if store.commits != 1 || store.tx.closeCalls != 1 {
		t.Fatalf("reimport transaction = commits:%d closes:%d, want 1:1", store.commits, store.tx.closeCalls)
	}
	if local.Status != types.StatusClosed || local.ClosedAt == nil || local.Title != "remote" {
		t.Fatalf("reimport lifecycle result = %+v", local)
	}
	if len(local.Labels) != 1 || local.Labels[0] != "keep-local-label" {
		t.Fatalf("reimport changed local labels: %v", local.Labels)
	}
}
