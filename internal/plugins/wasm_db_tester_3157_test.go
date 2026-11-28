package plugins

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

// With no event deadline at all, dbQueryCap (3 s) is the only bound on one
// statement. Mutation: dbQueryCap raised / removed. Skipped under -short (it
// really waits ~3 s).
func TestDBCallCapBoundsAStatementWhenTheEventHasNoDeadline_3157(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 3 s cap")
	}
	f := newDBFixture(t)
	hs := f.plugin("com.test.db.cap", true, "db:own")
	mustExec(t, f.exec(context.Background(), hs, "CREATE TABLE t(a)", ""), 0)
	done := make(chan int32, 1)
	start := time.Now()
	go func() {
		n, _ := f.query(context.Background(), hs, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`, "", dbDstCap)
		done <- n
	}()
	select {
	case n := <-done:
		if el := time.Since(start); el < dbQueryCap-time.Second || el > dbQueryCap+3*time.Second {
			t.Fatalf("returned after %v, want about %v", el, dbQueryCap)
		}
		if n != hostErrInternal {
			t.Fatalf("capped query = %d, want -3", n)
		}
	case <-time.After(dbQueryCap + 8*time.Second):
		t.Fatal("runaway statement was not stopped by the per-call cap")
	}
	mustExec(t, f.exec(context.Background(), hs, "INSERT INTO t VALUES (1)", ""), 1)
}

// A read is interrupted before a sale-path write waiting for the single
// connection would give up with busy. Mutation: raise dbQueryCap to >= 5 s
// or lower plugindb.DefaultBusyWait -> red.
func TestQueryCapIsBelowTheWritersBusyWait_3157(t *testing.T) {
	if dbQueryCap >= plugindb.DefaultBusyWait {
		t.Fatalf("dbQueryCap %v >= BusyWait %v: a long report can starve a sale-path write", dbQueryCap, plugindb.DefaultBusyWait)
	}
}
