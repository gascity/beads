package sqlbuild

import "fmt"

// ReadyWorkIssueColumns is IssueSelectColumns qualified with the "i." alias
// used by the counts mega-query.
var ReadyWorkIssueColumns = QualifyColumns(IssueSelectColumns, "i.")

// DepJSONObject renders one dependency row as JSON for JSON_ARRAYAGG
// aggregation in the counts mega-query. Field names must match the JSON tags
// of types.Dependency.
const DepJSONObject = `JSON_OBJECT(
	'issue_id', issue_id,
	'depends_on_id', COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external),
	'type', type,
	'created_at', DATE_FORMAT(created_at, '%Y-%m-%dT%H:%i:%sZ'),
	'created_by', created_by,
	'metadata', CAST(metadata AS CHAR),
	'thread_id', thread_id
)`

// SearchCountsSQL renders the counts mega-query: full issue rows aliased "i"
// plus labels JSON, dep/rdep/comment counts, parent ID, and dependency JSON,
// for one table family. whereSQL/orderBySQL/limitSQL may be empty; the
// reverse-blocker count unions wisp_dependencies only when the caller has
// probed that the table exists.
//
// The scan side is issueops.ScanReadyWorkRowWithCounts, which scans
// IssueSelectColumns positionally followed by the six extra columns in the
// order projected here.
func SearchCountsSQL(tables FilterTables, whereSQL, orderBySQL, limitSQL string, includeWispReverseDeps, skipLabels bool) string {
	reverseBlockerSelect := `
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS dep_id
				FROM dependencies WHERE type = 'blocks'
	`
	if includeWispReverseDeps {
		reverseBlockerSelect += `
				UNION ALL
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS dep_id
				FROM wisp_dependencies WHERE type = 'blocks'
		`
	}

	labelsSelect := "l.labels_json AS labels_json"
	labelsJoin := fmt.Sprintf(`
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(label) AS labels_json
			FROM %s
			GROUP BY issue_id
		) l ON l.issue_id = i.id`, tables.Labels)
	if skipLabels {
		labelsSelect = "NULL AS labels_json"
		labelsJoin = ""
	}

	return fmt.Sprintf(`
		SELECT %s,
			%s,
			COALESCE(dc.cnt, 0) AS dep_count,
			COALESCE(rc.cnt, 0) AS rdep_count,
			COALESCE(cc.cnt, 0) AS comment_count,
			pc.parent_id     AS parent_id,
			d.deps_json      AS deps_json
		FROM %s i
		%s
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			WHERE type = 'blocks'
			GROUP BY issue_id
		) dc ON dc.issue_id = i.id
		LEFT JOIN (
			SELECT dep_id, COUNT(*) AS cnt FROM (
				%s
			) all_blockers GROUP BY dep_id
		) rc ON rc.dep_id = i.id
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			GROUP BY issue_id
		) cc ON cc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id,
			       MIN(COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) AS parent_id
			FROM %s
			WHERE type = 'parent-child'
			GROUP BY issue_id
		) pc ON pc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(%s) AS deps_json
			FROM %s
			GROUP BY issue_id
		) d ON d.issue_id = i.id
		%s
		%s
		%s
	`,
		ReadyWorkIssueColumns,
		labelsSelect,
		tables.Main,
		labelsJoin,
		tables.Dependencies,
		reverseBlockerSelect,
		tables.Comments,
		tables.Dependencies,
		DepJSONObject,
		tables.Dependencies,
		whereSQL,
		orderBySQL,
		limitSQL,
	)
}

// SearchCountsByIDsSQL is the page-pushed form of SearchCountsSQL: it renders the
// counts mega-query for an explicit, already-resolved set of issue IDs, with the
// driver AND every count subquery constrained to those IDs. This keeps each
// subquery from scanning/aggregating its whole side table and keeps the
// reverse-blocker rc self-join (whose COALESCE(...) key SQLite cannot auto-index)
// from re-scanning its full materialization per candidate — the cost is bounded
// by the page, not by every ready issue.
//
// It stays byte-identical to SearchCountsSQL for the same IDs: each per-issue
// count is a function of the whole dependency graph restricted to that issue, so
// filtering a subquery's input to the page issues cannot change any surviving
// row's count. Comment/dep/parent counts are order-insensitive; labels are
// re-sorted in Go (ScanReadyWorkRowWithCounts); deps_json's element order is the
// per-issue row order, which the issue_id filter preserves.
//
// Returns the SQL plus its args in left-to-right placeholder order (ids repeated
// once per injection point). ids must be non-empty; the scan side is the same
// issueops.ScanReadyWorkRowWithCounts.
func SearchCountsByIDsSQL(tables FilterTables, ids []string, includeWispReverseDeps, skipLabels bool) (string, []any) {
	inSQL, idArgs := InPlaceholders(ids)

	reverseBlockerSelect := fmt.Sprintf(`
				SELECT %[1]s AS dep_id
				FROM dependencies WHERE type = 'blocks' AND %[1]s IN (%[2]s)
	`, DepTargetExpr, inSQL)
	if includeWispReverseDeps {
		reverseBlockerSelect += fmt.Sprintf(`
				UNION ALL
				SELECT %[1]s AS dep_id
				FROM wisp_dependencies WHERE type = 'blocks' AND %[1]s IN (%[2]s)
		`, DepTargetExpr, inSQL)
	}

	labelsSelect := "l.labels_json AS labels_json"
	labelsJoin := fmt.Sprintf(`
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(label) AS labels_json
			FROM %s
			WHERE issue_id IN (%s)
			GROUP BY issue_id
		) l ON l.issue_id = i.id`, tables.Labels, inSQL)
	if skipLabels {
		labelsSelect = "NULL AS labels_json"
		labelsJoin = ""
	}

	sqlText := fmt.Sprintf(`
		SELECT %s,
			%s,
			COALESCE(dc.cnt, 0) AS dep_count,
			COALESCE(rc.cnt, 0) AS rdep_count,
			COALESCE(cc.cnt, 0) AS comment_count,
			pc.parent_id     AS parent_id,
			d.deps_json      AS deps_json
		FROM %s i
		%s
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			WHERE type = 'blocks' AND issue_id IN (%s)
			GROUP BY issue_id
		) dc ON dc.issue_id = i.id
		LEFT JOIN (
			SELECT dep_id, COUNT(*) AS cnt FROM (
				%s
			) all_blockers GROUP BY dep_id
		) rc ON rc.dep_id = i.id
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			WHERE issue_id IN (%s)
			GROUP BY issue_id
		) cc ON cc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id,
			       MIN(%s) AS parent_id
			FROM %s
			WHERE type = 'parent-child' AND issue_id IN (%s)
			GROUP BY issue_id
		) pc ON pc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(%s) AS deps_json
			FROM %s
			WHERE issue_id IN (%s)
			GROUP BY issue_id
		) d ON d.issue_id = i.id
		WHERE i.id IN (%s)
	`,
		ReadyWorkIssueColumns,
		labelsSelect,
		tables.Main,
		labelsJoin,
		tables.Dependencies, inSQL,
		reverseBlockerSelect,
		tables.Comments, inSQL,
		DepTargetExpr, tables.Dependencies, inSQL,
		DepJSONObject, tables.Dependencies, inSQL,
		inSQL,
	)

	// args must follow the placeholder order in sqlText: labels join (unless
	// skipped), dc, rc dependencies branch, rc wisp branch (if any), cc, pc, d,
	// then the driver.
	args := make([]any, 0, len(idArgs)*8)
	if !skipLabels {
		args = append(args, idArgs...)
	}
	args = append(args, idArgs...) // dc
	args = append(args, idArgs...) // rc dependencies
	if includeWispReverseDeps {
		args = append(args, idArgs...) // rc wisp_dependencies
	}
	args = append(args, idArgs...) // cc
	args = append(args, idArgs...) // pc
	args = append(args, idArgs...) // d
	args = append(args, idArgs...) // driver
	return sqlText, args
}
