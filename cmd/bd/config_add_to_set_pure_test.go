package main

import (
	"reflect"
	"testing"
)

func TestUnionConfigSet(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		values      []string
		wantMerged  string
		wantAdded   []string
		wantChanged bool
	}{
		{
			name:        "append to empty",
			current:     "",
			values:      []string{"riga", "rigb"},
			wantMerged:  "riga,rigb",
			wantAdded:   []string{"riga", "rigb"},
			wantChanged: true,
		},
		{
			name:        "union preserves existing and appends new, dedups overlap",
			current:     "riga,rigb",
			values:      []string{"rigb", "rigc"},
			wantMerged:  "riga,rigb,rigc",
			wantAdded:   []string{"rigc"},
			wantChanged: true,
		},
		{
			name:        "idempotent re-append is a no-op and never removes",
			current:     "riga,rigb",
			values:      []string{"riga"},
			wantMerged:  "riga,rigb",
			wantAdded:   nil,
			wantChanged: false,
		},
		{
			name:        "trims spaces and dedups existing dups",
			current:     "riga, rigb ,riga",
			values:      []string{" rigc "},
			wantMerged:  "riga,rigb,rigc",
			wantAdded:   []string{"rigc"},
			wantChanged: true,
		},
		{
			name:        "empty and whitespace tokens ignored",
			current:     "riga",
			values:      []string{"", "  "},
			wantMerged:  "riga",
			wantAdded:   nil,
			wantChanged: false,
		},
		{
			name:        "existing order preserved, new appended in order",
			current:     "zeta,alpha",
			values:      []string{"mu", "alpha", "nu"},
			wantMerged:  "zeta,alpha,mu,nu",
			wantAdded:   []string{"mu", "nu"},
			wantChanged: true,
		},
		{
			name:        "a comma-containing value is split into elements",
			current:     "base",
			values:      []string{"riga,rigb"},
			wantMerged:  "base,riga,rigb",
			wantAdded:   []string{"riga", "rigb"},
			wantChanged: true,
		},
		{
			name:        "trailing dashes are normalized on write",
			current:     "base",
			values:      []string{"riga-", "rigb-"},
			wantMerged:  "base,riga,rigb",
			wantAdded:   []string{"riga", "rigb"},
			wantChanged: true,
		},
		{
			name:        "dash-suffixed value already present is a no-op",
			current:     "base,riga",
			values:      []string{"riga-"},
			wantMerged:  "base,riga",
			wantAdded:   nil,
			wantChanged: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged, added, changed := unionConfigSet(tt.current, tt.values)
			if merged != tt.wantMerged {
				t.Errorf("merged = %q; want %q", merged, tt.wantMerged)
			}
			if !reflect.DeepEqual(added, tt.wantAdded) {
				t.Errorf("added = %v; want %v", added, tt.wantAdded)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v; want %v", changed, tt.wantChanged)
			}
		})
	}
}
