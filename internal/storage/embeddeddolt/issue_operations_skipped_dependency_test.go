//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedIssueOperationsCreateRejectsMissingDependencyTargets pins the
// facade create against silently dropping an edge whose target does not
// exist. The batch engine tolerates that for imports; a guarded single create
// must refuse the whole request instead of reporting success on an issue whose
// requested relationships were never written.
func TestEmbeddedIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "skipdep")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	seed := &types.Issue{ID: "skipdep-seed", Title: "seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, seed, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		id      string
		request publicops.CreateRequest
		target  string
	}{
		{
			name:   "explicit dependency",
			id:     "skipdep-explicit",
			target: "skipdep-missing-dep",
			request: publicops.CreateRequest{
				Dependencies: []publicops.CreateDependency{{TargetID: "skipdep-missing-dep", Type: publicops.DepBlocks}},
			},
		},
		{
			name:    "parent",
			id:      "skipdep-parent",
			target:  "skipdep-missing-parent",
			request: publicops.CreateRequest{ParentID: "skipdep-missing-parent"},
		},
		{
			name:   "waits-for spawner",
			id:     "skipdep-waits",
			target: "skipdep-missing-spawner",
			request: publicops.CreateRequest{
				WaitsFor: &publicops.WaitsFor{SpawnerID: "skipdep-missing-spawner"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			request.Actor = "writer"
			request.Issue = &types.Issue{ID: tc.id, Title: tc.name, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			_, err := operations.Create(ctx, request)
			if err == nil {
				t.Fatal("Create returned nil error, want a refusal for the missing dependency target")
			}
			if !errors.Is(err, publicops.ErrNotFound) {
				t.Errorf("Create error = %v, want ErrNotFound", err)
			}
			if !errors.Is(err, publicops.ErrValidation) {
				t.Errorf("Create error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("Create error = %v, want it to name the missing target %q", err, tc.target)
			}
			te.assertRowNotExists(t, ctx, "issues", tc.id)
			te.assertRowNotExists(t, ctx, "wisps", tc.id)
		})
	}

	// A create whose targets all exist is unaffected.
	result, err := operations.Create(ctx, publicops.CreateRequest{
		Actor:        "writer",
		Issue:        &types.Issue{ID: "skipdep-ok", Title: "ok", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		Dependencies: []publicops.CreateDependency{{TargetID: seed.ID, Type: publicops.DepBlocks}},
	})
	if err != nil {
		t.Fatalf("Create with existing target: %v", err)
	}
	if len(result.Issue.Dependencies) != 1 || result.Issue.Dependencies[0].DependsOnID != seed.ID {
		t.Fatalf("Create result dependencies = %#v, want one edge to %s", result.Issue.Dependencies, seed.ID)
	}
}
