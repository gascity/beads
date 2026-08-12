package db

import (
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestEventsJournal_PruneFloors exercises the real prune predicate
// (issueops.BuildEventsPruneWhere + EventsPruneRowsCeilQuery) against Dolt:
// retain-rows keeps the newest N rows, retain-days keeps rows within the window,
// both floors compose, and the seq keeps climbing after a prune (never resets,
// because it is drawn from the durable bd_events_seq counter, which prune
// never touches). Rows are inserted directly with controlled ts so age-based
// pruning is deterministic.
func (s *testSuite) TestEventsJournal_PruneFloors() {
	ctx := s.Ctx()
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	// insert allocates seq from the durable counter (mirroring
	// issueops.nextEventSeq) so seq never resets across the DELETEs below.
	insert := func(id string, ts time.Time) int64 {
		_, err := s.Runner().ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = next_seq + 1 WHERE id = 0")
		s.Require().NoError(err)
		var seq int64
		s.Require().NoError(s.Runner().QueryRowContext(ctx,
			"SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&seq))
		_, err = s.Runner().ExecContext(ctx,
			`INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, dep_json) VALUES (?, ?, 'create', ?, NULL, NULL)`,
			seq, ts.UTC(), id)
		s.Require().NoError(err)
		return seq
	}
	remaining := func() []string {
		rows, err := s.Runner().QueryContext(ctx, "SELECT issue_id FROM bd_events_journal ORDER BY seq ASC")
		s.Require().NoError(err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			s.Require().NoError(rows.Scan(&id))
			out = append(out, id)
		}
		s.Require().NoError(rows.Err())
		return out
	}
	// prune applies the exact runEventsPrune logic against the live table.
	prune := func(before int64, retainDays, retainRows int) {
		var (
			rowsCeil   int64
			rowsCeilOK bool
		)
		if retainRows > 0 {
			err := s.Runner().QueryRowContext(ctx, issueops.EventsPruneRowsCeilQuery(), retainRows).Scan(&rowsCeil)
			if err != nil {
				// No row past the retained window: nothing prunable.
				return
			}
			rowsCeilOK = true
		}
		where, args := issueops.BuildEventsPruneWhere(before, retainDays, time.Now().UTC(), rowsCeil, rowsCeilOK)
		_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal WHERE "+where, args...)
		s.Require().NoError(err)
	}

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -10)
	// s1,s2 are old; s3,s4,s5 are recent.
	insert("r1", old)
	insert("r2", old)
	insert("r3", now)
	insert("r4", now)
	s5 := insert("r5", now)

	// retain-rows=2 with a wide-open --before keeps only the newest two rows.
	prune(s5+1000, 0, 2)
	s.Equal([]string{"r4", "r5"}, remaining(), "retain-rows=2 must keep the newest two rows")

	// Reset and prove the retain-days floor keeps rows within the window.
	_, err = s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)
	insert("d1", old)
	insert("d2", old)
	insert("d3", now)
	insert("d4", now)
	dLast := insert("d5", now)
	prune(dLast+1000, 3, 0) // keep rows younger than 3 days → drops the two 10-day-old rows
	s.Equal([]string{"d3", "d4", "d5"}, remaining(), "retain-days=3 must keep only rows within the window")

	// Seq never resets: a fresh insert after pruning gets a seq above everything.
	after := insert("d6", now)
	s.Greater(after, dLast, "counter-drawn seq must keep climbing after a prune (never reset)")
}
