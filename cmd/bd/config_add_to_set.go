package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
)

var configAddToSetCmd = &cobra.Command{
	Use:   "add-to-set <key> <value>...",
	Short: "Append value(s) to a comma-separated config set (transactional union; never removes existing entries)",
	Long: `Append one or more values to a comma-separated config value as a set union.

The read-modify-write runs in a single transaction so concurrent appenders
never lose each other's entries (a plain 'config get' + 'config set' is a
lost-update race). Existing entries are preserved and re-appending an existing
value is a no-op.

This is the safe way for several cities/rigs to register their prefixes in a
shared database's allowed_prefixes:

  bd config add-to-set allowed_prefixes riga rigb`,
	Args:          cobra.MinimumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-add-to-set")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		CheckReadonly("config add-to-set")

		key := args[0]
		values := args[1:]

		if msg, rejected := rejectProtectedConfigKey(key); rejected {
			fmt.Fprintln(os.Stderr, msg)
			return SilentExit()
		}

		if usesProxiedServer() {
			return runConfigAddToSetProxiedServer(rootCtx, key, values)
		}

		if err := ensureDirectMode("config add-to-set requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		merged, added, err := addToSetInStore(rootCtx, store, key, values)
		if err != nil {
			return HandleError("setting config: %v", err)
		}
		if len(added) > 0 {
			commandDidWrite.Store(true)
		}

		return reportAddToSet(key, merged, added)
	},
}

// addToSetInStore performs the transactional set-union append against a direct
// store: it reads the current value, unions the new values in, and writes the
// result — all in one transaction. The dolt store retries the transaction on a
// write-write conflict, so a re-read on retry sees a concurrent appender's
// committed write and unions on top of it rather than clobbering it. That is
// what makes concurrent appends to a shared allowed_prefixes lossless.
// addToSetAfterReadHook is a test seam invoked inside the transaction after the
// current value is read but before it is written, so tests can force two
// concurrent transactions to interleave (both read before either commits) and
// exercise the serialization-retry path. nil in production.
var addToSetAfterReadHook func()

func addToSetInStore(ctx context.Context, st storage.DoltStorage, key string, values []string) (merged string, added []string, err error) {
	err = st.RunInTransaction(ctx, "bd: config add-to-set "+key, func(tx storage.Transaction) error {
		current, err := tx.GetConfig(ctx, key)
		if err != nil {
			return err
		}
		if addToSetAfterReadHook != nil {
			addToSetAfterReadHook()
		}
		var changed bool
		merged, added, changed = unionConfigSet(current, values)
		if !changed {
			return nil
		}
		return tx.SetConfig(ctx, key, merged)
	})
	return merged, added, err
}

// runConfigAddToSetProxiedServer performs the same set-union append against a
// proxied dolt sql-server. The read-modify-write runs inside one uow
// transaction so concurrent cities appending to a shared org database's
// allowed_prefixes cannot clobber each other (the both-plumbings twin of the
// embedded path above).
func runConfigAddToSetProxiedServer(ctx context.Context, key string, values []string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	var merged string
	var added []string
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		current, err := uw.ConfigUseCase().GetConfig(ctx, key)
		if err != nil {
			return "", err
		}
		var changed bool
		merged, added, changed = unionConfigSet(current, values)
		if !changed {
			return "", nil
		}
		if err := uw.ConfigUseCase().SetConfig(ctx, key, merged); err != nil {
			return "", err
		}
		return "bd: config add-to-set " + key, nil
	})
	if err != nil {
		return HandleErrorRespectJSON("setting config: %v", err)
	}

	return reportAddToSet(key, merged, added)
}

func reportAddToSet(key, merged string, added []string) error {
	if jsonOutput {
		return outputJSON(map[string]any{
			"key":   key,
			"value": merged,
			"added": added,
		})
	}
	if len(added) == 0 {
		fmt.Printf("No change: %s already contains the given value(s)\n", key)
		return nil
	}
	fmt.Printf("Set %s = %s (added: %s)\n", key, merged, strings.Join(added, ", "))
	return nil
}

// unionConfigSet computes the set union of a comma-separated config value with
// additional values, preserving the existing entries and their order and
// appending only genuinely new ones (in the order given). It returns the merged
// value, the values that were actually added, and whether anything changed.
//
// Each incoming value is itself split on commas, so `add-to-set k a,b` adds two
// elements rather than one token "a,b" that would read back as a spurious pair.
// Tokens are canonicalized (whitespace trimmed, a trailing hyphen stripped —
// the stored form the prefix readers and the decorator both normalize to);
// empty tokens are dropped. It never removes an existing entry.
func unionConfigSet(current string, values []string) (merged string, added []string, changed bool) {
	var existing []string
	seen := make(map[string]struct{})
	add := func(raw string, isNew bool) {
		tok := canonicalSetToken(raw)
		if tok == "" {
			return
		}
		if _, ok := seen[tok]; ok {
			return
		}
		seen[tok] = struct{}{}
		existing = append(existing, tok)
		if isNew {
			added = append(added, tok)
		}
	}
	for _, tok := range strings.Split(current, ",") {
		add(tok, false)
	}
	for _, v := range values {
		for _, tok := range strings.Split(v, ",") {
			add(tok, true)
		}
	}
	return strings.Join(existing, ","), added, len(added) > 0
}

func canonicalSetToken(s string) string {
	return strings.TrimSuffix(strings.TrimSpace(s), "-")
}
