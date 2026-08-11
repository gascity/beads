package issueops

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

func liteIssueRows() *sqlmock.Rows {
	return sqlmock.NewRows(parseSelectColumns(IssueSelectColumnsLite))
}

func liteIssueRowValues(id, title string) []driver.Value {
	values := make([]driver.Value, 0, len(parseSelectColumns(IssueSelectColumnsLite)))
	for _, col := range parseSelectColumns(IssueSelectColumnsLite) {
		switch col {
		case "id":
			values = append(values, id)
		case "title":
			values = append(values, title)
		case "status":
			values = append(values, string(types.StatusOpen))
		case "priority":
			values = append(values, 1)
		case "issue_type":
			values = append(values, string(types.TypeTask))
		case "compaction_level":
			values = append(values, 0)
		default:
			values = append(values, nil)
		}
	}
	return values
}

func TestSearchIssuesInTxLitePreservesRelationsAndOmitsHeavyFields(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`(?s)SELECT id, content_hash, title,\s+status.* FROM issues .*WHERE .*status = \?.*ORDER BY`).
		WithArgs(types.StatusOpen).
		WillReturnRows(liteIssueRows().
			AddRow(liteIssueRowValues("bd-001", "one")...).
			AddRow(liteIssueRowValues("bd-002", "two")...))
	mock.ExpectQuery(`SELECT issue_id, label FROM labels WHERE issue_id IN \(\?,\?\) ORDER BY issue_id, label`).
		WithArgs("bd-001", "bd-002").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).
			AddRow("bd-001", "release"))
	mock.ExpectQuery(`(?s)SELECT issue_id, .* FROM dependencies WHERE issue_id IN \(\?,\?\) ORDER BY issue_id`).
		WithArgs("bd-001", "bd-002").
		WillReturnRows(sqlmock.NewRows([]string{
			"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id",
		}).AddRow("bd-001", "bd-002", string(types.DepBlocks), nil, "tester", nil, nil))

	status := types.StatusOpen
	got, err := SearchIssuesInTx(context.Background(), tx, "", types.IssueFilter{
		Status:              &status,
		IncludeDependencies: true,
		SkipWisps:           true,
		NoIDShrink:          true,
		Lite:                true,
	})
	if err != nil {
		t.Fatalf("SearchIssuesInTx: %v", err)
	}
	if len(got) != 2 || got[0].ID != "bd-001" || got[1].ID != "bd-002" {
		t.Fatalf("issues = %#v, want [bd-001 bd-002] in storage order", got)
	}
	if !got[0].IsLitePartial {
		t.Fatal("first issue IsLitePartial = false, want true")
	}
	if got[0].Description != "" || got[0].Design != "" || got[0].AcceptanceCriteria != "" ||
		got[0].Notes != "" || got[0].Payload != "" || len(got[0].Waiters) != 0 {
		t.Fatalf("first issue heavy fields were hydrated: %#v", got[0])
	}
	if len(got[0].Labels) != 1 || got[0].Labels[0] != "release" {
		t.Fatalf("first issue labels = %v, want [release]", got[0].Labels)
	}
	if len(got[0].Dependencies) != 1 || got[0].Dependencies[0].DependsOnID != "bd-002" {
		t.Fatalf("first issue dependencies = %v, want bd-002", got[0].Dependencies)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestSearchIssuesInTxDefaultKeepsFullBody(t *testing.T) {
	t.Parallel()

	values := issueRowValues("bd-001", "one")
	for i, col := range issueColumns() {
		if col == "description" {
			values[i] = "full description"
		}
	}

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`(?s)SELECT .*description.* FROM issues .*ORDER BY`).
		WillReturnRows(issueRows().AddRow(values...))
	mock.ExpectQuery(`SELECT issue_id, label FROM labels ORDER BY issue_id, label`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))

	got, err := SearchIssuesInTx(context.Background(), tx, "", types.IssueFilter{SkipWisps: true})
	if err != nil {
		t.Fatalf("SearchIssuesInTx: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("issues = %d, want 1", len(got))
	}
	if got[0].IsLitePartial {
		t.Fatal("default search IsLitePartial = true, want false")
	}
	if got[0].Description != "full description" {
		t.Fatalf("description = %q, want full description", got[0].Description)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
