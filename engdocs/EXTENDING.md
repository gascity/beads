# Extending bd

This file documents contracts for code that embeds bd or calls its storage
layer directly. It is not a user-facing CLI guide.

## Lite issue projection

`SearchIssues(ctx, query, filter)` accepts `types.IssueFilter`. Setting
`filter.Lite` to `true` selects an issue shape that omits these heavy body
columns:

- `description`
- `design`
- `acceptance_criteria`
- `notes`
- `payload`
- `waiters`

All other issue fields, including metadata, labels, dependencies when
requested, and lease timestamps, retain their normal semantics. Membership,
filtering, and ordering are unchanged because Lite uses the same search path as
full issue hydration.

Returned records have `Issue.IsLitePartial == true`. Code must not interpret
the omitted fields' zero values as stored values. Use `GetIssue(ctx, id)` when a
caller needs the full body for a specific issue; `GetIssue` always performs full
hydration.

Lite defaults to `false`, so existing callers retain full issue hydration and
receive `IsLitePartial == false`. The marker is internal (`json:"-"`) and does
not alter serialized issue responses.

The projection is honored by stores that delegate search to
`issueops.SearchIssuesInTx`, including the hosted `sqlkit` store. The canonical
column lists and scanners live in `internal/storage/issueops/scan.go`; a parity
test requires every full-projection column to remain classified as retained or
deferred.
