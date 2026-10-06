//go:build integration

package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/giovanni/lumino/internal/db/sqlbase"
)

// TestSessionLifecycle covers session regressions found in review (on
// PostgreSQL): closed sessions don't reconnect, Close stops a running
// statement, closing a partly read cursor keeps the transaction, a failed
// raw BEGIN opens nothing.
func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	conn, s := pgrOpen(t, "postgres", nil)
	pgrExec(t, s, "DROP TABLE IF EXISTS lc_t")
	pgrExec(t, s, "CREATE TABLE lc_t (id int)")
	t.Cleanup(func() {
		if s2, err := conn.Session(ctx); err == nil {
			if r, err := s2.Execute(ctx, "DROP TABLE IF EXISTS lc_t"); err == nil {
				r.Close()
			}
			s2.Close()
		}
	})

	t.Run("CursorCloseKeepsTx", func(t *testing.T) {
		if err := s.Begin(ctx); err != nil {
			t.Fatal(err)
		}
		pgrExec(t, s, "INSERT INTO lc_t VALUES (1)")
		r, err := s.Execute(ctx, "SELECT g, repeat('x', 200) FROM generate_series(1, 2000000) g")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.Fetch(ctx, 100); err != nil {
			t.Fatal(err)
		}
		pgrExec(t, s, "INSERT INTO lc_t VALUES (2)") // closes the cursor
		if err := s.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if n := pgrScalar(t, s, "SELECT count(*) FROM lc_t"); n != "2" {
			t.Fatalf("rows after commit: %s", n)
		}
	})

	t.Run("FailedRawBegin", func(t *testing.T) {
		if r, err := s.Execute(ctx, "BEGIN ISOLATION LEVEL BOGUS"); err == nil {
			r.Close()
			t.Fatal("bogus BEGIN accepted")
		}
		if s.TxActive() {
			t.Fatal("a failed BEGIN left a transaction open")
		}
		if r, err := s.Execute(ctx, "BEGIN ISOLATION LEVEL READ COMMITTED"); err != nil {
			t.Fatal(err)
		} else {
			r.Close()
		}
		if !s.TxActive() {
			t.Fatal("raw BEGIN not tracked")
		}
		pgrExec(t, s, "ROLLBACK")
		if s.TxActive() {
			t.Fatal("raw ROLLBACK not tracked")
		}
	})

	t.Run("CloseStopsAndRefuses", func(t *testing.T) {
		s2, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			r, err := s2.Execute(ctx, "SELECT pg_sleep(20)")
			if err == nil {
				r.Close()
			}
			done <- err
		}()
		time.Sleep(300 * time.Millisecond)
		start := time.Now()
		s2.Close()
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("Close waited %v for the running statement", d)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled statement reported success")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("running statement not stopped by Close")
		}
		if _, err := s2.Execute(ctx, "SELECT 1"); !errors.Is(err, sqlbase.ErrSessionClosed) {
			t.Fatalf("execute after close: %v", err)
		}
		if err := s2.Commit(ctx); !errors.Is(err, sqlbase.ErrSessionClosed) {
			t.Fatalf("commit after close: %v", err)
		}
	})
}
