package pages

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#5127 (design §2 H2): every /sync device report carries the till's
// approval key and its kid, so the cloud's quarantine rule sees it each time
// — from a main till and from an additional till alike (any till can move).
func checkApprovalReport(t *testing.T, storeID string, extra map[string]any) {
	t.Helper()
	rep, ok := extra["approval_key"].(map[string]string)
	if !ok {
		t.Fatalf("approval_key = %#v, want the {kid, public_key, pop} map", extra["approval_key"])
	}
	if extra["approval_kid"] != rep["kid"] || rep["kid"] == "" {
		t.Fatalf("approval_kid = %#v, kid = %q", extra["approval_kid"], rep["kid"])
	}
	pub, err := base64.RawURLEncoding.DecodeString(rep["public_key"])
	if err != nil || approvalkey.KID(pub) != rep["kid"] {
		t.Fatalf("public key / kid mismatch (%v)", err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(rep["pop"])
	if !ed25519.Verify(pub, approvalkey.PopMessage(enroll.CurrentStatus().DeviceID, storeID, rep["public_key"]), sig) {
		t.Fatal("proof of possession does not verify for this device and store")
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
