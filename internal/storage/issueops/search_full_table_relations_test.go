package issueops

import (
	"context"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// A search with no predicate returns every row from the selected issue table. Relation
// hydration must use that fact instead of sending the same complete ID set back to the
// database in queryBatchSize chunks. On a remote store those chunks are pure round trips.
func TestSearchWholeTableHydratesRelationsInOneQueryEach(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	issueRows := issueRows()
	for i := 0; i < 401; i++ {
		id := fmt.Sprintf("bd-%03d", i)
		issueRows.AddRow(issueRowValues(id, "issue "+id)...)
	}
	mock.ExpectQuery(`(?s)SELECT .* FROM issues .*ORDER BY`).
		WillReturnRows(issueRows)
	mock.ExpectQuery(`SELECT issue_id, label FROM labels ORDER BY issue_id, label`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).
			AddRow("bd-000", "release"))
	mock.ExpectQuery(`(?s)SELECT issue_id, .* FROM dependencies\s+ORDER BY issue_id`).
		WillReturnRows(sqlmock.NewRows([]string{
			"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id",
		}).AddRow("bd-000", "bd-001", string(types.DepBlocks), nil, "tester", nil, nil))

	got, err := searchTableInTxT(
		context.Background(),
		tx,
		"",
		types.IssueFilter{IncludeDependencies: true},
		IssuesFilterTables,
		issueProjection,
	)
	if err != nil {
		t.Fatalf("searchTableInTxT: %v", err)
	}
	if len(got) != 401 {
		t.Fatalf("issues = %d, want 401", len(got))
	}
	if len(got[0].Labels) != 1 || got[0].Labels[0] != "release" {
		t.Fatalf("first issue labels = %v, want [release]", got[0].Labels)
	}
	if len(got[0].Dependencies) != 1 || got[0].Dependencies[0].DependsOnID != "bd-001" {
		t.Fatalf("first issue dependencies = %v, want bd-001", got[0].Dependencies)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A selective search does not cover the whole relation table. Keep the bounded ID query in
// that case so unrelated labels and dependencies are not read merely because they exist.
func TestSearchFilteredTableHydratesOnlyMatchingRelations(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`(?s)SELECT .* FROM issues .*WHERE .*status = \?.*ORDER BY`).
		WithArgs(types.StatusOpen).
		WillReturnRows(issueRows().
			AddRow(issueRowValues("bd-001", "one")...).
			AddRow(issueRowValues("bd-002", "two")...))
	mock.ExpectQuery(`SELECT issue_id, label FROM labels WHERE issue_id IN \(\?,\?\) ORDER BY issue_id, label`).
		WithArgs("bd-001", "bd-002").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
	mock.ExpectQuery(`(?s)SELECT issue_id, .* FROM dependencies WHERE issue_id IN \(\?,\?\) ORDER BY issue_id`).
		WithArgs("bd-001", "bd-002").
		WillReturnRows(sqlmock.NewRows([]string{
			"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id",
		}))

	status := types.StatusOpen
	got, err := searchTableInTxT(
		context.Background(),
		tx,
		"",
		types.IssueFilter{Status: &status, IncludeDependencies: true},
		IssuesFilterTables,
		issueProjection,
	)
	if err != nil {
		t.Fatalf("searchTableInTxT: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("issues = %d, want 2", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
