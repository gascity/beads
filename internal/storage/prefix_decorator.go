// Package storage — prefix_decorator.go
//
// PrefixMintingStore is a decorator around DoltStorage that stamps a
// workspace-supplied mint prefix onto auto-minted issues. It exists so a
// single database shared by several workspaces (rigs/cities) can still mint
// per-workspace IDs: when the workspace config prefix (config.yaml
// `issue-prefix`) differs from the database's own `issue_prefix` AND is listed
// in the database's `allowed_prefixes`, freshly-minted issues take the
// workspace prefix instead of the database prefix.
//
// The mechanism is deliberately a decorator over the store's create surfaces
// (CreateIssue, CreateIssues, and the transaction handed out by
// RunInTransaction) rather than a change to any individual command. Every bd
// mint surface — create, quick, batch, markdown import, molecule cook — routes
// its writes through one of those store methods, so wrapping them here makes
// all of them inherit workspace-prefix minting without per-command edits. The
// storage layer already honors types.Issue.PrefixOverride (issueops and the
// domain/uow mint paths both short-circuit to it); this decorator is the seam
// that SETS it from workspace-visible configuration the storage layer cannot
// see on its own.
//
// Backward compatibility is load-bearing: NewPrefixMintingStore returns the
// inner store unwrapped when no workspace prefix is configured, and even when
// one is configured the stamp is applied only when the workspace prefix is
// listed in allowed_prefixes and differs from the database prefix. With an
// empty allowed_prefixes, or a workspace prefix that is not listed, minting is
// byte-for-byte unchanged.
package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/types"
)

// CapabilityWorkspacePrefixMint is the stable capability marker advertised by
// bd builds that ship workspace-prefix minting. It is declared here, in the
// package that implements the behavior, so a consumer (e.g. bdCapabilities in
// cmd/bd) that references it cannot compile without the decorator present —
// the marker cannot be cherry-picked apart from the feature.
const CapabilityWorkspacePrefixMint = "workspace-prefix-mint"

// WorkspaceMintPrefixOverride decides the prefix a workspace should mint under
// given the workspace config prefix, the database's own configured prefix, and
// the database's comma-separated allowed_prefixes. It returns the workspace
// prefix when it should override the database prefix for new mints, or "" when
// minting must fall back to the database prefix (the legacy behavior).
//
// The override applies only when the workspace prefix is non-empty, differs
// from the database prefix, and is present in allowed_prefixes. This keeps the
// change inert unless an operator has explicitly opted the prefix into the
// shared database's allow-list.
func WorkspaceMintPrefixOverride(workspacePrefix, dbPrefix, allowedPrefixes string) string {
	workspacePrefix = normalizePrefix(workspacePrefix)
	if workspacePrefix == "" {
		return ""
	}
	if workspacePrefix == normalizePrefix(dbPrefix) {
		// Already the database's own mint prefix — nothing to override.
		return ""
	}
	if !prefixInAllowedSet(workspacePrefix, allowedPrefixes) {
		return ""
	}
	return workspacePrefix
}

// StampWorkspaceMintPrefix sets issue.PrefixOverride to override for an issue
// that is about to be auto-minted, so the mint path adopts the workspace
// prefix. It is a no-op when override is empty or the issue is not eligible:
//
//   - an issue with an explicit ID (or a reserved child ID) is not minting;
//   - an issue that already carries a PrefixOverride (e.g. `bd create
//     --prefix`) has an operator-chosen prefix that wins;
//   - an issue with IDPrefix set is doing cross-rig sub-prefix routing, whose
//     "<db>-<sub>" shape the override would flatten;
//   - a wisp (ephemeral / no-history) keeps its "-wisp" suffix and lives on a
//     workspace-local store, so it is never re-prefixed here.
func StampWorkspaceMintPrefix(issue *types.Issue, override string) {
	if override == "" || issue == nil {
		return
	}
	if issue.ID != "" || issue.PrefixOverride != "" || issue.IDPrefix != "" {
		return
	}
	if issue.Ephemeral || issue.NoHistory {
		return
	}
	issue.PrefixOverride = override
}

func normalizePrefix(p string) string {
	return strings.TrimSuffix(strings.TrimSpace(p), "-")
}

func prefixInAllowedSet(prefix, allowedPrefixes string) bool {
	if allowedPrefixes == "" {
		return false
	}
	for _, allowed := range strings.Split(allowedPrefixes, ",") {
		if normalizePrefix(allowed) == prefix {
			return true
		}
	}
	return false
}

// PrefixMintingStore wraps a DoltStorage and stamps the resolved workspace mint
// prefix onto auto-minted issues before delegating to the inner store. Non-mint
// methods pass through unchanged via the embedded DoltStorage.
type PrefixMintingStore struct {
	DoltStorage             // embed for passthrough of non-overridden methods
	inner       DoltStorage // the real store
	wsPrefix    string      // workspace config prefix (config.yaml issue-prefix)

	once       sync.Once
	override   string
	resolveErr error
}

// NewPrefixMintingStore wraps store so auto-minted issues adopt the workspace
// prefix when the shared database allows it. When workspacePrefix is empty the
// inner store is returned unwrapped, guaranteeing zero behavior change for the
// common single-workspace case.
func NewPrefixMintingStore(store DoltStorage, workspacePrefix string) DoltStorage {
	if store == nil {
		return nil
	}
	if strings.TrimSpace(workspacePrefix) == "" {
		return store
	}
	return &PrefixMintingStore{
		DoltStorage: store,
		inner:       store,
		wsPrefix:    workspacePrefix,
	}
}

// Unwrap returns the underlying store, satisfying Unwrapper so UnwrapStore can
// peel this decorator when reaching for optional store interfaces.
func (p *PrefixMintingStore) Unwrap() DoltStorage { return p.inner }

// resolveOverride reads the database prefix and allowed_prefixes once and caches
// the resulting override for the lifetime of the store (a single CLI
// invocation), so a batch of creates does not re-read config per issue. A
// config read failure is cached and surfaced to the caller rather than
// silently falling back to the database prefix, so a transient blip cannot
// quietly mis-attribute a whole batch to the DB prefix.
func (p *PrefixMintingStore) resolveOverride(ctx context.Context) (string, error) {
	p.once.Do(func() {
		dbPrefix, err := p.inner.GetConfig(ctx, "issue_prefix")
		if err != nil {
			p.resolveErr = fmt.Errorf("resolve workspace mint prefix: read issue_prefix: %w", err)
			return
		}
		allowed, err := p.inner.GetConfig(ctx, "allowed_prefixes")
		if err != nil {
			p.resolveErr = fmt.Errorf("resolve workspace mint prefix: read allowed_prefixes: %w", err)
			return
		}
		p.override = WorkspaceMintPrefixOverride(p.wsPrefix, dbPrefix, allowed)
	})
	return p.override, p.resolveErr
}

// stamp resolves the override and stamps the issue, unless the issue is not an
// eligible top-level work-bead auto-mint. In addition to the base guards in
// StampWorkspaceMintPrefix, it excludes infra-type issues: the store-level
// CreateIssue/CreateIssues promote a configured infra type to a wisp BELOW this
// decorator (dolt/issues.go), so stamping the bare workspace prefix here would
// replace the "<db>-wisp" ID infix that wisp fast-paths key on.
func (p *PrefixMintingStore) stamp(ctx context.Context, issue *types.Issue) error {
	override, err := p.resolveOverride(ctx)
	if err != nil {
		return err
	}
	if override == "" || issue == nil {
		return nil
	}
	if p.inner.IsInfraTypeCtx(ctx, issue.IssueType) {
		return nil
	}
	StampWorkspaceMintPrefix(issue, override)
	return nil
}

// CreateIssue stamps the workspace mint prefix, then delegates.
func (p *PrefixMintingStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if err := p.stamp(ctx, issue); err != nil {
		return err
	}
	return p.inner.CreateIssue(ctx, issue, actor)
}

// CreateIssues stamps the workspace mint prefix on each auto-minted issue, then
// delegates.
func (p *PrefixMintingStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := p.stamp(ctx, issue); err != nil {
			return err
		}
	}
	return p.inner.CreateIssues(ctx, issues, actor)
}

// CreateIssuesWithFullOptions stamps the workspace mint prefix on each
// auto-minted (empty-ID) issue, then delegates. `bd import` seeds rows through
// this bulk surface, and rows without an explicit ID are minted by the engine —
// so without this override they would take the database prefix instead of the
// workspace prefix. Explicit-ID import rows are skipped by the stamp guard.
func (p *PrefixMintingStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts BatchCreateOptions) error {
	for _, issue := range issues {
		if err := p.stamp(ctx, issue); err != nil {
			return err
		}
	}
	return p.inner.CreateIssuesWithFullOptions(ctx, issues, actor, opts)
}

// RunInTransaction resolves the override up front and hands the callback a
// transaction that stamps auto-minted issues, so batch and molecule creates
// (which mint inside a transaction) inherit the workspace prefix too. The tx
// path does not exclude infra types: transaction-level CreateIssue routes on
// the Ephemeral/NoHistory flags only (createIssueWithDeps promotes infra types
// to Ephemeral before the tx), so the base wisp guard already covers them.
func (p *PrefixMintingStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx Transaction) error) error {
	override, err := p.resolveOverride(ctx)
	if err != nil {
		return err
	}
	return p.inner.RunInTransaction(ctx, commitMsg, func(tx Transaction) error {
		return fn(&prefixMintingTx{Transaction: tx, override: override})
	})
}

// prefixMintingTx wraps a Transaction, stamping the workspace mint prefix on
// auto-minted issues created inside the transaction.
type prefixMintingTx struct {
	Transaction
	override string
}

func (t *prefixMintingTx) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	StampWorkspaceMintPrefix(issue, t.override)
	return t.Transaction.CreateIssue(ctx, issue, actor)
}

func (t *prefixMintingTx) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		StampWorkspaceMintPrefix(issue, t.override)
	}
	return t.Transaction.CreateIssues(ctx, issues, actor)
}

// Compile-time interface satisfaction.
var (
	_ DoltStorage = (*PrefixMintingStore)(nil)
	_ Unwrapper   = (*PrefixMintingStore)(nil)
	_ Transaction = (*prefixMintingTx)(nil)
)
