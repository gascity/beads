package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// The workspace-prefix-mint capability must be advertised so the Gas City
// topology migrations can probe for it without comparing version numbers.
func TestBDCapabilitiesIncludesWorkspacePrefixMint(t *testing.T) {
	if !slices.Contains(bdCapabilities(), storage.CapabilityWorkspacePrefixMint) {
		t.Fatalf("bdCapabilities() = %v; missing %q", bdCapabilities(), storage.CapabilityWorkspacePrefixMint)
	}
}

// The marker is tied to the feature seam: it is advertised only when the
// `bd create --prefix` flag is actually registered. A partial cherry-pick that
// dropped the flag would drop the marker too.
func TestBDCapabilitiesTiedToPrefixFlag(t *testing.T) {
	flagRegistered := createCmd.Flags().Lookup("prefix") != nil
	markerAdvertised := slices.Contains(bdCapabilities(), storage.CapabilityWorkspacePrefixMint)
	if flagRegistered != markerAdvertised {
		t.Fatalf("capability/flag tie broken: --prefix registered=%v but marker advertised=%v", flagRegistered, markerAdvertised)
	}
}

// `bd version --json` must surface the capabilities list (a stable machine
// probe surface).
func TestVersionJSONAdvertisesCapabilities(t *testing.T) {
	prev := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prev })

	out := captureStdout(t, func() error {
		return versionCmd.RunE(versionCmd, nil)
	})

	var payload struct {
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("version --json output not valid JSON: %v\noutput: %s", err, out)
	}
	if !slices.Contains(payload.Capabilities, "workspace-prefix-mint") {
		t.Errorf("version --json capabilities = %v; missing workspace-prefix-mint", payload.Capabilities)
	}
}
