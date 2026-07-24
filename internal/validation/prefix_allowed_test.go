package validation

import (
	"strings"
	"testing"
)

func TestValidatePrefixAllowed(t *testing.T) {
	tests := []struct {
		name          string
		prefix        string
		db            string
		allowed       string
		force         bool
		wantErr       bool
		wantCanonical string
	}{
		{"force bypasses everything", "zzz", "bd", "", true, false, "zzz"},
		{"empty db prefix accepts anything", "zzz", "", "", false, false, "zzz"},
		{"prefix equals db prefix", "bd", "bd", "", false, false, "bd"},
		{"prefix equals db prefix with trailing dash", "bd", "bd-", "", false, false, "bd"},
		{"prefix in allowed set", "riga", "bd", "riga,rigb", false, false, "riga"},
		{"prefix in allowed set with spaces and dash", "riga", "bd", " riga- , rigb ", false, false, "riga"},
		{"trailing dash on the flag is normalized", "riga-", "bd", "riga,rigb", false, false, "riga"},
		{"surrounding whitespace on the flag is normalized", " riga ", "bd", "riga,rigb", false, false, "riga"},
		{"prefix not in allowed set", "rigc", "bd", "riga,rigb", false, true, ""},
		{"prefix mismatch with empty allowed", "riga", "bd", "", false, true, ""},
		{"empty prefix is rejected when db prefix set", "", "bd", "riga", false, true, ""},
		{"empty prefix is rejected even with force", "", "bd", "riga", true, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, err := ValidatePrefixAllowed(tt.prefix, tt.db, tt.allowed, tt.force)
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if canonical != tt.wantCanonical {
				t.Errorf("canonical = %q; want %q", canonical, tt.wantCanonical)
			}
		})
	}
}

func TestValidatePrefixAllowed_ErrorIsHelpful(t *testing.T) {
	_, err := ValidatePrefixAllowed("rigc", "bd", "riga,rigb", false)
	if err == nil {
		t.Fatal("expected error")
	}
	// The message should name the db prefix, the allowed set, and --force so
	// operators can see why the prefix was rejected and how to override it.
	msg := err.Error()
	for _, want := range []string{"bd", "riga,rigb", "rigc", "--force"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}
