package dolt

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestParseCredential(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantTok string
		wantExp bool // expect a non-zero expiry
		wantErr bool
	}{
		{"bare token (EIA-shaped)", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig\n", "eyJhbGciOiJSUzI1NiJ9.eyJvIjoib18xIn0.sig", false, false},
		{"execcredential token+exp", `{"token":"abc","expirationTimestamp":"2099-01-02T15:04:05Z"}`, "abc", true, false},
		{"gasworks access_token+expires_in", `{"access_token":"xyz","expires_in":90,"token_type":"DPoP"}`, "xyz", true, false},
		{"json without token", `{"foo":"bar"}`, "", false, true},
		{"unparseable json", `{not json`, "", false, true},
		{"empty output", "   \n", "", false, true},
		{"bare with whitespace (error message)", "access denied: nope", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, exp, err := parseCredential([]byte(c.in))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got token=%q", tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tok != c.wantTok {
				t.Fatalf("token = %q, want %q", tok, c.wantTok)
			}
			if c.wantExp && exp.IsZero() {
				t.Fatal("expected a non-zero expiry")
			}
			if !c.wantExp && !exp.IsZero() {
				t.Fatalf("expected zero expiry, got %v", exp)
			}
		})
	}
}

// resolveCredentialToken caches by command until near expiry, then re-runs the helper.
func TestResolveCredentialTokenCachesUntilExpiry(t *testing.T) {
	// Isolate the package cache + runner for this test.
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })

	var calls int
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		calls++
		// Long-lived expiry so the cache holds across the second call.
		return []byte(fmt.Sprintf(`{"token":"tok-%d","expirationTimestamp":%q}`, calls,
			time.Now().Add(time.Hour).Format(time.RFC3339))), nil
	}

	tok1, err := resolveCredentialToken("helper --x")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	tok2, err := resolveCredentialToken("helper --x")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if tok1 != tok2 || calls != 1 {
		t.Fatalf("expected one cached helper call, got calls=%d tok1=%q tok2=%q", calls, tok1, tok2)
	}

	// A different command is a different cache key → a fresh run.
	if _, err := resolveCredentialToken("helper --y"); err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a fresh run for a new command, got calls=%d", calls)
	}
}

func TestResolveCredentialTokenPropagatesHelperError(t *testing.T) {
	credCacheMu.Lock()
	credCache = map[string]cachedCred{}
	credCacheMu.Unlock()
	orig := credRunner
	t.Cleanup(func() { credRunner = orig })
	credRunner = func(_ context.Context, _ string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	if _, err := resolveCredentialToken("broken-helper"); err == nil {
		t.Fatal("expected an error when the helper fails")
	}
}
