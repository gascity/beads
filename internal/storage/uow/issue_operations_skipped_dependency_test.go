package uow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestIssueOperationsCreateRejectsMissingDependencyTargets pins the
// unit-of-work facade create against reporting success for an issue whose
// requested relationships were never written. A create naming a parent,
// waits-for spawner, or explicit dependency target that does not exist must
// fail with a typed refusal that names the target, and must leave no rows
// behind.
func TestIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	seed, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{ID: "bd-skipdep-seed", Title: "seed", Status: issueops.StatusOpen, Priority: 2, IssueType: issueops.TypeTask},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		id      string
		request issueops.CreateRequest
		target  string
	}{
		{
			name:   "explicit dependency",
			id:     "bd-skipdep-explicit",
			target: "bd-skipdep-missing-dep",
			request: issueops.CreateRequest{
				Dependencies: []issueops.CreateDependency{{TargetID: "bd-skipdep-missing-dep", Type: issueops.DepBlocks}},
			},
		},
		{
			name:    "parent",
			id:      "bd-skipdep-parent",
			target:  "bd-skipdep-missing-parent",
			request: issueops.CreateRequest{ParentID: "bd-skipdep-missing-parent"},
		},
		{
			name:   "waits-for spawner",
			id:     "bd-skipdep-waits",
			target: "bd-skipdep-missing-spawner",
			request: issueops.CreateRequest{
				WaitsFor: &issueops.WaitsFor{SpawnerID: "bd-skipdep-missing-spawner"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			request.Actor = "writer"
			request.Issue = &issueops.Issue{ID: tc.id, Title: tc.name, Status: issueops.StatusOpen, Priority: 2, IssueType: issueops.TypeTask}
			_, err := operations.Create(ctx, request)
			if err == nil {
				t.Fatal("Create returned nil error, want a refusal for the missing dependency target")
			}
			if !errors.Is(err, issueops.ErrNotFound) {
				t.Errorf("Create error = %v, want ErrNotFound", err)
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("Create error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("Create error = %v, want it to name the missing target %q", err, tc.target)
			}
			assertUOWRowMissing(t, ctx, provider, "issues", tc.id)
			assertUOWRowMissing(t, ctx, provider, "wisps", tc.id)
		})
	}

	// A create whose targets all exist is unaffected.
	result, err := operations.Create(ctx, issueops.CreateRequest{
		Actor:        "writer",
		Issue:        &issueops.Issue{ID: "bd-skipdep-ok", Title: "ok", Status: issueops.StatusOpen, Priority: 2, IssueType: issueops.TypeTask},
		Dependencies: []issueops.CreateDependency{{TargetID: seed.Issue.ID, Type: issueops.DepBlocks}},
	})
	if err != nil {
		t.Fatalf("Create with existing target: %v", err)
	}
	if len(result.Issue.Dependencies) != 1 || result.Issue.Dependencies[0].DependsOnID != seed.Issue.ID {
		t.Fatalf("Create result dependencies = %#v, want one edge to %s", result.Issue.Dependencies, seed.Issue.ID)
	}
}
