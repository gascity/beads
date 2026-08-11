package issueops

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

func TestLoadBlockingDepsForIssueIDsInTxBatchesAtTwoHundred(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := testIDs(401)
	query := regexp.MustCompile(`(?s)FROM dependencies\s+WHERE issue_id IN \(.*type = 'blocks'.*type = 'waits-for'.*type = 'conditional-blocks'`).String()
	for range 3 {
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_id", "type", "metadata"}))
	}

	deps, err := loadBlockingDepsForIssueIDsInTx(t.Context(), db, []string{"dependencies"}, ids)
	if err != nil {
		t.Fatalf("load blocking deps: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("deps = %d, want 0", len(deps))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadParentIDsForChildrenInTxBatchesAtTwoHundred(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := testIDs(401)
	query := regexp.MustCompile(`(?s)FROM dependencies\s+WHERE issue_id IN \(.*type = 'parent-child'`).String()
	for range 3 {
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_id"}))
	}

	parents, err := loadParentIDsForChildrenInTx(t.Context(), db, []string{"dependencies"}, ids)
	if err != nil {
		t.Fatalf("load parent IDs: %v", err)
	}
	if len(parents) != 0 {
		t.Fatalf("parents = %d, want 0", len(parents))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetStatisticsInTxUsesOneAggregateAndClampsReady(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.MustCompile(`(?s)SELECT.*COUNT\(\*\).*is_blocked.*status <> 'closed'.*status <> 'pinned'`).String()).
		WillReturnRows(sqlmock.NewRows([]string{"total", "open", "in_progress", "closed", "deferred", "pinned", "blocked"}).
			AddRow(8, 2, 1, 3, 1, 1, 3))

	stats, err := GetStatisticsInTx(t.Context(), db)
	if err != nil {
		t.Fatalf("GetStatisticsInTx: %v", err)
	}
	want := &types.Statistics{TotalIssues: 8, OpenIssues: 2, InProgressIssues: 1, ClosedIssues: 3, DeferredIssues: 1, PinnedIssues: 1, BlockedIssues: 3, ReadyIssues: 0}
	if *stats != *want {
		t.Fatalf("stats = %+v, want %+v", *stats, *want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func testIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("issue-%03d", i)
	}
	return ids
}
