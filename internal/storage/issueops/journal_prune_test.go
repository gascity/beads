package issueops

import (
	"errors"
	"testing"
	"time"
)

func TestBuildEventsPruneWhere(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	before := int64(500)

	cases := []struct {
		name       string
		retainDays int
		rowsCeil   int64
		rowsCeilOK bool
		wantWhere  string
		wantArgs   []any
	}{
		{
			name:      "no floors",
			wantWhere: "seq < ?",
			wantArgs:  []any{before},
		},
		{
			name:       "retain-days only",
			retainDays: 7,
			wantWhere:  "seq < ? AND ts < ?",
			wantArgs:   []any{before, now.AddDate(0, 0, -7).UTC()},
		},
		{
			name:       "retain-rows only",
			rowsCeil:   480,
			rowsCeilOK: true,
			wantWhere:  "seq < ? AND seq <= ?",
			wantArgs:   []any{before, int64(480)},
		},
		{
			name:       "both floors",
			retainDays: 3,
			rowsCeil:   490,
			rowsCeilOK: true,
			wantWhere:  "seq < ? AND ts < ? AND seq <= ?",
			wantArgs:   []any{before, now.AddDate(0, 0, -3).UTC(), int64(490)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			where, args := BuildEventsPruneWhere(before, tc.retainDays, now, tc.rowsCeil, tc.rowsCeilOK)
			if where != tc.wantWhere {
				t.Errorf("where = %q, want %q", where, tc.wantWhere)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args len = %d (%v), want %d (%v)", len(args), args, len(tc.wantArgs), tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("arg[%d] = %v, want %v", i, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

// TestComputeEventsPruneWhere pins the shared retain-floor orchestration both
// prune plumbings (the DBTX path and the proxied raw-SQL path) run through, so
// they can never drift on the retain-rows short-circuit or which rows a floor
// protects.
func TestComputeEventsPruneWhere(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

	t.Run("no retain-rows skips ceil read", func(t *testing.T) {
		called := false
		where, args, skip, err := ComputeEventsPruneWhere(500, 7, 0, now, func() (int64, bool, error) {
			called = true
			return 0, false, nil
		})
		if err != nil || skip {
			t.Fatalf("err=%v skip=%v, want nil/false", err, skip)
		}
		if called {
			t.Error("readCeil must not be called when retainRows == 0")
		}
		if where != "seq < ? AND ts < ?" || len(args) != 2 {
			t.Errorf("where=%q args=%v", where, args)
		}
	})

	t.Run("retain-rows not found skips prune", func(t *testing.T) {
		_, _, skip, err := ComputeEventsPruneWhere(500, 0, 10, now, func() (int64, bool, error) {
			return 0, false, nil
		})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if !skip {
			t.Error("skip must be true when the whole journal is inside the retained window")
		}
	})

	t.Run("retain-rows floor applied", func(t *testing.T) {
		where, args, skip, err := ComputeEventsPruneWhere(500, 0, 10, now, func() (int64, bool, error) {
			return 480, true, nil
		})
		if err != nil || skip {
			t.Fatalf("err=%v skip=%v", err, skip)
		}
		if where != "seq < ? AND seq <= ?" || len(args) != 2 || args[1] != int64(480) {
			t.Errorf("where=%q args=%v", where, args)
		}
	})

	t.Run("ceil read error propagates", func(t *testing.T) {
		sentinel := errors.New("boom")
		_, _, _, err := ComputeEventsPruneWhere(500, 0, 10, now, func() (int64, bool, error) {
			return 0, false, sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want %v", err, sentinel)
		}
	})
}

func TestNormalizeEventsTimestampRFC3339UTC(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"2026-07-29 10:11:12", "2026-07-29T10:11:12Z"},
		{"2026-07-29 10:11:12.123456", "2026-07-29T10:11:12.123456Z"},
		{"2026-07-29T10:11:12.123456+02:00", "2026-07-29T08:11:12.123456Z"},
	} {
		if got := normalizeEventsTimestamp(tc.in); got != tc.want {
			t.Errorf("normalizeEventsTimestamp(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
