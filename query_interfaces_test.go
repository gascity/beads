package beads_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// Compile-time proof that the concrete Dolt store satisfies each narrow public
// interface (the embedded Dolt store is asserted in query_interfaces_cgo_test.go).
// These pin the interfaces to a real implementation, complementing the
// engine-interface guards in beads.go.
var (
	_ beads.IssueClaimer     = (*dolt.DoltStore)(nil)
	_ beads.EventQuerier     = (*dolt.DoltStore)(nil)
	_ beads.DependentQuerier = (*dolt.DoltStore)(nil)
)

// TestQueryInterfacesAgainstRealDolt exercises AsEventQuerier / AsDependentQuerier
// (and their methods) against a live Dolt Storage through the public surface,
// the runtime complement to the compile-time guards.
func TestQueryInterfacesAgainstRealDolt(t *testing.T) {
	skipIfNoDoltServer(t)

	ctx := context.Background()
	store, err := beads.Open(ctx, filepath.Join(t.TempDir(), "qi-dolt"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.SetConfig(ctx, "issue_prefix", "qi"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	mk := func(id string) {
		iss := &beads.Issue{ID: id, Title: id, Status: beads.StatusOpen, Priority: 2, IssueType: beads.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	mk("qi-target")
	mk("qi-src")
	if err := store.AddDependency(ctx, &beads.Dependency{IssueID: "qi-src", DependsOnID: "qi-target", Type: beads.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	dq, ok := beads.AsDependentQuerier(store)
	if !ok {
		t.Fatalf("AsDependentQuerier returned ok=false for a live Dolt Storage")
	}
	deps, err := dq.GetDependentRecords(ctx, "qi-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	if len(deps) != 1 || deps[0].IssueID != "qi-src" {
		t.Fatalf("GetDependentRecords = %v, want [qi-src]", deps)
	}
	if n, err := dq.CountDependentRecords(ctx, "qi-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 1 {
		t.Fatalf("CountDependentRecords = %d, want 1", n)
	}

	eq, ok := beads.AsEventQuerier(store)
	if !ok {
		t.Fatalf("AsEventQuerier returned ok=false for a live Dolt Storage")
	}
	evs, err := eq.EventsSince(ctx, beads.EventCursor{}, "", 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(evs) == 0 {
		t.Fatalf("EventsSince returned no durable events after creates")
	}
}

// TestAsAccessorsResolveThroughHookDecorator proves the decorator contract that
// lets the As* accessors use a single direct assertion (no UnwrapStore): a
// HookFiringStore embeds storage.DoltStorage, so the engine-interface narrow
// surfaces (claim / event feed / dependents) promote through it and resolve
// without unwrapping. This is the test that would go red if a future decorator
// stopped forwarding one of them — i.e. the reason the removed fallback was dead
// rather than load-bearing.
func TestAsAccessorsResolveThroughHookDecorator(t *testing.T) {
	skipIfNoDoltServer(t)

	ctx := context.Background()
	store, err := beads.Open(ctx, filepath.Join(t.TempDir(), "deco-dolt"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ds, ok := store.(storage.DoltStorage)
	if !ok {
		t.Fatalf("live Dolt store is not a storage.DoltStorage")
	}
	// nil runner => passthrough decorator; the narrow surfaces are promoted from
	// the embedded engine interface, not overridden.
	decorated := storage.NewHookFiringStore(ds, nil)

	if _, ok := beads.AsIssueClaimer(decorated); !ok {
		t.Errorf("AsIssueClaimer returned ok=false through a HookFiringStore decorator")
	}
	if _, ok := beads.AsEventQuerier(decorated); !ok {
		t.Errorf("AsEventQuerier returned ok=false through a HookFiringStore decorator")
	}
	if _, ok := beads.AsDependentQuerier(decorated); !ok {
		t.Errorf("AsDependentQuerier returned ok=false through a HookFiringStore decorator")
	}
}
