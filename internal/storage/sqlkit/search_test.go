package sqlkit

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

type searchTestDialect struct {
	name string
	db   *sql.DB
}

func (d searchTestDialect) Name() string { return d.name }

func (d searchTestDialect) Open(context.Context) (*sql.DB, error) { return d.db, nil }

func newSearchTestStore(t *testing.T, dialect string) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Store{db: db, dialect: searchTestDialect{name: dialect, db: db}}, mock
}

func TestSearchIssuesPostgresLiteDurableBypassesReadTransaction(t *testing.T) {
	store, mock := newSearchTestStore(t, "postgres")
	mock.ExpectQuery(`SELECT id, content_hash, title,\s+status.* FROM issues .*ORDER BY`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	issues, err := store.SearchIssues(context.Background(), "", types.IssueFilter{
		Lite:       true,
		SkipWisps:  true,
		SkipLabels: true,
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want no results", issues)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected transaction or query: %v", err)
	}
}

func TestSearchIssuesPostgresLiteDurablePreservesClosedGuard(t *testing.T) {
	store, _ := newSearchTestStore(t, "postgres")
	store.closed.Store(true)

	_, err := store.SearchIssues(context.Background(), "", types.IssueFilter{Lite: true, SkipWisps: true})
	if !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("SearchIssues error = %v, want ErrStoreClosed", err)
	}
}

func TestSearchIssuesKeepsReadTransactionOutsidePostgresLiteDurablePath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect string
		filter  types.IssueFilter
	}{
		{
			name:    "sqlite lite",
			dialect: "sqlite",
			filter:  types.IssueFilter{Lite: true, SkipWisps: true, SkipLabels: true},
		},
		{
			name:    "postgres full",
			dialect: "postgres",
			filter:  types.IssueFilter{SkipWisps: true, SkipLabels: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newSearchTestStore(t, tc.dialect)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectRollback()

			issues, err := store.SearchIssues(context.Background(), "", tc.filter)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			if len(issues) != 0 {
				t.Fatalf("issues = %#v, want no results", issues)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("transaction expectations: %v", err)
			}
		})
	}
}
