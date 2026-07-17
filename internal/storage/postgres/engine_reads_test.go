package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/pgdialect"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// newEngineReadsStore builds an initialized Postgres store in a throwaway schema.
func newEngineReadsStore(t *testing.T) (*Store, string) {
	t.Helper()
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping live Postgres engine-reads test")
	}
	schema := fmt.Sprintf("engreads_%d", time.Now().UnixNano())
	ctx := context.Background()

	st, err := New(ctx, Config{DSN: url, Schema: schema})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if err := InitSchema(ctx, raw, schema); err != nil {
		_ = raw.Close()
		t.Fatalf("InitSchema: %v", err)
	}
	_ = raw.Close()
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
		_ = st.Close()
	})
	if err := st.SetConfig(ctx, "issue_prefix", "er"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return st, schema
}

// TestEngineReadsPostgres exercises EventsSince / GetDependentRecords /
// CountDependentRecords end-to-end on a live Postgres store, proving the sqlkit
// delegation + pg dialect translation produce the same behavior the Dolt
// reference does.
func TestEngineReadsPostgres(t *testing.T) {
	st, _ := newEngineReadsStore(t)
	ctx := context.Background()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2, CreatedBy: "tester"}
		if err := st.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	for _, id := range []string{"er-target", "er-s1", "er-s2", "er-s3", "er-other"} {
		mk(id)
	}
	addDep := func(src, tgt string, typ types.DependencyType) {
		if err := st.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ, CreatedBy: "tester"}, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", src, tgt, err)
		}
	}
	addDep("er-s1", "er-target", types.DepBlocks)
	addDep("er-s2", "er-target", types.DepParentChild)
	addDep("er-s3", "er-target", types.DepParentChild)
	addDep("er-target", "er-other", types.DepBlocks) // decoy: target is the source

	// GetDependentRecords: all inbound edges of the target, raw, id populated.
	all, err := st.GetDependentRecords(ctx, "er-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	got := map[string]bool{}
	for _, d := range all {
		if d.ID == "" {
			t.Fatalf("dependent row has empty ID (cursor key): %+v", d)
		}
		if d.DependsOnID != "er-target" {
			t.Fatalf("row target = %q, want er-target", d.DependsOnID)
		}
		got[d.IssueID] = true
	}
	if len(all) != 3 || !got["er-s1"] || !got["er-s2"] || !got["er-s3"] {
		t.Fatalf("dependents = %v, want {er-s1,er-s2,er-s3}", got)
	}

	// Type filter.
	pc, err := st.GetDependentRecords(ctx, "er-target", string(types.DepParentChild), 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(parent-child): %v", err)
	}
	if len(pc) != 2 {
		t.Fatalf("parent-child dependents = %d, want 2", len(pc))
	}

	// Keyset paging on the row id: page size 1 recovers all 3, no dup/drop.
	seen := map[string]bool{}
	after := ""
	for {
		page, err := st.GetDependentRecords(ctx, "er-target", "", 1, after)
		if err != nil {
			t.Fatalf("GetDependentRecords(page after %q): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
	}
	if len(seen) != 3 {
		t.Fatalf("paged sources = %v, want 3 distinct", seen)
	}

	// CountDependentRecords: true total without paging.
	if n, err := st.CountDependentRecords(ctx, "er-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords = %d, want 3", n)
	}
	if n, err := st.CountDependentRecords(ctx, "er-target", string(types.DepParentChild)); err != nil {
		t.Fatalf("CountDependentRecords(parent-child): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(parent-child) = %d, want 2", n)
	}

	// EventsSince: durable feed + per-issue filter.
	feed, err := st.EventsSince(ctx, storage.EventCursor{}, "", 500)
	if err != nil {
		t.Fatalf("EventsSince(all): %v", err)
	}
	if len(feed) == 0 {
		t.Fatalf("EventsSince(all) returned no durable events")
	}
	scoped, err := st.EventsSince(ctx, storage.EventCursor{}, "er-s1", 500)
	if err != nil {
		t.Fatalf("EventsSince(issue=er-s1): %v", err)
	}
	if len(scoped) == 0 {
		t.Fatalf("EventsSince(issue=er-s1) returned no events")
	}
	for _, e := range scoped {
		if e.IssueID != "er-s1" {
			t.Fatalf("scoped feed returned event for %q", e.IssueID)
		}
	}
}

// TestDependentTargetPredicateSargablePostgres is the #8 regression guard on the
// hosted backend: the per-column OR target predicate must plan as a BitmapOr /
// index scan (sargable) rather than the Seq Scan the old COALESCE(target)=?
// wrapper forces. It builds a volume-populated synthetic table carrying the
// dependencies target columns + indexes (a real ANALYZEd table is needed for the
// planner to prefer the index), then EXPLAINs both forms.
func TestDependentTargetPredicateSargablePostgres(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping Postgres sargability EXPLAIN")
	}
	schema := fmt.Sprintf("sarg_%d", time.Now().UnixNano())
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	// Close LAST. t.Cleanup runs LIFO, so registering the close first makes it
	// run after the DROP below — a `defer raw.Close()` would instead close the
	// pool before any t.Cleanup DROP, so the drop would silently no-op on a
	// closed connection and leak the schema.
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// Registered after the close cleanup, so it runs FIRST — while raw is open.
	t.Cleanup(func() {
		_, _ = raw.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
	})
	setup := []string{
		`CREATE TABLE dep_probe (id text PRIMARY KEY, type text NOT NULL DEFAULT 'blocks',
			depends_on_issue_id text, depends_on_wisp_id text, depends_on_external text)`,
		`CREATE INDEX idx_dp_issue ON dep_probe (depends_on_issue_id)`,
		`CREATE INDEX idx_dp_wisp ON dep_probe (depends_on_wisp_id)`,
		`CREATE INDEX idx_dp_external ON dep_probe (depends_on_external)`,
		`INSERT INTO dep_probe (id, depends_on_issue_id)
			SELECT 'id-'||g, CASE WHEN g<10 THEN 'tgt' ELSE 'o-'||g END FROM generate_series(1,5000) g`,
		`ANALYZE dep_probe`,
	}
	for _, s := range setup {
		if _, err := raw.ExecContext(ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	explain := func(where string) string {
		rows, err := raw.QueryContext(ctx, "EXPLAIN SELECT id FROM dep_probe WHERE "+where)
		if err != nil {
			t.Fatalf("EXPLAIN: %v", err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var line sql.NullString
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if line.Valid {
				sb.WriteString(line.String)
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}

	// Single-source the predicate under test from production: the sargable
	// per-column OR is issueops.DepTargetEqualsOr, whose three ? placeholders we
	// bind to the same literal 'tgt' for the plan. A revert to a COALESCE wrapper
	// there breaks this guard.
	orPlan := explain(literalizeParams(issueops.DepTargetEqualsOr(""), "'tgt'", "'tgt'", "'tgt'"))
	if !strings.Contains(orPlan, "Bitmap") && !strings.Contains(orPlan, "Index") {
		t.Fatalf("per-column OR target predicate is not sargable (want BitmapOr/Index scan), plan:\n%s", orPlan)
	}
	if strings.Contains(orPlan, "Seq Scan") {
		t.Fatalf("per-column OR target predicate regressed to a Seq Scan:\n%s", orPlan)
	}

	// Contrast: the old COALESCE wrapper (issueops.DepTargetExpr) is NOT sargable
	// (Seq Scan). This is the concrete before/after — if a future change reverts
	// the predicate to COALESCE this stays a Seq Scan and the OR assertion above
	// would too.
	coalescePlan := explain(issueops.DepTargetExpr + ` = 'tgt'`)
	if !strings.Contains(coalescePlan, "Seq Scan") {
		t.Logf("note: COALESCE form no longer Seq Scans on this PG version; plan:\n%s", coalescePlan)
	}
}

// TestSearchIssuesKeysetPostgres exercises the §13.12 (created_at DESC, id ASC)
// keyset end-to-end on a live Postgres store via the public SearchIssues, proving
// the shared sqlbuild predicate + pg dialect page a same-second group larger than
// a page completely, with no drop/dup.
func TestSearchIssuesKeysetPostgres(t *testing.T) {
	st, _ := newEngineReadsStore(t)
	ctx := context.Background()

	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	seeds := []struct {
		id string
		at time.Time
	}{
		{"k-newer", base.Add(time.Second)},
		{"k-a1", base}, {"k-a2", base}, {"k-a3", base}, {"k-a4", base}, {"k-a5", base},
		{"k-older", base.Add(-time.Second)},
	}
	for _, s := range seeds {
		iss := &types.Issue{ID: s.id, Title: s.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedBy: "tester", CreatedAt: s.at}
		if err := st.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	want := []string{"k-newer", "k-a1", "k-a2", "k-a3", "k-a4", "k-a5", "k-older"}
	ids := func(issues []*types.Issue) []string {
		out := make([]string, len(issues))
		for i, iss := range issues {
			out[i] = iss.ID
		}
		return out
	}
	eq := func(got, exp []string) bool {
		if len(got) != len(exp) {
			return false
		}
		for i := range exp {
			if got[i] != exp[i] {
				return false
			}
		}
		return true
	}

	full, err := st.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: 100})
	if err != nil {
		t.Fatalf("SearchIssues(full): %v", err)
	}
	if got := ids(full); !eq(got, want) {
		t.Fatalf("full order = %v, want %v", got, want)
	}

	const pageSize = 2
	var collected []string
	seen := map[string]bool{}
	var afterCreatedAt *time.Time
	afterID := ""
	for i := 0; i < 100; i++ {
		page, err := st.SearchIssues(ctx, "", types.IssueFilter{
			IDPrefix: "k-", SkipWisps: true, SortBy: "created", Limit: pageSize,
			AfterCreatedAt: afterCreatedAt, AfterID: afterID,
		})
		if err != nil {
			t.Fatalf("SearchIssues(page %d): %v", i, err)
		}
		if len(page) == 0 {
			break
		}
		for _, iss := range page {
			if seen[iss.ID] {
				t.Fatalf("duplicate id %q across pages — same-second overflow lost", iss.ID)
			}
			seen[iss.ID] = true
			collected = append(collected, iss.ID)
		}
		last := page[len(page)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !eq(collected, want) {
		t.Fatalf("keyset paged order = %v, want %v (no drop/dup)", collected, want)
	}
}

// TestIsBlockedBatchPostgres is the §13.7 parity regression on the live Postgres
// backend: the batch transitive is_blocked read agrees with per-row IsBlocked and
// reflects inherited blockedness with an empty direct-blocker set.
func TestIsBlockedBatchPostgres(t *testing.T) {
	st, _ := newEngineReadsStore(t)
	ctx := context.Background()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedBy: "tester"}
		if err := st.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		if err := st.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ, CreatedBy: "tester"}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s: %v", src, tgt, err)
		}
	}
	mk("ib-blk")
	mk("ib-parent")
	add("ib-parent", "ib-blk", types.DepBlocks)
	mk("ib-child")
	add("ib-child", "ib-parent", types.DepParentChild)
	mk("ib-free")

	ids := []string{"ib-blk", "ib-parent", "ib-child", "ib-free"}
	batch, err := st.IsBlockedBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	for _, id := range ids {
		want, _, err := st.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		if batch[id] != want {
			t.Fatalf("IsBlockedBatch[%s] = %v, want %v (per-row IsBlocked)", id, batch[id], want)
		}
	}
	blocked, blockers, err := st.IsBlocked(ctx, "ib-child")
	if err != nil {
		t.Fatalf("IsBlocked(ib-child): %v", err)
	}
	if !blocked || len(blockers) != 0 {
		t.Fatalf("ib-child IsBlocked = (%v, %v), want (true, []) — inherited block, empty direct blockers", blocked, blockers)
	}
	if !batch["ib-child"] || !batch["ib-parent"] || batch["ib-free"] {
		t.Fatalf("IsBlockedBatch = %v, want child+parent true, free false", batch)
	}
}

// TestKeysetPredicateSargablePostgres is the §13.12 sargability guard on the
// hosted backend: the keyset predicate must plan as an index/BitmapOr scan
// (sargable) rather than a Seq Scan. It builds a volume-populated synthetic table
// with a created_at index, then EXPLAINs the production predicate (single-sourced
// from sqlbuild.KeysetCreatedAtIDPredicate) with a selective cursor low in the
// range so the planner prefers the index.
func TestKeysetPredicateSargablePostgres(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping Postgres keyset sargability EXPLAIN")
	}
	schema := fmt.Sprintf("kssarg_%d", time.Now().UnixNano())
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = raw.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
	})
	setup := []string{
		`CREATE TABLE issue_probe (id text PRIMARY KEY, created_at timestamptz NOT NULL)`,
		`CREATE INDEX idx_ip_created_at ON issue_probe (created_at)`,
		// 5000 rows spread across an hour; only a small early slice satisfies a
		// low cursor, so the planner prefers the index over a Seq Scan.
		`INSERT INTO issue_probe (id, created_at)
			SELECT 'id-'||g, TIMESTAMPTZ '2024-01-01 00:00:00Z' + (g || ' seconds')::interval
			FROM generate_series(1,5000) g`,
		`ANALYZE issue_probe`,
	}
	for _, s := range setup {
		if _, err := raw.ExecContext(ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	explain := func(where string) string {
		rows, err := raw.QueryContext(ctx, "EXPLAIN SELECT id FROM issue_probe WHERE "+where+" ORDER BY created_at DESC, id ASC LIMIT 100")
		if err != nil {
			t.Fatalf("EXPLAIN: %v", err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var line sql.NullString
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if line.Valid {
				sb.WriteString(line.String)
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}

	// Selective cursor: 10 seconds into the range → ~10 of 5000 rows qualify.
	const cur = "'2024-01-01 00:00:10Z'"
	plan := explain(literalizeParams(sqlbuild.KeysetCreatedAtIDPredicate, cur, cur, "''"))
	if !strings.Contains(plan, "Index") && !strings.Contains(plan, "Bitmap") {
		t.Fatalf("keyset predicate is not sargable (want Index/Bitmap scan on idx_ip_created_at), plan:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan") {
		t.Fatalf("keyset predicate regressed to a Seq Scan:\n%s", plan)
	}
}

// literalizeParams replaces each ? placeholder in query, in order, with the
// corresponding literal — for EXPLAINing a production ?-bound predicate whose
// planner shape is under test. It panics on an arity mismatch so a drifted
// placeholder count fails loudly rather than EXPLAINing malformed SQL.
func literalizeParams(query string, literals ...string) string {
	for _, lit := range literals {
		if !strings.Contains(query, "?") {
			panic("literalizeParams: more literals than ? placeholders in query")
		}
		query = strings.Replace(query, "?", lit, 1)
	}
	if strings.Contains(query, "?") {
		panic("literalizeParams: unbound ? placeholder(s) remain in query")
	}
	return query
}
