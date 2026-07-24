package main

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

func parseDepSpecs(deps []string) ([]domain.DependencySpec, error) {
	var out []domain.DependencySpec
	for _, raw := range deps {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		spec, err := parseDepSpec(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func parseDepSpec(raw string) (domain.DependencySpec, error) {
	if !strings.Contains(raw, ":") {
		return domain.DependencySpec{
			Type:     types.DepBlocks,
			TargetID: raw,
		}, nil
	}

	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return domain.DependencySpec{}, fmt.Errorf("invalid dependency format %q, expected 'type:id' or 'id'", raw)
	}
	rawType := types.DependencyType(strings.TrimSpace(parts[0]))
	target := strings.TrimSpace(parts[1])

	spec := domain.DependencySpec{TargetID: target}
	switch rawType {
	case "depends-on", "blocked-by":
		spec.Type = types.DepBlocks
	case types.DepBlocks:
		spec.Type = types.DepBlocks
		spec.SwapDirection = true
	default:
		spec.Type = rawType
	}

	if !spec.Type.IsValid() {
		return domain.DependencySpec{}, fmt.Errorf("invalid dependency type %q (must be non-empty, max 50 chars); valid types: %s",
			spec.Type, createDepsAcceptedTypeList())
	}
	if !spec.Type.IsWellKnown() {
		return domain.DependencySpec{}, fmt.Errorf("unknown dependency type %q; valid types: %s",
			spec.Type, createDepsAcceptedTypeList())
	}
	return spec, nil
}

func buildWaitsFor(spawnerID, gate string) (*domain.WaitsForSpec, error) {
	spawnerID = strings.TrimSpace(spawnerID)
	if spawnerID == "" {
		return nil, nil
	}
	if gate == "" {
		gate = types.WaitsForAllChildren
	}
	if !types.IsValidWaitsForGate(gate) {
		return nil, fmt.Errorf("invalid --waits-for-gate value %q (valid: all-children, any-children)", gate)
	}
	return &domain.WaitsForSpec{SpawnerID: spawnerID, Gate: gate}, nil
}

func discoveredFromParent(deps []string) string {
	for _, raw := range deps {
		raw = strings.TrimSpace(raw)
		if raw == "" || !strings.Contains(raw, ":") {
			continue
		}
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 {
			continue
		}
		depType := types.DependencyType(strings.TrimSpace(parts[0]))
		target := strings.TrimSpace(parts[1])
		if depType == types.DepDiscoveredFrom && target != "" {
			return target
		}
	}
	return ""
}

func overlayYAMLPrefix(dbPrefix string) string {
	if v := strings.TrimSpace(config.GetString("issue-prefix")); v != "" {
		return v
	}
	return dbPrefix
}

// applyMintPrefix stamps the mint prefix onto an auto-minted issue on the
// proxied-server create path. (The embedded path routes every create through
// the storage.PrefixMintingStore decorator, so this helper is the proxied
// twin of that seam.) An explicit --prefix wins and is validated against the
// database prefix + allowed_prefixes; otherwise the workspace config prefix is
// applied when the shared database lists it in allowed_prefixes. It is a no-op
// for issues that are not auto-minting (explicit ID, wisp, etc. — see
// storage.StampWorkspaceMintPrefix).
func applyMintPrefix(issue *types.Issue, explicitPrefix, dbPrefix, allowedPrefixes string, force bool) error {
	if explicitPrefix != "" {
		if issue.Ephemeral || issue.NoHistory {
			return HandleError("cannot specify both --prefix and --ephemeral/--no-history (a wisp ID always mints under the '<db-prefix>-wisp' prefix)")
		}
		canonical, err := validation.ValidatePrefixAllowed(explicitPrefix, dbPrefix, allowedPrefixes, force)
		if err != nil {
			return err
		}
		if issue.ID == "" {
			issue.PrefixOverride = canonical
		}
		return nil
	}
	override := storage.WorkspaceMintPrefixOverride(overlayYAMLPrefix(""), dbPrefix, allowedPrefixes)
	storage.StampWorkspaceMintPrefix(issue, override)
	return nil
}
