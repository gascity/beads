package storage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestWorkspaceMintPrefixOverride(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		db        string
		allowed   string
		want      string
	}{
		{"empty workspace -> no override", "", "bd", "riga,rigb", ""},
		{"workspace equals db -> no override", "bd", "bd", "bd,riga", ""},
		{"workspace equals db with trailing dash -> no override", "bd", "bd-", "bd,riga", ""},
		{"allowed empty -> no override (backward compat)", "riga", "bd", "", ""},
		{"workspace not listed -> no override", "riga", "bd", "rigb,rigc", ""},
		{"workspace listed and differs -> override", "riga", "bd", "riga,rigb", "riga"},
		{"listed with spaces -> override", "riga", "bd", " riga , rigb ", "riga"},
		{"listed with trailing dash -> override", "riga", "bd", "riga-,rigb", "riga"},
		{"workspace has trailing dash -> normalized override", "riga-", "bd", "riga,rigb", "riga"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storage.WorkspaceMintPrefixOverride(tt.workspace, tt.db, tt.allowed); got != tt.want {
				t.Errorf("WorkspaceMintPrefixOverride(%q,%q,%q) = %q; want %q",
					tt.workspace, tt.db, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestStampWorkspaceMintPrefix(t *testing.T) {
	tests := []struct {
		name     string
		issue    *types.Issue
		override string
		want     string
	}{
		{"stamps a plain auto-mint issue", &types.Issue{}, "riga", "riga"},
		{"empty override is a no-op", &types.Issue{}, "", ""},
		{"explicit id is not minting", &types.Issue{ID: "bd-1"}, "riga", ""},
		{"existing PrefixOverride wins", &types.Issue{PrefixOverride: "explicit"}, "riga", "explicit"},
		{"IDPrefix routing is left alone", &types.Issue{IDPrefix: "sub"}, "riga", ""},
		{"ephemeral wisp is not re-prefixed", &types.Issue{Ephemeral: true}, "riga", ""},
		{"no-history wisp is not re-prefixed", &types.Issue{NoHistory: true}, "riga", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage.StampWorkspaceMintPrefix(tt.issue, tt.override)
			if got := tt.issue.PrefixOverride; got != tt.want {
				t.Errorf("PrefixOverride = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestStampWorkspaceMintPrefix_NilIssue(t *testing.T) {
	// Must not panic.
	storage.StampWorkspaceMintPrefix(nil, "riga")
}

// ── decorator behavior over a fake store ────────────────────────────────────

// fakeMintStore is a minimal DoltStorage that mints an ID for issues with no
// ID, using PrefixOverride when present and its configured db prefix otherwise.
// This mirrors the real storage mint rule (both issueops and the uow path
// short-circuit to PrefixOverride) so tests can assert the decorator stamped it.
type fakeMintStore struct {
	storage.DoltStorage // nil: only the methods below are exercised
	dbPrefix            string
	allowed             string
	infraTypes          map[types.IssueType]bool
	getConfigErr        error
	created             []*types.Issue
	getConfigCalls      int
}

func (f *fakeMintStore) GetConfig(_ context.Context, key string) (string, error) {
	f.getConfigCalls++
	if f.getConfigErr != nil {
		return "", f.getConfigErr
	}
	switch key {
	case "issue_prefix":
		return f.dbPrefix, nil
	case "allowed_prefixes":
		return f.allowed, nil
	}
	return "", nil
}

func (f *fakeMintStore) IsInfraTypeCtx(_ context.Context, t types.IssueType) bool {
	return f.infraTypes[t]
}

func (f *fakeMintStore) CreateIssuesWithFullOptions(_ context.Context, issues []*types.Issue, _ string, _ storage.BatchCreateOptions) error {
	for _, issue := range issues {
		f.mint(issue)
	}
	return nil
}

func (f *fakeMintStore) mint(issue *types.Issue) {
	if issue.ID == "" {
		prefix := f.dbPrefix
		if issue.PrefixOverride != "" {
			prefix = issue.PrefixOverride
		}
		issue.ID = prefix + "-1"
	}
	f.created = append(f.created, issue)
}

func (f *fakeMintStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	f.mint(issue)
	return nil
}

func (f *fakeMintStore) CreateIssues(_ context.Context, issues []*types.Issue, _ string) error {
	for _, issue := range issues {
		f.mint(issue)
	}
	return nil
}

func (f *fakeMintStore) RunInTransaction(ctx context.Context, _ string, fn func(tx storage.Transaction) error) error {
	return fn(&fakeMintTx{f: f})
}

type fakeMintTx struct {
	storage.Transaction // nil: only the methods below are exercised
	f                   *fakeMintStore
}

func (t *fakeMintTx) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	t.f.mint(issue)
	return nil
}

func (t *fakeMintTx) CreateIssues(_ context.Context, issues []*types.Issue, _ string) error {
	for _, issue := range issues {
		t.f.mint(issue)
	}
	return nil
}

func TestPrefixMintingStore_EmptyPrefixReturnsInner(t *testing.T) {
	inner := &fakeMintStore{dbPrefix: "bd"}
	got := storage.NewPrefixMintingStore(inner, "")
	if got != storage.DoltStorage(inner) {
		t.Fatalf("empty workspace prefix should return the inner store unwrapped; got %T", got)
	}
}

func TestPrefixMintingStore_StampsWhenAllowed(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga,rigb"}
	store := storage.NewPrefixMintingStore(inner, "riga")

	issue := &types.Issue{Title: "work"}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.ID != "riga-1" {
		t.Errorf("minted ID = %q; want riga-1 (workspace prefix override)", issue.ID)
	}
}

func TestPrefixMintingStore_NoStampWhenNotAllowed(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: ""}
	store := storage.NewPrefixMintingStore(inner, "riga")

	issue := &types.Issue{Title: "work"}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.ID != "cityhq-1" {
		t.Errorf("minted ID = %q; want cityhq-1 (db prefix, backward compatible)", issue.ID)
	}
}

func TestPrefixMintingStore_ExplicitIDUntouched(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga"}
	store := storage.NewPrefixMintingStore(inner, "riga")

	issue := &types.Issue{ID: "riga-500", Title: "imported"}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.ID != "riga-500" {
		t.Errorf("explicit ID rewritten to %q; want riga-500", issue.ID)
	}
	if issue.PrefixOverride != "" {
		t.Errorf("explicit-ID issue got PrefixOverride %q; want empty", issue.PrefixOverride)
	}
}

func TestPrefixMintingStore_CreateIssuesStamps(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga"}
	store := storage.NewPrefixMintingStore(inner, "riga")

	issues := []*types.Issue{{Title: "a"}, {Title: "b"}}
	if err := store.CreateIssues(ctx, issues, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	for i, issue := range issues {
		if issue.ID != "riga-1" {
			t.Errorf("issue[%d] ID = %q; want riga-1", i, issue.ID)
		}
	}
}

func TestPrefixMintingStore_TransactionStamps(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga"}
	store := storage.NewPrefixMintingStore(inner, "riga")

	issue := &types.Issue{Title: "batched"}
	err := store.RunInTransaction(ctx, "test", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, issue, "tester")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	if issue.ID != "riga-1" {
		t.Errorf("tx-minted ID = %q; want riga-1", issue.ID)
	}
}

func TestPrefixMintingStore_SkipsInfraType(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{
		dbPrefix:   "cityhq",
		allowed:    "riga",
		infraTypes: map[types.IssueType]bool{types.IssueType("message"): true},
	}
	store := storage.NewPrefixMintingStore(inner, "riga")

	// An infra-type issue is promoted to a wisp at the store level, so the
	// decorator must NOT stamp it — it keeps the db prefix (which the wisp path
	// then turns into <db>-wisp-...).
	infra := &types.Issue{Title: "m", IssueType: types.IssueType("message")}
	if err := store.CreateIssue(ctx, infra, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if infra.PrefixOverride != "" {
		t.Errorf("infra-type issue got PrefixOverride %q; want empty (not stamped)", infra.PrefixOverride)
	}
	if infra.ID != "cityhq-1" {
		t.Errorf("infra-type minted %q; want cityhq-1", infra.ID)
	}

	// A normal work-bead type is still stamped.
	work := &types.Issue{Title: "w", IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, work, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if work.ID != "riga-1" {
		t.Errorf("work-bead minted %q; want riga-1", work.ID)
	}
}

func TestPrefixMintingStore_CreateIssuesWithFullOptionsStamps(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga"}
	store := storage.NewPrefixMintingStore(inner, "riga")

	bulk, ok := store.(storage.DoltStorage)
	if !ok {
		t.Fatal("decorator is not a DoltStorage")
	}
	issues := []*types.Issue{{Title: "seed a"}, {ID: "riga-99", Title: "explicit"}}
	if err := bulk.CreateIssuesWithFullOptions(ctx, issues, "tester", storage.BatchCreateOptions{}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions: %v", err)
	}
	if issues[0].ID != "riga-1" {
		t.Errorf("empty-ID import row minted %q; want riga-1 (workspace prefix)", issues[0].ID)
	}
	if issues[1].ID != "riga-99" {
		t.Errorf("explicit-ID import row rewritten to %q; want riga-99 untouched", issues[1].ID)
	}
}

func TestPrefixMintingStore_PropagatesConfigError(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga", getConfigErr: errFakeConfig}
	store := storage.NewPrefixMintingStore(inner, "riga")

	err := store.CreateIssue(ctx, &types.Issue{Title: "x"}, "tester")
	if err == nil {
		t.Fatal("expected CreateIssue to surface the config read error, got nil")
	}
	if len(inner.created) != 0 {
		t.Errorf("issue was persisted despite config resolution failure: %+v", inner.created)
	}
}

var errFakeConfig = fmt.Errorf("simulated config read failure")

func TestPrefixMintingStore_ResolvesConfigOnce(t *testing.T) {
	ctx := context.Background()
	inner := &fakeMintStore{dbPrefix: "cityhq", allowed: "riga"}
	store := storage.NewPrefixMintingStore(inner, "riga")

	for i := 0; i < 3; i++ {
		if err := store.CreateIssue(ctx, &types.Issue{Title: "x"}, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
	}
	// issue_prefix + allowed_prefixes read exactly once, then cached.
	if inner.getConfigCalls != 2 {
		t.Errorf("GetConfig called %d times; want 2 (resolved once, then cached)", inner.getConfigCalls)
	}
}
