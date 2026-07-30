//go:build cgo

package embeddeddolt_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedIssueOperationsUpdateFoldsMetadataIntoOneEvent pins a compound
// update to a single event. A guarded update is one atomic mutation, so its
// history must read as one entry; a metadata patch riding along with field
// edits used to write the row twice and fabricate a second event in the stream
// every history consumer sees.
func TestEmbeddedIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_metadata_event")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	issue := &types.Issue{ID: "ops-metadata-event", Title: "metadata event", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{"keep":"old"}`)}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	events := newEventCounter(t, ctx, te, issue.ID)

	updated, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		Metadata: publicops.MetadataPatch{
			Set: map[string]json.RawMessage{"added": json.RawMessage(`"value"`)},
		},
	}})
	if err != nil {
		t.Fatalf("compound update: %v", err)
	}
	if !updated.Changed || updated.Issue.Status != types.StatusInProgress {
		t.Fatalf("compound update result = %#v", updated)
	}
	if !sameEmbeddedMetadataJSON(updated.Issue.Metadata, json.RawMessage(`{"added":"value","keep":"old"}`)) {
		t.Fatalf("compound update metadata = %s", updated.Issue.Metadata)
	}
	events.assert(t, "compound update", 1, map[types.EventType]int{types.EventStatusChanged: 1, types.EventUpdated: 0})

	// A metadata-only patch still records its own single event.
	metadataOnly, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Unset: []string{"keep"}},
	}})
	if err != nil || !metadataOnly.Changed {
		t.Fatalf("metadata-only update = %#v, %v", metadataOnly, err)
	}
	if !sameEmbeddedMetadataJSON(metadataOnly.Issue.Metadata, json.RawMessage(`{"added":"value"}`)) {
		t.Fatalf("metadata-only update metadata = %s", metadataOnly.Issue.Metadata)
	}
	events.assert(t, "metadata-only update", 1, map[types.EventType]int{types.EventUpdated: 1})

	// A metadata patch that changes nothing stays elided.
	noOp, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}},
	}})
	if err != nil || noOp.Changed {
		t.Fatalf("no-op metadata update = %#v, %v", noOp, err)
	}
	events.assert(t, "no-op metadata update", 0, nil)
}

// eventCounter reports how many event rows each operation adds for one issue.
type eventCounter struct {
	te     *testEnv
	ctx    context.Context
	id     string
	total  int
	byType map[types.EventType]int
}

func newEventCounter(t *testing.T, ctx context.Context, te *testEnv, id string) *eventCounter {
	t.Helper()
	counter := &eventCounter{te: te, ctx: ctx, id: id, byType: map[types.EventType]int{}}
	counter.total = counter.count(t, "")
	for _, eventType := range []types.EventType{types.EventUpdated, types.EventStatusChanged} {
		counter.byType[eventType] = counter.count(t, eventType)
	}
	return counter
}

func (c *eventCounter) count(t *testing.T, eventType types.EventType) int {
	t.Helper()
	var got int
	if eventType == "" {
		c.te.queryScalar(t, c.ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", []any{c.id}, &got)
		return got
	}
	c.te.queryScalar(t, c.ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", []any{c.id, string(eventType)}, &got)
	return got
}

// assert checks the rows added since the previous assert and re-baselines.
func (c *eventCounter) assert(t *testing.T, label string, wantTotal int, wantByType map[types.EventType]int) {
	t.Helper()
	total := c.count(t, "")
	if got := total - c.total; got != wantTotal {
		t.Errorf("%s wrote %d event rows, want %d", label, got, wantTotal)
	}
	c.total = total
	for eventType, want := range wantByType {
		current := c.count(t, eventType)
		if got := current - c.byType[eventType]; got != want {
			t.Errorf("%s wrote %d %q events, want %d", label, got, eventType, want)
		}
	}
	for _, eventType := range []types.EventType{types.EventUpdated, types.EventStatusChanged} {
		c.byType[eventType] = c.count(t, eventType)
	}
}
