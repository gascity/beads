package dolt

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// stubDialToken swaps the per-dial token resolution for the duration of a test,
// restoring it on cleanup. Tests using it must NOT run in parallel —
// resolveDialToken is a package global.
func stubDialToken(t *testing.T, fn func(ctx context.Context, command string) (string, error)) {
	t.Helper()
	orig := resolveDialToken
	resolveDialToken = fn
	t.Cleanup(func() { resolveDialToken = orig })
}

func TestCredentialBeforeConnect_SetsUserFromHelper(t *testing.T) {
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		return "tokA", nil
	})

	hook := credentialBeforeConnect("helper-sets-user")
	cfg := mysql.NewConfig()
	if err := hook(context.Background(), cfg); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if cfg.User != "tokA" {
		t.Fatalf("cfg.User = %q, want %q", cfg.User, "tokA")
	}
}

func TestCredentialBeforeConnect_HelperErrorPropagates(t *testing.T) {
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("boom")
	})

	hook := credentialBeforeConnect("helper-errors")
	cfg := mysql.NewConfig()
	cfg.User = "unchanged"
	err := hook(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected an error when the helper fails (fail-closed)")
	}
	if !strings.Contains(err.Error(), "resolving dolt credential command") {
		t.Fatalf("error = %q, want it to wrap %q", err.Error(), "resolving dolt credential command")
	}
	if cfg.User != "unchanged" {
		t.Fatalf("cfg.User = %q, want it left unchanged on error", cfg.User)
	}
}

// The DEFAULT per-dial resolution (no stub) runs the real helper via the creds
// command source and returns the token — the end-to-end path a hosted dial takes.
func TestResolveGatewayDialToken_DefaultPath(t *testing.T) {
	tok, err := resolveGatewayDialToken(context.Background(), "printf tok-dial-default")
	if err != nil {
		t.Fatalf("resolveGatewayDialToken: %v", err)
	}
	if tok != "tok-dial-default" {
		t.Fatalf("token = %q, want %q", tok, "tok-dial-default")
	}
}

// The per-dial path applies the same DSN-charset validation as the open-time
// ApplyGatewayCredential: a token with ':' '@' or '/' is refused, not presented.
func TestResolveGatewayDialToken_RejectsBadCharToken(t *testing.T) {
	_, err := resolveGatewayDialToken(context.Background(), "printf tok@dial-bad")
	if err == nil {
		t.Fatal("expected an error for a token with a DSN-breaking character")
	}
}

func TestOpenSQLDB_EmptyCommandNeverRunsHelper(t *testing.T) {
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		t.Fatal("token resolution must not be invoked when credCmd is empty")
		return "", nil
	})

	db, err := openSQLDB("root@tcp(127.0.0.1:3307)/beads", "")
	if err != nil {
		t.Fatalf("openSQLDB(validDSN, \"\"): %v", err)
	}
	if db == nil {
		t.Fatal("openSQLDB returned a nil *sql.DB")
	}
	_ = db.Close()
}

func TestOpenSQLDB_InvalidDSNWithCommand(t *testing.T) {
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		t.Fatal("token resolution must not be invoked when the DSN fails to parse")
		return "", nil
	})

	_, err := openSQLDB("not-a-dsn", "some-cmd")
	if err == nil {
		t.Fatal("expected a parse error for an invalid DSN")
	}
	if !strings.Contains(err.Error(), "parsing DSN for credential connector") {
		t.Fatalf("error = %q, want it to wrap %q", err.Error(), "parsing DSN for credential connector")
	}
}

func TestOpenSQLDB_ConnectorConfiguredIsLazy(t *testing.T) {
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		t.Fatal("token resolution must not run at construction — BeforeConnect is dial-time only")
		return "", nil
	})

	db, err := openSQLDB("root@tcp(127.0.0.1:3307)/beads", "lazy-cmd")
	if err != nil {
		t.Fatalf("openSQLDB(validDSN, cmd): %v", err)
	}
	if db == nil {
		t.Fatal("openSQLDB returned a nil *sql.DB")
	}
	_ = db.Close()
}

// TestOpenSQLDB_WiresBeforeConnectOnDial is the FIX #2 teeth test: it fails if the
// BeforeConnect hook is not actually attached to the connector openSQLDB builds.
// The mysql connector resolves BeforeConnect BEFORE it dials, so a dial against an
// unroutable host still invokes the credential resolution — observing that invocation
// proves the hook is wired. Deleting the cfg.Apply(BeforeConnect(...)) line in
// openSQLDB makes the resolver never run here, and this test then fails (whereas the
// direct credentialBeforeConnect tests above would still pass — the gap this closes).
func TestOpenSQLDB_WiresBeforeConnectOnDial(t *testing.T) {
	var calls int
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		calls++
		return "tokWired", nil
	})

	db, err := openSQLDB("root@tcp(127.0.0.1:1)/x", "wire-probe-cmd")
	if err != nil {
		t.Fatalf("openSQLDB: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(context.Background())
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected the dial against the unroutable host to fail")
	}
	if calls == 0 {
		t.Fatal("credential resolution never ran on dial — openSQLDB did not wire BeforeConnect; FIX #2's per-dial credential is absent")
	}
}

// TestIsAuthError classifies the MySQL access-denied (1045) signal that drives
// credential-cache invalidation, and rejects non-auth errors.
func TestIsAuthError(t *testing.T) {
	if !isAuthError(&mysql.MySQLError{Number: 1045, Message: "Access denied for user"}) {
		t.Fatal("MySQL 1045 must be an auth error")
	}
	if isAuthError(&mysql.MySQLError{Number: 1213, Message: "deadlock found"}) {
		t.Fatal("MySQL 1213 (deadlock) is not an auth error")
	}
	if !isAuthError(fmt.Errorf("Error 1045: Access denied for user 'x'@'y'")) {
		t.Fatal("an access-denied string must match")
	}
	if isAuthError(fmt.Errorf("dial tcp: connection refused")) {
		t.Fatal("connection refused is not an auth error")
	}
	if isAuthError(nil) {
		t.Fatal("nil is not an auth error")
	}
}
