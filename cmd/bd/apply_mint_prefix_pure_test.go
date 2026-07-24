package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// applyMintPrefix is the shared stamping seam for the proxied create surfaces.
// These pure cases pin its normalization, wisp rejection, and workspace-override
// behavior without a running sql-server.

func TestApplyMintPrefix_ExplicitNormalizesTrailingDash(t *testing.T) {
	issue := &types.Issue{Title: "x"}
	if err := applyMintPrefix(issue, "riga-", "cityhq", "riga,rigb", false); err != nil {
		t.Fatalf("applyMintPrefix: %v", err)
	}
	if issue.PrefixOverride != "riga" {
		t.Errorf("PrefixOverride = %q; want normalized 'riga'", issue.PrefixOverride)
	}
}

func TestApplyMintPrefix_ExplicitRejectsWisp(t *testing.T) {
	for _, w := range []*types.Issue{{Title: "e", Ephemeral: true}, {Title: "n", NoHistory: true}} {
		if err := applyMintPrefix(w, "riga", "cityhq", "riga", false); err == nil {
			t.Errorf("expected error for --prefix on a wisp (%+v), got nil", w)
		}
	}
}

func TestApplyMintPrefix_ExplicitRejectsUnallowed(t *testing.T) {
	issue := &types.Issue{Title: "x"}
	if err := applyMintPrefix(issue, "rigc", "cityhq", "riga,rigb", false); err == nil {
		t.Fatal("expected prefix-mismatch error, got nil")
	}
	if issue.PrefixOverride != "" {
		t.Errorf("rejected prefix still stamped: %q", issue.PrefixOverride)
	}
}

func TestApplyMintPrefix_WorkspaceOverride(t *testing.T) {
	config.ResetForTesting()
	_ = config.Initialize()
	config.Set("issue-prefix", "riga")
	t.Cleanup(config.ResetForTesting)

	stamped := &types.Issue{Title: "x"}
	if err := applyMintPrefix(stamped, "", "cityhq", "riga,rigb", false); err != nil {
		t.Fatalf("applyMintPrefix: %v", err)
	}
	if stamped.PrefixOverride != "riga" {
		t.Errorf("PrefixOverride = %q; want 'riga' (workspace override)", stamped.PrefixOverride)
	}

	// Not in allowed_prefixes -> no stamp (backward compatible).
	unstamped := &types.Issue{Title: "y"}
	if err := applyMintPrefix(unstamped, "", "cityhq", "", false); err != nil {
		t.Fatalf("applyMintPrefix: %v", err)
	}
	if unstamped.PrefixOverride != "" {
		t.Errorf("PrefixOverride = %q; want empty (riga not allowed)", unstamped.PrefixOverride)
	}
}
