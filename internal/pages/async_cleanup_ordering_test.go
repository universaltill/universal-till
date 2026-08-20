package pages

import (
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#514: every Deps-construction test helper in this package must
// drain Deps.AsyncWork BEFORE closing the *sql.DB it handed that Deps.
//
// completeTender fires detached goroutines (printReceiptAsync,
// printKitchenAsync, the invoice print path) that keep touching d.Db and
// d.Settings after the handler has already responded. If a helper's
// t.Cleanup closes the DB first, those goroutines run against a closed
// handle — "sql: database is closed" log noise at best, and (with
// t.TempDir()'s RemoveAll racing SQLite's sidecar files) the
// "directory not empty" flake ut-docs#425 originally chased at worst.
//
// The fix is purely cleanup ORDERING: Go runs t.Cleanup functions LIFO,
// so registering t.Cleanup(dp.WaitForAsyncWork) AFTER the helper's own
// db.Close cleanup makes the drain run FIRST. This test pins that
// ordering per helper rather than trusting a reading of the source.
func TestTestDepsHelpers_DrainAsyncWorkBeforeClosingDB(t *testing.T) {
	helpers := []struct {
		name  string
		build func(*testing.T) *common.Deps
	}{
		{"newPOSTestDeps", func(t *testing.T) *common.Deps { _, dp := newPOSTestDeps(t); return dp }},
		{"newRefundTestDeps", func(t *testing.T) *common.Deps { _, dp, _ := newRefundTestDeps(t); return dp }},
		{"newInvoiceTestDeps", func(t *testing.T) *common.Deps { _, dp := newInvoiceTestDeps(t); return dp }},
		{"newSyncSalesTestDeps", func(t *testing.T) *common.Deps { _, dp := newSyncSalesTestDeps(t); return dp }},
		{"setupSelfOrderShopDeps", func(t *testing.T) *common.Deps { dp, _ := setupSelfOrderShopDeps(t); return dp }},
	}

	for _, h := range helpers {
		t.Run(h.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				queryErr error
				ran      bool
			)
			started := make(chan struct{})

			// The inner sub-test owns the helper's *testing.T, so its
			// cleanups (the DB close, and the drain this test is about)
			// run when it returns — while we are still here to inspect
			// what the detached goroutine saw.
			t.Run("teardown", func(t *testing.T) {
				dp := h.build(t)
				dp.AsyncWork.Add(1)
				go func() {
					defer dp.AsyncWork.Done()
					close(started)
					// Stand in for printReceiptAsync: still in flight when
					// the test body returns, still needing a live handle.
					time.Sleep(50 * time.Millisecond)
					var n int
					err := dp.Db.QueryRow(`SELECT 1`).Scan(&n)
					mu.Lock()
					queryErr, ran = err, true
					mu.Unlock()
				}()
				<-started
			})

			mu.Lock()
			defer mu.Unlock()
			if !ran {
				t.Fatalf("%s: cleanup returned before the AsyncWork goroutine ran at all — "+
					"the drain is not registered (or not registered after the db.Close cleanup)", h.name)
			}
			if queryErr != nil {
				t.Fatalf("%s: background AsyncWork goroutine hit a closed DB during cleanup: %v — "+
					"register t.Cleanup(dp.WaitForAsyncWork) AFTER the helper's db.Close cleanup "+
					"so LIFO order drains first (ut-docs#514)", h.name, queryErr)
			}
		})
	}
}
