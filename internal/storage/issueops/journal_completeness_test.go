package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// The emit helpers a function calls to journal a row directly.
var journalEmitHelpers = map[string]bool{
	"RecordEventInTx":    true,
	"RecordDeleteInTx":   true,
	"RecordDepEventInTx": true,
	"insertEventRow":     true,
}

// mutationEntryPoints are the issueops functions that must result in a journal
// row. Every write plumbing bottoms out in one of these. The structural
// cross-check below (TestEveryBeadMutatorJournalsOrIsExempt) guarantees this
// list stays complete: any exported function that writes a work-bead table must
// appear here or in beadDMLExemptions, so a new mutation path cannot be added
// without being accounted for.
var mutationEntryPoints = []string{
	"CreateIssueInTx",
	"CreateIssueInTxWithResult",
	"CreateIssuesInTx",
	"CreateIssuesInTxWithResult",
	"PersistDependenciesWithOptionsResult", // creation-time dependency edges
	"UpdateIssueInTx",
	"UpdateIssueWithoutEventInTx",
	"CloseIssueInTx",
	"CloseIssueWithoutEventInTx",
	"ReopenIssueInTx",
	"DeleteIssueInTx",
	"DeleteIssuesInTx",
	"DeleteIssuesBySourceRepoInTx",
	"ClaimIssueInTx",
	"ClaimReadyIssueInTx",
	"ReclaimExpiredLeasesInTx",
	"PromoteFromEphemeralInTx",
	"AddDependencyInTx",
	"RemoveDependencyInTx",
	"AddLabelInTx",
	"RemoveLabelInTx",
	"UpdateIssueIDInTx",
	"AddIssueCommentInTx",
	"ImportIssueCommentInTx",
}

// beadDMLExemptions are exported functions the DML detector flags as writing a
// work-bead table but which legitimately do NOT journal, each with a reason.
// They fall into four buckets: (1) derived child-counter maintenance; (2)
// aux-table writers the templated-%s heuristic can't distinguish from a bead
// table (events, child counters); (3) constituent sub-helpers of a
// create/rename/promote/delete whose top-level entry point journals the whole
// mutation once; (4) compaction
// maintenance outside the create/update/close/delete/dep/label op vocabulary. The
// staleness check fails if any stops being flagged, so an exemption cannot rot.
var beadDMLExemptions = map[string]string{
	// (1) Child counters are derived CLI acceleration state. In contrast,
	// is_blocked is part of the exported bead snapshot and its recompute helpers
	// structurally journal every value that actually changes.
	"ReconcileChildCounters": "recomputes denormalized child-counter state, not a bead mutation",

	// (2) aux tables matched via templated %s, not work-bead state.
	"RecordEventInTable":     "writes the events audit table (templated %s), not work-bead state",
	"RecordFullEventInTable": "writes the events audit table (templated %s), not work-bead state",
	"AddCommentEventInTx":    "writes the events audit table (templated %s), not work-bead state",
	"GetNextChildIDTx":       "writes the child_counters allocation table (templated %s), not work-bead state",

	// (3) constituent sub-helpers; the calling entry point journals the whole
	// mutation once (a create/rename/promote/delete emits a single row).
	"InsertIssueIntoTable":                   "raw issue insert; the calling create entry point journals the create",
	"InsertIssueIfNew":                       "raw issue insert; the calling create entry point journals the create",
	"PersistLabels":                          "constituent label write of a create; the create entry point journals it",
	"PersistComments":                        "constituent comment write of a create; the create entry point journals it",
	"UpdateWispIDInDependenciesInTx":         "rewrites dep rows during a rename; UpdateIssueIDInTx journals the rename",
	"UpdateIssueIDInDependenciesInTx":        "rewrites dep rows during a rename; UpdateIssueIDInTx journals the rename",
	"RetargetInboundDependenciesToWispInTx":  "rewrites dep rows during promote; PromoteFromEphemeralInTx journals it",
	"RetargetInboundDependenciesToIssueInTx": "rewrites dep rows during promote; PromoteFromEphemeralInTx journals it",
	"DeleteWispFromDependenciesInTx":         "cleans up dep rows during a delete that journals the delete",
	"DeleteWispsFromDependenciesInTx":        "cleans up dep rows during a delete that journals the delete",

	// (4) compaction maintenance — a lossy content rewrite outside the mutation
	// op vocabulary; not currently journaled.
	"ApplyCompactionInTx":     "compaction content rewrite (maintenance), outside the mutation op vocabulary",
	"RestoreFromSnapshotInTx": "restores a compacted issue (maintenance), outside the mutation op vocabulary",
}

// TestEveryMutationFunctionJournals parses this package's source, builds the
// intra-package call graph, and asserts every mutation entry point either
// records a journal row directly (calls one of the Record*InTx emit helpers) or
// calls a function that transitively does.
//
// This kills the enumeration-drift class that sank the decorator design: there,
// coverage was a hand-maintained list of overridden methods, and new mutation
// paths silently slipped through. Here, if a listed mutation function stops
// emitting — directly or through its delegates — this test fails.
func TestEveryMutationFunctionJournals(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}

	emits := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(journalEmitHelpers) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })

	for _, entry := range mutationEntryPoints {
		if _, defined := fns[entry]; !defined {
			t.Errorf("mutation entry point %q not found in issueops — was it renamed? update mutationEntryPoints", entry)
			continue
		}
		if !emits[entry] {
			t.Errorf("mutation entry point %q does not journal: it neither calls a Record*InTx emit helper nor a function that transitively does", entry)
		}
	}
}

// TestEveryBeadMutatorJournalsOrIsExempt is the STRUCTURAL completeness
// cross-check. It detects, by DML rather than by name, every EXPORTED function
// that writes a work-bead table (INSERT / UPDATE / DELETE against issues, wisps,
// dependencies, labels, comments, and their wisp variants — literal or templated
// table name), and asserts each one journals (calls an emit helper directly or
// transitively) OR is an explicitly-exempted derived-state / aux / sub-helper.
// A new exported mutator that writes a bead table therefore cannot ship without
// either journaling or being justified in beadDMLExemptions, closing the "named
// outside the pattern, silently un-journaled" hole the hand list alone left open.
func TestEveryBeadMutatorJournalsOrIsExempt(t *testing.T) {
	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}

	// A function writes a bead table if its own body does, or a free function it
	// calls transitively does.
	beadDML := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.OwnBeadDML },
		func(f *journalscan.FuncInfo) []string { return f.IdentCalls })

	// A function emits if it calls an emit helper directly or transitively.
	emits := journalscan.Fixpoint(fns,
		func(f *journalscan.FuncInfo) bool { return f.CallsAnyOf(journalEmitHelpers) },
		func(f *journalscan.FuncInfo) []string { return f.AllCallNames() })

	seenExempt := map[string]bool{}
	var checked int
	for key, f := range fns {
		if f.Recv != "" || !f.Exported || !beadDML[key] {
			continue
		}
		if reason, ok := beadDMLExemptions[f.Name]; ok {
			if reason == "" {
				t.Errorf("%s has an empty exemption reason", f.Name)
			}
			seenExempt[f.Name] = true
			continue
		}
		checked++
		if !emits[key] {
			t.Errorf("exported function %q writes a work-bead table but does not journal (no Record*InTx emit helper directly or transitively) and is not exempted — make it journal, or add it to beadDMLExemptions with a reason", f.Name)
		}
	}

	if checked == 0 {
		t.Fatal("cross-check found no exported bead mutators — DML detection or parsing changed; the guard is not actually running")
	}
	for m := range beadDMLExemptions {
		if !seenExempt[m] {
			t.Errorf("exemption %q no longer matches an exported bead-writing function — remove it", m)
		}
	}
}
