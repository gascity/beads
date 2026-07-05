package dolt

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These pure-Go unit tests exercise the FIX #1 ignored-tx borrow path using the
// in-process mock driver from connection_pool_test.go. No Dolt sql-server is
// needed. An unroutable connStr makes the fresh-pool fallback fail fast and
// observably, so tests can distinguish a borrow (mock-backed, succeeds) from a
// fallback (real "mysql" driver, dial error).

const unroutableConnStr = "root@tcp(127.0.0.1:1)/x"

func newMockStore(t *testing.T, maxOpen int) (*DoltStore, *mockDriver) {
	t.Helper()
	db, drv := openMockDB(t)
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(5)
	s := &DoltStore{db: db, connStr: unroutableConnStr}
	t.Cleanup(func() { _ = db.Close() })
	return s, drv
}

func TestBorrowConnForIgnoredTxGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("MaxOpenConns==1 never touches the pool", func(t *testing.T) {
		s, drv := newMockStore(t, 1)
		if conn := s.borrowConnForIgnoredTx(ctx); conn != nil {
			_ = conn.Close()
			t.Fatal("borrow should return nil when MaxOpenConns==1")
		}
		if got := drv.opens.Load(); got != 0 {
			t.Fatalf("drv.opens = %d, want 0 (guard fires before any dial)", got)
		}
	})

	t.Run("exhausted pool falls back without waiting", func(t *testing.T) {
		s, _ := newMockStore(t, 2)
		c1, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("hold conn 1: %v", err)
		}
		defer c1.Close()
		c2, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("hold conn 2: %v", err)
		}
		defer c2.Close()

		start := time.Now()
		conn := s.borrowConnForIgnoredTx(ctx)
		elapsed := time.Since(start)
		if conn != nil {
			_ = conn.Close()
			t.Fatal("borrow should return nil when the pool is exhausted")
		}
		if elapsed >= ignoredTxBorrowTimeout {
			t.Fatalf("borrow took %v, want the InUse pre-check to return well under %v", elapsed, ignoredTxBorrowTimeout)
		}
	})

	t.Run("spare capacity borrows a conn", func(t *testing.T) {
		s, _ := newMockStore(t, 2)
		c1, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("hold conn 1: %v", err)
		}
		defer c1.Close()

		conn := s.borrowConnForIgnoredTx(ctx)
		if conn == nil {
			t.Fatal("borrow should succeed with spare capacity")
		}
		_ = conn.Close()
	})

	t.Run("unlimited pool always borrows", func(t *testing.T) {
		s, _ := newMockStore(t, 0)
		conn := s.borrowConnForIgnoredTx(ctx)
		if conn == nil {
			t.Fatal("borrow should succeed on an unlimited pool")
		}
		_ = conn.Close()
	})
}

func TestIgnoredTxBorrowReusesPooledConn(t *testing.T) {
	ctx := context.Background()
	s, drv := newMockStore(t, 10)

	const iterations = 5
	for i := 0; i < iterations; i++ {
		cleanup, tx, err := s.beginIgnoredTxOnBranch(ctx, "main")
		if err != nil {
			t.Fatalf("iter %d: beginIgnoredTxOnBranch: %v", i, err)
		}
		if tx == nil {
			t.Fatalf("iter %d: nil tx", i)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("iter %d: commit: %v", i, err)
		}
		cleanup()
	}

	if got := drv.opens.Load(); got != 1 {
		t.Fatalf("drv.opens = %d after %d borrows, want 1 (pooled conn reused, no per-write churn)", got, iterations)
	}
	if got := drv.countQuery("DOLT_CHECKOUT"); got != iterations {
		t.Fatalf("DOLT_CHECKOUT ran %d times, want %d (checkout on the borrowed conn each write)", got, iterations)
	}
}

func TestIgnoredTxFallsBackWhenPoolPinned(t *testing.T) {
	ctx := context.Background()
	s, drv := newMockStore(t, 1) // MaxOpenConns==1 → borrow guard forces the fallback

	cleanup, tx, err := s.beginIgnoredTxOnBranch(ctx, "main")
	if err == nil {
		// Resolve the tx BEFORE cleanup: cleanup closes the sql.Conn, which blocks
		// on closemu until an open Tx is committed/rolled back — a discarded open tx
		// would hang the package instead of failing this assertion cleanly.
		if tx != nil {
			_ = tx.Rollback()
		}
		cleanup()
		t.Fatal("expected the fallback dial to fail against the unroutable connStr")
	}
	if !strings.Contains(err.Error(), "failed to acquire ignored tx connection") {
		t.Fatalf("error = %q, want the fallback's %q", err.Error(), "failed to acquire ignored tx connection")
	}
	if got := drv.opens.Load(); got != 0 {
		t.Fatalf("drv.opens = %d, want 0 (fallback dials the real mysql driver, not the mock)", got)
	}
}

// TestIgnoredTxFallbackUsesCredentialCommand is the FIX#1→FIX#2 seam teeth test: the
// ignored-tx fallback must dial via openSQLDB(s.connStr, s.credCommand), NOT with an
// empty credential command. MaxOpenConns==1 forces the fallback; the unroutable connStr
// makes the dial fail, but the mysql connector resolves the credential BEFORE dialing,
// so a wired fallback invokes the resolver. Mutating the fallback to openSQLDB(s.connStr,
// "") makes the resolver never run here and this test fails.
func TestIgnoredTxFallbackUsesCredentialCommand(t *testing.T) {
	var calls int
	stubDialToken(t, func(_ context.Context, _ string) (string, error) {
		calls++
		return "tokFallback", nil
	})

	db, _ := openMockDB(t)
	db.SetMaxOpenConns(1) // borrow guard returns nil → the fallback path runs
	t.Cleanup(func() { _ = db.Close() })
	s := &DoltStore{db: db, connStr: unroutableConnStr, credCommand: "fallback-cred-probe"}

	cleanup, tx, err := s.beginIgnoredTxOnBranch(context.Background(), "main")
	if err == nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		cleanup()
		t.Fatal("expected the fallback dial to fail against the unroutable connStr")
	}
	if calls == 0 {
		t.Fatal("fallback dialed without resolving the credential command — openSQLDB was called with an empty credCmd (FIX#1→FIX#2 seam broken)")
	}
}

func TestIgnoredTxBorrowFallsThroughOnBadConn(t *testing.T) {
	ctx := context.Background()
	s, drv := newMockStore(t, 10)
	drv.failCheckout.Store(true) // borrow succeeds, but DOLT_CHECKOUT fails on it

	cleanup, tx, err := s.beginIgnoredTxOnBranch(ctx, "main")
	if err == nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		cleanup()
		t.Fatal("expected the fallback dial to fail after the borrowed conn's checkout failed")
	}
	if !strings.Contains(err.Error(), "failed to acquire ignored tx connection") {
		t.Fatalf("error = %q, want the fallback's %q (proves it fell through)", err.Error(), "failed to acquire ignored tx connection")
	}
	// The borrowed conn must have been closed back to the pool, not leaked.
	if inUse := s.db.Stats().InUse; inUse != 0 {
		t.Fatalf("s.db InUse = %d after fall-through, want 0 (borrowed conn returned to pool)", inUse)
	}
}
