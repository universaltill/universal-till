package cloudsync

import (
	"context"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/entitlement"
)

// TestMain opens ADR-0148's paid-only gate for the package's tests by
// default: the many tick tests here are about what a check-in DOES once it
// runs (directives, snapshots, entitlement caching, backoff), and predate
// the gate. The gate itself is tested against the real
// entitlement.SyncAllowed via useRealSyncGate (sync_gate_test.go).
func TestMain(m *testing.M) {
	syncAllowedFn = func(context.Context, entitlement.Reader) bool { return true }
	os.Exit(m.Run())
}
