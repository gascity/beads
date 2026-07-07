package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func GetReadyWorkWithCountsInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp dependency probe: %w", err)
	}

	issuePreds, err := buildReadyWorkPredicates(ctx, tx, filter, IssuesFilterTables)
	if err != nil {
		return nil, err
	}
	out, err := runReadyCountsInTx(ctx, tx, IssuesFilterTables, filter.Limit, issuePreds, wispDepsExist, false)
	if err != nil {
		return nil, err
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return out, nil
	}
	if !wispDepsExist {
		return out, nil
	}

	wispPreds, err := buildReadyWorkPredicates(ctx, tx, filter, WispsFilterTables)
	if err != nil {
		return nil, err
	}
	wisps, err := runReadyCountsInTx(ctx, tx, WispsFilterTables, filter.Limit, wispPreds, true, false)
	if err != nil {
		if isTableNotExistError(err) {
			return out, nil
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return out, nil
	}

	// Prefer the canonical wisp record when an ID exists in both tables (be-iabdi).
	wispByID := make(map[string]struct{}, len(wisps))
	for _, w := range wisps {
		if w != nil && w.Issue != nil {
			wispByID[w.Issue.ID] = struct{}{}
		}
	}
	var kept []*types.IssueWithCounts
	for _, iwc := range out {
		if iwc == nil || iwc.Issue == nil {
			kept = append(kept, iwc)
			continue
		}
		if _, dup := wispByID[iwc.Issue.ID]; !dup {
			kept = append(kept, iwc)
		}
	}
	kept = append(kept, wisps...)
	sortIssuesWithCountsByPolicy(kept, filter.SortPolicy)
	if filter.Limit > 0 && len(kept) > filter.Limit {
		kept = kept[:filter.Limit]
	}
	return kept, nil
}

// runReadyCountsInTx renders the ready-work counts mega-query for one table
// family, pushing the page down when the caller bounded it.
//
// For a bounded page (limit > 0) it first resolves the ≤limit ready IDs with the
// cheap indexed ID query (the same SELECT id … the non-counts GetReadyWork path
// uses), then computes the counts constrained to exactly those IDs
// (WHERE i.id IN (…)). This is what de-quadratics the query: the reverse-blocker
// subquery rc joins on COALESCE(depends_on_issue_id, …), an expression SQLite
// cannot auto-index, so the planner re-scans rc's whole materialization once per
// driver row. Bounding the driver to the page (not every ready issue) turns that
// O(candidates × blockers) scan into O(page × blockers). Each per-issue count is
// a function of the full dependency graph, not of the candidate set, so
// constraining the driver leaves every emitted count byte-identical to the
// unbounded mega-query; the page is the same top-N the ORDER BY … LIMIT selected
// because the ready order ends in a unique `id` tiebreak.
//
// For limit <= 0 (unbounded) there is no page to push down, so it runs the
// original mega-query unchanged.
//
//nolint:gosec // G201: whereSQL/orderBySQL/limitSQL are hardcoded fragments; user input rides ? placeholders.
func runReadyCountsInTx(ctx context.Context, tx *sql.Tx, tables FilterTables, limit int, preds *readyWorkPredicates, includeWispReverseDeps, skipLabels bool) ([]*types.IssueWithCounts, error) {
	if limit <= 0 {
		return runSearchQueryInTx(ctx, tx, tables, preds.whereSQL, preds.orderBySQL, preds.limitSQL, preds.args, includeWispReverseDeps, skipLabels)
	}

	idQuery := fmt.Sprintf("SELECT id FROM %s %s %s %s", tables.Main, preds.whereSQL, preds.orderBySQL, preds.limitSQL)
	pageIDs, err := queryReadyIssueIDPage(ctx, tx, idQuery, preds.args)
	if err != nil {
		return nil, err
	}
	if len(pageIDs) == 0 {
		return nil, nil
	}

	// The by-IDs form binds the page up to eight times (driver + each subquery).
	// For an unusually large page fall back to the unbounded mega-query rather
	// than risk a per-statement placeholder limit; correctness is unchanged.
	if len(pageIDs) > readyCountsPushdownMaxPage {
		return runSearchQueryInTx(ctx, tx, tables, preds.whereSQL, preds.orderBySQL, preds.limitSQL, preds.args, includeWispReverseDeps, skipLabels)
	}

	countsSQL, idArgs := sqlbuild.SearchCountsByIDsSQL(tables, pageIDs, includeWispReverseDeps, skipLabels)
	rows, err := scanReadyCountsRows(ctx, tx, tables, countsSQL, idArgs)
	if err != nil {
		return nil, err
	}

	// The by-IDs query returns the page in arbitrary row order; restore the ready
	// order the ID query already computed so the result stays identical to the
	// unbounded mega-query's ORDER BY … LIMIT.
	byID := make(map[string]*types.IssueWithCounts, len(rows))
	for _, r := range rows {
		if r != nil && r.Issue != nil {
			byID[r.Issue.ID] = r
		}
	}
	ordered := make([]*types.IssueWithCounts, 0, len(pageIDs))
	for _, id := range pageIDs {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered, nil
}

// readyCountsPushdownMaxPage caps the page size for the by-IDs counts form.
// 1000 IDs bound up to ~8000 placeholders, comfortably under every backend's
// per-statement parameter limit; larger pages fall back to the mega-query.
const readyCountsPushdownMaxPage = 1000

// scanReadyCountsRows runs a prebuilt counts query and hydrates the rows through
// the same scan path runSearchQueryInTx uses, deduping by issue ID.
//
//nolint:gosec // G201: countsSQL is builder-produced; user input rides ? placeholders.
func scanReadyCountsRows(ctx context.Context, tx *sql.Tx, tables FilterTables, countsSQL string, args []any) ([]*types.IssueWithCounts, error) {
	rows, err := tx.QueryContext(ctx, countsSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("ready counts %s: %w", tables.Main, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, scanErr := ScanReadyWorkRowWithCounts(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if iwc == nil || iwc.Issue == nil {
			continue
		}
		if seen[iwc.Issue.ID] {
			continue
		}
		seen[iwc.Issue.ID] = true
		out = append(out, iwc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ready counts %s: rows: %w", tables.Main, err)
	}
	return out, nil
}

// CountReadyWorkInTx returns the number of ready-work items — identical to
// len(GetReadyWorkWithCountsInTx(filter with Limit=0)) — without materializing
// the counts mega-query. When no wisps exist (the common case) the ready set is
// exactly the issues matching the ready predicate, so a single indexed COUNT(*)
// over that predicate is authoritative. When wisps exist it falls back to the
// full method so the wisp-merge/dedup count stays exact. This backs the
// "Showing X of N" total that `bd ready` prints when the page is capped, so it
// avoids re-running the whole mega-query unbounded just to size N.
func CountReadyWorkInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) (int, error) {
	countFilter := filter
	countFilter.Limit = 0

	empty, err := wispsTableEmptyOrMissingInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("count ready work: wisp probe: %w", err)
	}
	if empty {
		preds, err := buildReadyWorkPredicates(ctx, tx, countFilter, IssuesFilterTables)
		if err != nil {
			return 0, err
		}
		//nolint:gosec // G201: whereSQL is hardcoded fragments; user input rides ? placeholders.
		q := "SELECT COUNT(*) FROM issues " + preds.whereSQL
		var n int
		if err := tx.QueryRowContext(ctx, q, preds.whereArgs...).Scan(&n); err != nil {
			return 0, fmt.Errorf("count ready work: %w", err)
		}
		return n, nil
	}

	rows, err := GetReadyWorkWithCountsInTx(ctx, tx, countFilter)
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

func sortIssuesWithCountsByPolicy(items []*types.IssueWithCounts, policy types.SortPolicy) {
	if len(items) <= 1 {
		return
	}
	issues := make([]*types.Issue, 0, len(items))
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		issues = append(issues, item.Issue)
	}
	if len(issues) != len(items) {
		return
	}
	sortReadyIssues(issues, policy)
	byID := make(map[string]int, len(issues))
	for i, iss := range issues {
		byID[iss.ID] = i
	}
	sorted := make([]*types.IssueWithCounts, len(items))
	for _, item := range items {
		sorted[byID[item.Issue.ID]] = item
	}
	copy(items, sorted)
}

// ScanReadyWorkRowWithCounts scans one row of the counts mega-query
// (sqlbuild.SearchCountsSQL): IssueSelectColumns followed by labels JSON,
// dep/rdep/comment counts, parent ID, and dependency JSON. Exported so the
// domain/db stack hydrates counts rows through the exact same code path.
func ScanReadyWorkRowWithCounts(rows *sql.Rows) (*types.IssueWithCounts, error) {
	var labelsJSON, depsJSON sql.NullString
	var parentID sql.NullString
	var depCount, rdepCount, commentCount sql.NullInt64

	composite := &compositeReadyRow{
		row: rows,
		extra: []any{
			&labelsJSON,
			&depCount,
			&rdepCount,
			&commentCount,
			&parentID,
			&depsJSON,
		},
	}
	issue, err := ScanIssueFrom(composite)
	if err != nil {
		return nil, fmt.Errorf("scan issue with counts: %w", err)
	}

	if labelsJSON.Valid && labelsJSON.String != "" {
		var labels []string
		if err := json.Unmarshal([]byte(labelsJSON.String), &labels); err != nil {
			return nil, fmt.Errorf("scan issue with counts: parse labels_json: %w", err)
		}
		sort.Strings(labels)
		issue.Labels = labels
	}

	if depsJSON.Valid && depsJSON.String != "" {
		var deps []*types.Dependency
		if err := json.Unmarshal([]byte(depsJSON.String), &deps); err != nil {
			return nil, fmt.Errorf("scan issue with counts: parse deps_json: %w", err)
		}
		issue.Dependencies = deps
	}

	iwc := &types.IssueWithCounts{
		Issue:           issue,
		DependencyCount: int(depCount.Int64),
		DependentCount:  int(rdepCount.Int64),
		CommentCount:    int(commentCount.Int64),
	}
	if parentID.Valid {
		s := parentID.String
		iwc.Parent = &s
	}
	return iwc, nil
}

type compositeReadyRow struct {
	row   *sql.Rows
	extra []any
}

func (c *compositeReadyRow) Scan(dest ...any) error {
	combined := make([]any, 0, len(dest)+len(c.extra))
	combined = append(combined, dest...)
	combined = append(combined, c.extra...)
	return c.row.Scan(combined...)
}
