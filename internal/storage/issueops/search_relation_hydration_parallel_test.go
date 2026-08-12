package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

type relationBarrier struct {
	started chan string
	release chan struct{}

	labelRows [][]driver.Value
	depRows   [][]driver.Value
	labelErr  error
	depErr    error
}

func newRelationBarrier(t *testing.T) *relationBarrier {
	t.Helper()
	return &relationBarrier{
		started: make(chan string, 2),
		release: make(chan struct{}),
	}
}

func newRelationBarrierDB(t *testing.T, barrier *relationBarrier) *sql.DB {
	t.Helper()
	db := sql.OpenDB(relationBarrierConnector{barrier: barrier})
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type relationBarrierConnector struct{ barrier *relationBarrier }

func (c relationBarrierConnector) Connect(context.Context) (driver.Conn, error) {
	return relationBarrierConn{barrier: c.barrier}, nil
}

func (c relationBarrierConnector) Driver() driver.Driver {
	return relationBarrierDriver{barrier: c.barrier}
}

type relationBarrierDriver struct{ barrier *relationBarrier }

func (d relationBarrierDriver) Open(string) (driver.Conn, error) {
	return relationBarrierConn{barrier: d.barrier}, nil
}

type relationBarrierConn struct{ barrier *relationBarrier }

func (c relationBarrierConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (relationBarrierConn) Close() error { return nil }

func (relationBarrierConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c relationBarrierConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	kind, rows, err := c.relationQuery(query)
	if kind == "" {
		return nil, errors.New("unexpected query")
	}
	c.barrier.started <- kind
	select {
	case <-c.barrier.release:
		if err != nil {
			return nil, err
		}
		return &relationDriverRows{columns: relationColumns(kind), values: rows}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c relationBarrierConn) relationQuery(query string) (string, [][]driver.Value, error) {
	switch {
	case strings.Contains(query, "FROM labels"):
		return "labels", c.barrier.labelRows, c.barrier.labelErr
	case strings.Contains(query, "FROM dependencies"):
		return "dependencies", c.barrier.depRows, c.barrier.depErr
	default:
		return "", nil, nil
	}
}

type relationDriverRows struct {
	columns []string
	values  [][]driver.Value
	next    int
}

func (r relationDriverRows) Columns() []string { return r.columns }

func (relationDriverRows) Close() error { return nil }

func (r *relationDriverRows) Next(dest []driver.Value) error {
	if r.next == len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.next])
	r.next++
	return nil
}

func relationColumns(kind string) []string {
	if kind == "labels" {
		return []string{"issue_id", "label"}
	}
	return []string{"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id"}
}

func awaitRelationOverlap(t *testing.T, barrier *relationBarrier, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for range 2 {
		select {
		case <-barrier.started:
		case <-deadline.C:
			close(barrier.release)
			<-done
			t.Fatal("relation reads did not overlap")
		}
	}
	close(barrier.release)
	if err := <-done; err != nil {
		t.Fatalf("hydrateIssueLabelsAndDeps: %v", err)
	}
}

func hydrateFromDB(ctx context.Context, db *sql.DB, issues []*types.Issue, filter types.IssueFilter, wholeTable bool) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- hydrateIssueLabelsAndDeps(ctx, db, IssuesFilterTables, issues, filter, wholeTable)
	}()
	return done
}

func TestHydrateIssueRelationsFromDBOverlapsWholeTableReads(t *testing.T) {
	barrier := newRelationBarrier(t)
	barrier.labelRows = [][]driver.Value{{"bd-001", "alpha"}, {"bd-002", "beta"}}
	barrier.depRows = [][]driver.Value{{"bd-002", "bd-001", string(types.DepBlocks), nil, "tester", nil, nil}}
	issues := []*types.Issue{{ID: "bd-002"}, {ID: "bd-001"}}

	done := hydrateFromDB(context.Background(), newRelationBarrierDB(t, barrier), issues, types.IssueFilter{
		Lite:                true,
		IncludeDependencies: true,
	}, true)
	awaitRelationOverlap(t, barrier, done)

	if issues[0].ID != "bd-002" || issues[1].ID != "bd-001" {
		t.Fatalf("issue order changed: %#v", issues)
	}
	if got := issues[0].Labels; len(got) != 1 || got[0] != "beta" {
		t.Fatalf("bd-002 labels = %v, want [beta]", got)
	}
	if got := issues[1].Labels; len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("bd-001 labels = %v, want [alpha]", got)
	}
	if got := issues[0].Dependencies; len(got) != 1 || got[0].IssueID != "bd-002" || got[0].DependsOnID != "bd-001" || got[0].Type != types.DepBlocks {
		t.Fatalf("bd-002 dependencies = %#v, want blocks bd-001", got)
	}
	if got := issues[1].Dependencies; len(got) != 0 {
		t.Fatalf("bd-001 dependencies = %#v, want none", got)
	}
}

func TestHydrateIssueRelationsFromDBOverlapsBoundedReads(t *testing.T) {
	barrier := newRelationBarrier(t)
	barrier.labelRows = [][]driver.Value{{"bd-099", "last"}, {"bd-000", "first"}}
	barrier.depRows = [][]driver.Value{{"bd-099", "bd-000", string(types.DepBlocks), nil, "tester", nil, nil}}
	issues := make([]*types.Issue, 100)
	for i := range issues {
		issues[i] = &types.Issue{ID: fmt.Sprintf("bd-%03d", i)}
	}
	issues[0], issues[99] = issues[99], issues[0]

	done := hydrateFromDB(context.Background(), newRelationBarrierDB(t, barrier), issues, types.IssueFilter{
		Lite:                true,
		IncludeDependencies: true,
	}, false)
	awaitRelationOverlap(t, barrier, done)

	if issues[0].ID != "bd-099" || issues[99].ID != "bd-000" {
		t.Fatalf("issue order changed: first=%s last=%s", issues[0].ID, issues[99].ID)
	}
	if got := issues[0].Labels; len(got) != 1 || got[0] != "last" {
		t.Fatalf("bd-099 labels = %v, want [last]", got)
	}
	if got := issues[99].Labels; len(got) != 1 || got[0] != "first" {
		t.Fatalf("bd-000 labels = %v, want [first]", got)
	}
	if got := issues[0].Dependencies; len(got) != 1 || got[0].IssueID != "bd-099" || got[0].DependsOnID != "bd-000" || got[0].Type != types.DepBlocks {
		t.Fatalf("bd-099 dependencies = %#v, want blocks bd-000", got)
	}
	if got := issues[99].Dependencies; len(got) != 0 {
		t.Fatalf("bd-000 dependencies = %#v, want none", got)
	}
}

func TestHydrateIssueRelationsFromDBAttachesNeitherRelationOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		labelErr error
		depErr   error
		context  string
	}{
		{name: "labels", labelErr: errors.New("labels unavailable"), context: "hydrate labels"},
		{name: "dependencies", depErr: errors.New("dependencies unavailable"), context: "hydrate dependencies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			barrier := newRelationBarrier(t)
			barrier.labelRows = [][]driver.Value{{"bd-001", "alpha"}}
			barrier.depRows = [][]driver.Value{{"bd-001", "bd-002", string(types.DepBlocks), nil, "tester", nil, nil}}
			barrier.labelErr = tc.labelErr
			barrier.depErr = tc.depErr
			issues := []*types.Issue{{ID: "bd-001"}}

			done := hydrateFromDB(context.Background(), newRelationBarrierDB(t, barrier), issues, types.IssueFilter{
				Lite:                true,
				IncludeDependencies: true,
			}, false)
			deadline := time.NewTimer(time.Second)
			for range 2 {
				select {
				case <-barrier.started:
				case <-deadline.C:
					close(barrier.release)
					<-done
					t.Fatal("relation reads did not both start")
				}
			}
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			close(barrier.release)
			if err := <-done; err == nil {
				t.Fatal("hydrateIssueLabelsAndDeps error = nil, want relation error")
			} else if !strings.Contains(err.Error(), tc.context) {
				t.Fatalf("hydrateIssueLabelsAndDeps error = %q, want context %q", err, tc.context)
			}
			if len(issues[0].Labels) != 0 || len(issues[0].Dependencies) != 0 {
				t.Fatalf("failed hydration attached relations: %#v", issues[0])
			}
		})
	}
}

func TestHydrateIssueRelationsInTxRemainsSerial(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT issue_id, label FROM labels WHERE issue_id IN \(\?,\?\) ORDER BY issue_id, label`).
		WithArgs("bd-001", "bd-002").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
	mock.ExpectQuery(`(?s)SELECT issue_id, .* FROM dependencies WHERE issue_id IN \(\?,\?\) ORDER BY issue_id`).
		WithArgs("bd-001", "bd-002").
		WillReturnRows(sqlmock.NewRows([]string{
			"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id",
		}))

	err := hydrateIssueLabelsAndDeps(context.Background(), tx, IssuesFilterTables, []*types.Issue{{ID: "bd-001"}, {ID: "bd-002"}}, types.IssueFilter{
		Lite:                true,
		IncludeDependencies: true,
	}, false)
	if err != nil {
		t.Fatalf("hydrateIssueLabelsAndDeps: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected serial relation reads: %v", err)
	}
}

func TestHydrateIssueRelationsSkipsUnrequestedQueries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter types.IssueFilter
		query  string
	}{
		{
			name:   "skip labels",
			filter: types.IssueFilter{Lite: true, SkipLabels: true, IncludeDependencies: true},
			query:  `(?s)SELECT issue_id, .* FROM dependencies WHERE issue_id IN \(\?\) ORDER BY issue_id`,
		},
		{
			name:   "dependencies disabled",
			filter: types.IssueFilter{Lite: true},
			query:  `SELECT issue_id, label FROM labels WHERE issue_id IN \(\?\) ORDER BY issue_id, label`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery(tc.query).WithArgs("bd-001").WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))

			if err := hydrateIssueLabelsAndDeps(context.Background(), tx, IssuesFilterTables, []*types.Issue{{ID: "bd-001"}}, tc.filter, false); err != nil {
				t.Fatalf("hydrateIssueLabelsAndDeps: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected relation query: %v", err)
			}
		})
	}
}
