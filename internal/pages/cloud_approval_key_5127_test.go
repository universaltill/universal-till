package pages

import (
	"crypto/ed25519"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#5127 (design §2 H2, review M-1): every /sync device report carries
// the till's approval_kid so the cloud's quarantine rule sees it each time —
// from a main till and from an additional till alike (any till can move). The
// key itself is NOT in the sync report: the cloud ignores it there, and the
// till reports it by POST /v1/stores/devices/approval-key (internal/enroll).
func checkApprovalReport(t *testing.T, storeID string, extra map[string]any) {
	t.Helper()
	if _, ok := extra["approval_key"]; ok {
		t.Fatal("approval_key is in the sync report; the cloud drops it there")
	}
	kid, _ := extra["approval_kid"].(string)
	if len(kid) != 16 {
		t.Fatalf("approval_kid = %#v, want a 16-hex key id", extra["approval_kid"])
	}
	k, err := approvalkey.Shared().LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if want := approvalkey.KID(k.Public().(ed25519.PublicKey)); kid != want {
		t.Fatalf("approval_kid = %s, want this till's key id %s", kid, want)
	}
}

func TestDeviceExtra_MainTillReportsApprovalKey(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	checkApprovalReport(t, enroll.Effective(dp.Cfg).Marketplace.StoreID, extra)
	// Same key on the next check-in.
	again := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	if again["approval_kid"] != extra["approval_kid"] {
		t.Fatal("the kid changed between check-ins")
	}
}

func TestDeviceExtra_AdditionalTillReportsApprovalKey(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	setReplica(t, dp)
	checkApprovalReport(t, enroll.Effective(dp.Cfg).Marketplace.StoreID, buildCloudHooks(dp, nil).DeviceExtra(t.Context()))
}

func TestDeviceExtra_UnusableApprovalKeyReportsNothingAndKeepsTheFile(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	p := paths.Data("secrets", "approval-ed25519.key")
	if err := os.MkdirAll(paths.Data("secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	if _, ok := extra["approval_key"]; ok {
		t.Fatal("approval_key reported from an unusable file")
	}
	if _, ok := extra["approval_kid"]; ok {
		t.Fatal("approval_kid reported from an unusable file")
	}
	if b, _ := os.ReadFile(p); string(b) != "short" {
		t.Fatal("file overwritten")
	}
}
