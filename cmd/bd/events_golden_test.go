package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// goldenPath is the committed consumer contract: one journal line per record, in
// the exact shape `bd events tail`/`export` emit. Regenerate with
// BD_UPDATE_GOLDEN=1 go test ./cmd/bd/ -run TestEventsJournalGolden.
const goldenPath = "testdata/events_journal_records.jsonl"

// TestEventsJournalGolden pins the external record contract for the durable
// events journal. It marshals REAL beads types.Issue and EventDep values
// through the same buildRecord path `bd events tail` uses, so the golden
// captures bd's actual field marshaling — issue_type, omitempty elision, and the
// top-level dep edge (kind/target) — that external consumers parse. A change to
// the wire shape (a renamed/added/removed field, a lost omitempty) fails this
// test until the golden is regenerated deliberately.
func TestEventsJournalGolden(t *testing.T) {
	got := renderGoldenLines(t)

	// The runtime snapshot comes from issueops.GetIssueInTx, which loads the
	// issue row and its labels only — never its Dependencies. So no journal
	// record can carry an inline "dependencies" array; dependency edges surface
	// solely through the top-level "dep" field on dep_add / dep_remove records.
	// Enforce that here so the golden pins what bd actually writes.
	assertNoInlineDependencies(t, got)

	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with BD_UPDATE_GOLDEN=1): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("journal record contract drifted from %s.\n--- got ---\n%s\n--- want ---\n%s\nregenerate with BD_UPDATE_GOLDEN=1 if intended", goldenPath, got, want)
	}
}

// renderGoldenLines builds the fixture records and returns them as JSONL exactly
// as buildRecord + the tail encoder would emit them.
func renderGoldenLines(t *testing.T) []byte {
	t.Helper()
	ts := "2026-01-02 03:04:05" // fixed committed-at string, as CAST(ts AS CHAR) yields
	created := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closed := time.Date(2026, 1, 2, 3, 30, 0, 0, time.UTC)
	est := 90

	// A minimal open task — the common case; exercises omitempty elision.
	minimal := &types.Issue{
		ID: "bd-100", Title: "wire the seam", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 1, CreatedAt: created, UpdatedAt: updated,
	}
	// A richly populated feature with labels, metadata, and an external ref —
	// exercises the full field surface consumers may read. It deliberately omits
	// Dependencies: the runtime snapshot (issueops.GetIssueInTx) loads the issue
	// row and its labels only, so a real journal record never carries an inline
	// "dependencies" array. Dependency edges are recorded solely through the
	// top-level "dep" field on the dep_add / dep_remove records below.
	full := &types.Issue{
		ID: "bd-101", Title: "durable journal", Description: "append-only record",
		AcceptanceCriteria: "replayable", Status: types.StatusInProgress,
		IssueType: types.TypeFeature, Priority: 0, Assignee: "worker-1",
		Owner: "dev@example.com", EstimatedMinutes: &est, CreatedAt: created,
		CreatedBy: "author", UpdatedAt: updated, ExternalRef: strptr("gh-9"),
		SourceSystem: "github", Metadata: json.RawMessage(`{"k":"v"}`),
		Labels: []string{"infra", "urgent"},
	}
	// A closed issue — exercises close_reason / closed_at marshaling.
	closedIssue := &types.Issue{
		ID: "bd-101", Title: "durable journal", Status: types.StatusClosed,
		IssueType: types.TypeFeature, Priority: 0, CreatedAt: created,
		UpdatedAt: closed, ClosedAt: &closed, CloseReason: "shipped",
	}
	// An ephemeral wisp — exercises the ephemeral/wisp_type fields.
	wisp := &types.Issue{
		ID: "bd-wisp-1", Title: "convoy member", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 2, CreatedAt: created, UpdatedAt: updated,
		Ephemeral: true, WispType: types.WispType("convoy"),
	}

	records := []eventRecord{
		buildRecord(1, ts, string(issueops.EventCreate), minimal.ID, mustJSON(t, minimal), "", ""),
		buildRecord(2, ts, string(issueops.EventCreate), full.ID, mustJSON(t, full), "", ""),
		buildRecord(3, ts, string(issueops.EventDepAdd), "bd-101", mustJSON(t, full),
			mustJSON(t, &issueops.EventDep{Kind: string(types.DepBlocks), Target: "bd-100", Metadata: `{}`}), ""),
		buildRecord(4, ts, string(issueops.EventDepRemove), "bd-101", mustJSON(t, full),
			mustJSON(t, &issueops.EventDep{Kind: string(types.DepBlocks), Target: "bd-100", Metadata: `{}`}), ""),
		buildRecord(5, ts, string(issueops.EventCommentWrite), full.ID, mustJSON(t, full), "", mustJSON(t, &issueops.EventComment{ID: "019c0000-0000-7000-8000-000000000000", Author: "worker-1", Text: "journal me", CreatedAt: updated, Source: "structured"})),
		buildRecord(6, ts, string(issueops.EventClose), closedIssue.ID, mustJSON(t, closedIssue), "", ""),
		buildRecord(7, ts, string(issueops.EventCreate), wisp.ID, mustJSON(t, wisp), "", ""),
		buildRecord(8, ts, string(issueops.EventDelete), "bd-100", "", "", ""), // null issue on delete
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode record: %v", err)
		}
	}
	return buf.Bytes()
}

// assertNoInlineDependencies fails if any record's issue snapshot carries a
// "dependencies" array. The runtime snapshot (GetIssueInTx: issue + labels
// only) never populates Dependencies, so a real record cannot contain one; a
// fixture that does would pin a shape bd never emits.
func assertNoInlineDependencies(t *testing.T, jsonl []byte) {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(jsonl), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Seq   int64           `json:"seq"`
			Issue json.RawMessage `json:"issue"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		if len(rec.Issue) == 0 || string(rec.Issue) == "null" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rec.Issue, &fields); err != nil {
			t.Fatalf("unmarshal issue for seq %d: %v", rec.Seq, err)
		}
		if _, ok := fields["dependencies"]; ok {
			t.Errorf("record seq %d carries an inline \"dependencies\" array, but the runtime snapshot (GetIssueInTx loads issue + labels only) never emits one; dependency edges belong only in the top-level \"dep\" field", rec.Seq)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

func strptr(s string) *string { return &s }
