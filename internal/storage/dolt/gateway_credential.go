package dolt

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/creds"
)

// ApplyGatewayCredential resolves the server credential command
// (BEADS_DOLT_CREDENTIAL_COMMAND) into cfg: the command's short-lived token becomes the
// connection (MySQL) username, and the connection is marked as targeting a gateway
// server. It is the vendor-neutral credential-process idiom (kubectl ExecCredential /
// AWS credential_process / git credential helper) — bd runs a command the operator
// configures and knows nothing of the issuer.
//
// Presenting the token as the username is how bd connects to an authenticating gateway
// server: the server verifies the token, routes by the project database, and owns the
// schema (so bd stays a passive client — it does not create databases or run
// migrations). This is the one place a token is placed in the username slot; the direct
// SQL backends only ever place a secret in the password slot.
//
// It is a no-op ((false, nil)) when cfg.ServerUser is already set (a caller/flag preset
// wins) or no command is configured. It fails closed: a configured-but-failing command
// aborts the open and never falls back to the static/root user — a wrong identity must
// never connect. It also disables auto-start: a gateway server is externally managed, so
// spawning a local dolt server would shadow it.
//
// The token resolved here only SEEDS the DSN username (and validates the helper at
// open time — a broken helper still aborts store construction). The command itself is
// threaded into cfg.CredentialCommand so the store's per-dial connector (connector.go)
// re-resolves a live token at each new physical connection dial: pooled connections
// are never re-authenticated by the server, so they survive token rotation, while
// new/replacement dials authenticate with a fresh token instead of the stale seed.
func ApplyGatewayCredential(ctx context.Context, fileCfg *configfile.Config, cfg *Config) (bool, error) {
	if cfg.ServerUser != "" {
		return false, nil
	}
	command := fileCfg.GetDoltCredentialCommand()
	tok, ok, err := resolveGatewayToken(ctx, command)
	if err != nil || !ok {
		return false, err
	}
	cfg.ServerUser = tok
	cfg.CredentialCommand = command
	cfg.Gateway = true
	cfg.DisableAutoStart = true
	return true, nil
}

// resolveGatewayToken resolves command into a token safe to present as the connection
// (MySQL) username. It is the shared resolution path for the open-time eager seed
// (ApplyGatewayCredential) and the per-dial connector hook (resolveGatewayDialToken),
// so both apply identical fail-closed validation. ok is false when no command is
// configured. The creds command cache makes repeated calls a map hit until the token
// nears expiry.
func resolveGatewayToken(ctx context.Context, command string) (string, bool, error) {
	cred, ok, err := creds.ResolveLadder(ctx, creds.CommandSource{
		Command: command,
		Kind:    creds.KindIdentity,
		Label:   "BEADS_DOLT_CREDENTIAL_COMMAND",
	})
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	// Defense in depth: the token is presented AS the username, so a non-identity
	// credential must never reach this slot.
	if cred.Kind != creds.KindIdentity {
		return "", false, fmt.Errorf("dolt: credential from %s is not an identity; refusing to present it as the connection username", cred.Source)
	}
	// The token becomes the DSN username; the go-sql-driver grammar has no escaping for
	// the user field, so a ':' '@' or '/' would silently mis-split it into user/password.
	// Reject rather than connect with a mangled identity. (JWTs are base64url + '.', safe.)
	if strings.ContainsAny(cred.Value, ":@/") {
		return "", false, fmt.Errorf("dolt: credential from %s contains a character (:, @, or /) that cannot be placed in the connection username", cred.Source)
	}
	// cred.Username (a dynamic user/password pair) is meaningless here: the token IS the
	// username. Ignored deliberately.
	return cred.Value, true, nil
}

// resolveGatewayDialToken resolves a live gateway token for one new physical
// connection dial — the default behind connector.go's BeforeConnect hook. The store
// only builds a credential connector when a command is configured, so an
// unconfigured resolution here is a wiring bug and fails closed.
func resolveGatewayDialToken(ctx context.Context, command string) (string, error) {
	tok, ok, err := resolveGatewayToken(ctx, command)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("dolt: credential connector built without a credential command")
	}
	return tok, nil
}
