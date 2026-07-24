//go:build cgo

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Two workspaces with different config prefixes over ONE database (both listed
// in allowed_prefixes) mint side-by-side without collision — on `bd create`
// (store.CreateIssue) AND a non-create surface (RunInTransaction, the batch /
// molecule path). This is the load-bearing red-team pin for the prerequisite
// slice.
func TestWorkspacePrefixMint_SideBySide(t *testing.T) {
	tmpDir := t.TempDir()
	raw := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "cityhq")
	ctx := context.Background()

	if err := raw.SetConfig(ctx, "allowed_prefixes", "riga,rigb"); err != nil {
		t.Fatalf("set allowed_prefixes: %v", err)
	}

	storeA := storage.NewPrefixMintingStore(raw, "riga")
	storeB := storage.NewPrefixMintingStore(raw, "rigb")

	// Workspace A via the create surface.
	issueA := &types.Issue{Title: "work A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := storeA.CreateIssue(ctx, issueA, "tester"); err != nil {
		t.Fatalf("workspace A CreateIssue: %v", err)
	}

	// Workspace B via a non-create surface (transaction, like batch/molecule).
	issueB := &types.Issue{Title: "work B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	err := storeB.RunInTransaction(ctx, "test: batch create", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, issueB, "tester")
	})
	if err != nil {
		t.Fatalf("workspace B RunInTransaction: %v", err)
	}

	if !strings.HasPrefix(issueA.ID, "riga-") {
		t.Errorf("workspace A minted %q; want a riga- prefix", issueA.ID)
	}
	if !strings.HasPrefix(issueB.ID, "rigb-") {
		t.Errorf("workspace B minted %q; want a rigb- prefix", issueB.ID)
	}
	if issueA.ID == issueB.ID {
		t.Errorf("collision: both workspaces minted %q", issueA.ID)
	}

	// Both rows are present in the one shared database.
	for _, id := range []string{issueA.ID, issueB.ID} {
		if _, err := raw.GetIssue(ctx, id); err != nil {
			t.Errorf("GetIssue(%q) from shared db: %v", id, err)
		}
	}
}

// Backward compatibility: with no allowed_prefixes (or a prefix that is not
// listed), minting uses the database prefix exactly as before.
func TestWorkspacePrefixMint_BackwardCompatible(t *testing.T) {
	tmpDir := t.TempDir()
	raw := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "cityhq")
	ctx := context.Background()

	// No allowed_prefixes row at all.
	store := storage.NewPrefixMintingStore(raw, "riga")
	issue := &types.Issue{Title: "legacy", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if !strings.HasPrefix(issue.ID, "cityhq-") {
		t.Errorf("minted %q; want the db prefix cityhq- (no allowed_prefixes)", issue.ID)
	}

	// allowed_prefixes present but does not list the workspace prefix.
	if err := raw.SetConfig(ctx, "allowed_prefixes", "rigb,rigc"); err != nil {
		t.Fatalf("set allowed_prefixes: %v", err)
	}
	store2 := storage.NewPrefixMintingStore(raw, "riga")
	issue2 := &types.Issue{Title: "legacy2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store2.CreateIssue(ctx, issue2, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if !strings.HasPrefix(issue2.ID, "cityhq-") {
		t.Errorf("minted %q; want cityhq- (riga not in allowed_prefixes)", issue2.ID)
	}
}

// A no-deps create of an infra-type bead must NOT adopt the bare workspace
// prefix: the store promotes infra types to wisps, so it must keep the
// '<db-prefix>-wisp' ID infix that the wisp fast paths key on.
func TestWorkspacePrefixMint_SkipsInfraTypeNoDeps(t *testing.T) {
	tmpDir := t.TempDir()
	raw := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "cityhq")
	ctx := context.Background()

	if err := raw.SetConfig(ctx, "allowed_prefixes", "riga"); err != nil {
		t.Fatalf("set allowed_prefixes: %v", err)
	}
	// newTestStoreWithPrefix configures "message" as a custom (infra) type.
	store := storage.NewPrefixMintingStore(raw, "riga")

	infra := &types.Issue{Title: "m", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("message")}
	if err := store.CreateIssue(ctx, infra, "tester"); err != nil {
		t.Fatalf("CreateIssue infra type: %v", err)
	}
	if !strings.HasPrefix(infra.ID, "cityhq-wisp-") {
		t.Errorf("infra-type bead minted %q; want a cityhq-wisp- prefix (not the bare workspace prefix)", infra.ID)
	}
	if strings.HasPrefix(infra.ID, "riga-") {
		t.Errorf("infra-type wisp masqueraded under the workspace prefix: %q", infra.ID)
	}
}

// After importing high-numbered IDs under a foreign prefix (in allowed_prefixes),
// the next counter-mode mint under that prefix lands strictly above the imported
// maximum.
func TestWorkspacePrefixMint_ContinuesAboveImportedMax(t *testing.T) {
	tmpDir := t.TempDir()
	raw := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "cityhq")
	ctx := context.Background()

	if err := raw.SetConfig(ctx, "allowed_prefixes", "riga"); err != nil {
		t.Fatalf("set allowed_prefixes: %v", err)
	}
	if err := raw.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set issue_id_mode: %v", err)
	}

	// Import a high-numbered foreign-prefix ID (allowed, so it is accepted).
	imported := &types.Issue{ID: "riga-500", Title: "imported", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := raw.CreateIssue(ctx, imported, "tester"); err != nil {
		t.Fatalf("import riga-500: %v", err)
	}

	// Next mint under the riga workspace must be strictly above 500.
	store := storage.NewPrefixMintingStore(raw, "riga")
	next := &types.Issue{Title: "next", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, next, "tester"); err != nil {
		t.Fatalf("mint next riga: %v", err)
	}
	if next.ID != "riga-501" {
		t.Errorf("next mint = %q; want riga-501 (strictly above the imported max)", next.ID)
	}
}

// --conflict-skip leaves an existing row COMPLETELY untouched — row fields,
// labels, comments, AND dependency edges — and reports its ID.
func TestImportConflictSkip_LeavesExistingUntouched(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "cs")
	ctx := context.Background()

	// Seed cs-1 with a label, a comment, and a blocks-on edge to cs-2.
	for _, id := range []string{"cs-1", "cs-2", "cs-3"} {
		seed := &types.Issue{ID: id, Title: "original " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := st.CreateIssue(ctx, seed, "tester"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := st.AddLabel(ctx, "cs-1", "orig-label", "tester"); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	if _, err := st.AddIssueComment(ctx, "cs-1", "tester", "orig comment"); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if err := st.AddDependency(ctx, &types.Dependency{IssueID: "cs-1", DependsOnID: "cs-2", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}

	// Incoming cs-1 is NEWER (so the stale guard would otherwise overwrite it)
	// and carries a different title, label, comment, and dependency edge.
	incoming := &types.Issue{
		ID:           "cs-1",
		Title:        "changed by import",
		Status:       types.StatusOpen,
		Priority:     2,
		IssueType:    types.TypeTask,
		UpdatedAt:    time.Now().Add(24 * time.Hour),
		Labels:       []string{"import-label"},
		Comments:     []*types.Comment{{Author: "importer", Text: "import comment"}},
		Dependencies: []*types.Dependency{{IssueID: "cs-1", DependsOnID: "cs-3", Type: types.DepBlocks}},
	}
	res, err := importIssuesCore(ctx, "", st, []*types.Issue{incoming}, ImportOptions{
		SkipPrefixValidation: true,
		ConflictSkip:         true,
		ConflictSkipStrict:   true, // the user-facing `bd import --conflict-skip` contract
	})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}

	if res.Created != 0 {
		t.Errorf("Created = %d; want 0 (existing row skipped)", res.Created)
	}
	if len(res.ConflictSkippedIDs) != 1 || res.ConflictSkippedIDs[0] != "cs-1" {
		t.Errorf("ConflictSkippedIDs = %v; want [cs-1]", res.ConflictSkippedIDs)
	}
	if res.Updated != 0 {
		t.Errorf("Updated = %d; want 0 (conflict-skip never overwrites)", res.Updated)
	}

	got, err := st.GetIssue(ctx, "cs-1")
	if err != nil {
		t.Fatalf("GetIssue cs-1: %v", err)
	}
	if got.Title != "original cs-1" {
		t.Errorf("cs-1 title = %q; want unchanged 'original cs-1'", got.Title)
	}

	labels, err := st.GetLabels(ctx, "cs-1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "orig-label" {
		t.Errorf("cs-1 labels = %v; want [orig-label] (import label must not merge)", labels)
	}

	comments, err := st.GetIssueComments(ctx, "cs-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 1 || comments[0].Text != "orig comment" {
		t.Errorf("cs-1 comments = %d entries; want the single original comment", len(comments))
	}

	deps, err := st.GetDependencyRecords(ctx, "cs-1")
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "cs-2" {
		t.Errorf("cs-1 deps = %+v; want a single blocks-on cs-2 (import edge to cs-3 must not merge)", deps)
	}
}

// Non-strict ConflictSkip (the auto-import upgrade-recovery path) leaves the
// existing ROW untouched but still merges aux additively — the historical
// behavior the user-facing strict flag deliberately does NOT share.
func TestImportConflictSkip_NonStrictMergesAux(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "ns")
	ctx := context.Background()

	existing := &types.Issue{ID: "ns-1", Title: "original", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := st.CreateIssue(ctx, existing, "tester"); err != nil {
		t.Fatalf("seed ns-1: %v", err)
	}
	if err := st.AddLabel(ctx, "ns-1", "orig-label", "tester"); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	incoming := &types.Issue{
		ID: "ns-1", Title: "changed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		UpdatedAt: time.Now().Add(24 * time.Hour), Labels: []string{"merged-label"},
	}
	if _, err := importIssuesCore(ctx, "", st, []*types.Issue{incoming}, ImportOptions{
		SkipPrefixValidation: true,
		ConflictSkip:         true, // ConflictSkipStrict intentionally false (auto-import path)
	}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}

	got, err := st.GetIssue(ctx, "ns-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "original" {
		t.Errorf("ns-1 title = %q; want unchanged 'original' (row never overwritten)", got.Title)
	}
	labels, _ := st.GetLabels(ctx, "ns-1")
	set := map[string]bool{}
	for _, l := range labels {
		set[l] = true
	}
	if !set["orig-label"] || !set["merged-label"] {
		t.Errorf("ns-1 labels = %v; want both orig-label and merged-label (non-strict merges aux)", labels)
	}
}

// A plain import (no --conflict-skip) whose chunked deferred-dependency pass
// re-submits an existing row must NOT report that row as conflict-skipped: the
// row was genuinely created in phase 1, and the phase-2 ConflictSkip is an
// internal idempotency detail.
func TestImportPlain_ChunkedDeferredDeps_NoConflictMisreport(t *testing.T) {
	oldChunk := importChunkSize
	importChunkSize = 2
	t.Cleanup(func() { importChunkSize = oldChunk })

	tmpDir := t.TempDir()
	st := newTestStoreWithPrefix(t, filepath.Join(tmpDir, ".beads", "beads.db"), "pi")
	ctx := context.Background()

	var issues []*types.Issue
	for i := 1; i <= 4; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("pi-%d", i),
			Title:     fmt.Sprintf("issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			UpdatedAt: time.Now(),
		})
	}
	// A non-readiness edge from the first chunk to the last forces the
	// deferred-dependency pass (which re-submits pi-1 with ConflictSkip).
	issues[0].Dependencies = []*types.Dependency{{IssueID: "pi-1", DependsOnID: "pi-4", Type: types.DepRelated}}

	res, err := importIssuesCore(ctx, "", st, issues, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if res.Created != 4 {
		t.Errorf("Created = %d; want 4", res.Created)
	}
	if len(res.ConflictSkippedIDs) != 0 {
		t.Errorf("plain import misreported %v as conflict-skipped (deferred-dep pass leaked the callback)", res.ConflictSkippedIDs)
	}
}

// Concurrent add-to-set appends of different values are lossless: two
// transactions that BOTH read before either commits (forced via a rendezvous
// hook) still converge with both values present, because RunInTransaction
// retries the serialization conflict. Uses an isolated per-test database so the
// two goroutines run on distinct pooled connections (the shared-branch fast
// path clamps MaxOpenConns=1 and would serialize them).
func TestConfigAddToSet_ConcurrentAppendsSurvive(t *testing.T) {
	tmpDir := t.TempDir()
	st := newTestStoreIsolatedDB(t, filepath.Join(tmpDir, ".beads", "beads.db"), "base")
	ctx := context.Background()

	if err := st.SetConfig(ctx, "allowed_prefixes", "base"); err != nil {
		t.Fatalf("seed allowed_prefixes: %v", err)
	}

	// Force both transactions to read the pre-image before either writes, so
	// their commits genuinely conflict (1213) and exercise the retry.
	var arrived int32
	proceed := make(chan struct{})
	addToSetAfterReadHook = func() {
		if atomic.AddInt32(&arrived, 1) == 2 {
			close(proceed)
		}
		select {
		case <-proceed:
		case <-time.After(5 * time.Second):
		}
	}
	t.Cleanup(func() { addToSetAfterReadHook = nil })

	appenders := []string{"riga", "rigb"}
	var wg sync.WaitGroup
	errs := make([]error, len(appenders))
	for i, v := range appenders {
		wg.Add(1)
		go func(i int, v string) {
			defer wg.Done()
			_, _, err := addToSetInStore(ctx, st, "allowed_prefixes", []string{v})
			errs[i] = err
		}(i, v)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent append %q: %v", appenders[i], err)
		}
	}

	// Disable the rendezvous for the follow-up single-threaded assertions.
	addToSetAfterReadHook = nil

	final, err := st.GetConfig(ctx, "allowed_prefixes")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	set := make(map[string]bool)
	for _, tok := range strings.Split(final, ",") {
		set[strings.TrimSpace(tok)] = true
	}
	for _, want := range []string{"base", "riga", "rigb"} {
		if !set[want] {
			t.Errorf("final allowed_prefixes %q missing %q (lost update)", final, want)
		}
	}

	// A duplicate append is a no-op that changes nothing and drops nothing.
	merged, added, err := addToSetInStore(ctx, st, "allowed_prefixes", []string{"riga"})
	if err != nil {
		t.Fatalf("duplicate append: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("duplicate append added %v; want none", added)
	}
	if !strings.Contains(merged, "base") {
		t.Errorf("duplicate append dropped existing entries: %q", merged)
	}
}
