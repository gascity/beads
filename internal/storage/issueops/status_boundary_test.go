package issueops

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestGuardClosedBoundaryInTx(t *testing.T) {
	t.Run("built-in crossings are refused without configuration reads", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			current types.Status
			next    types.Status
		}{
			{name: "enter done", current: types.StatusOpen, next: types.StatusClosed},
			{name: "leave done", current: types.StatusClosed, next: types.StatusOpen},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, doneToDone, err := GuardClosedBoundaryInTx(context.Background(), nil, tc.current, tc.next)
				if !errors.Is(err, storage.ErrClosedBoundary) || got != "" || doneToDone {
					t.Fatalf("GuardClosedBoundaryInTx() = (%q, %v, %v), want ErrClosedBoundary", got, doneToDone, err)
				}
				var boundary *ClosedBoundaryError
				if !errors.As(err, &boundary) || boundary.From() != tc.current || boundary.To() != tc.next {
					t.Fatalf("boundary = %#v, want %q -> %q", boundary, tc.current, tc.next)
				}
			})
		}
	})

	t.Run("built-in same-side changes avoid configuration reads", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectRollback()
		got, doneToDone, err := GuardClosedBoundaryInTx(context.Background(), tx, types.StatusOpen, types.StatusInProgress)
		if err != nil || got != types.StatusInProgress || doneToDone {
			t.Fatalf("open -> in_progress = (%q, %v, %v), want (in_progress, false, nil)", got, doneToDone, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("same historical status avoids configuration reads", func(t *testing.T) {
		got, doneToDone, err := GuardClosedBoundaryInTx(context.Background(), nil, "retired", "retired")
		if err != nil || got != "retired" || doneToDone {
			t.Fatalf("retired -> retired = (%q, %v, %v), want (retired, false, nil)", got, doneToDone, err)
		}
	})

	t.Run("custom matrix honors done membership xor", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		for range 3 {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
				WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).
					AddRow("review", string(types.CategoryWIP)).
					AddRow("archived", string(types.CategoryDone)))
		}
		mock.ExpectRollback()

		for _, tc := range []struct {
			current    types.Status
			next       types.Status
			boundary   bool
			doneToDone bool
		}{
			{types.StatusOpen, "archived", true, false},
			{"archived", types.StatusOpen, true, false},
			{"archived", types.StatusClosed, false, true},
		} {
			got, doneToDone, err := GuardClosedBoundaryInTx(context.Background(), tx, tc.current, tc.next)
			if errors.Is(err, storage.ErrClosedBoundary) != tc.boundary || doneToDone != tc.doneToDone {
				t.Fatalf("%q -> %q = (%q, %v, %v), want boundary=%v doneToDone=%v", tc.current, tc.next, got, doneToDone, err, tc.boundary, tc.doneToDone)
			}
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unknown historical current is non-done and invalid request is not a boundary", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		for range 2 {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
				WillReturnRows(sqlmock.NewRows([]string{"name", "category"}))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
				WithArgs("status.custom").WillReturnRows(sqlmock.NewRows([]string{"value"}))
		}
		mock.ExpectRollback()
		got, doneToDone, err := GuardClosedBoundaryInTx(context.Background(), tx, "retired", types.StatusOpen)
		if err != nil || got != types.StatusOpen || doneToDone {
			t.Fatalf("retired -> open = (%q, %v, %v), want (open, false, nil)", got, doneToDone, err)
		}
		_, _, err = GuardClosedBoundaryInTx(context.Background(), tx, types.StatusOpen, "not-configured")
		if err == nil || errors.Is(err, storage.ErrClosedBoundary) {
			t.Fatalf("unknown request err = %v, want non-boundary validation error", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
