package plugins

// TDD arcs for coverage batch 5 (internal/plugins). Each test here was proven
// RED against the pre-fix code before the corresponding fix landed:
//
//  1. TestVerifyManifestRejectsUnsignedWhenKeyConfigured — with a marketplace
//     public key configured, a manifest carrying NO signature field passed
//     VerifyManifest (SignatureVerified=false but zero errors → nil error).
//     The manual-import path (Importer.Import) checks only the error, so an
//     unsigned plugin imported cleanly despite CLAUDE.md's "never run an
//     unverified plugin".
//  2. TestVerifyCompatibilityComparesVersionsNumerically — MinPOSVersion was
//     compared lexicographically ("0.2.5" > "0.2.49"), so a plugin requiring
//     0.2.5 was REJECTED on a 0.2.49 till, and one requiring 0.10.0 would be
//     ACCEPTED on 0.9.0.
//  3. TestDownloadChecksumMismatchIsNotRetried — isRetryable compared the
//     whole error string to the literal "checksum mismatch", which never
//     matches the real "checksum mismatch: expected …, got …" message, so a
//     tampered/corrupt artifact was re-downloaded maxRetries more times
//     (resuming from the already-complete corrupt .part file) before failing.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"
)

func TestVerifyManifestRejectsUnsignedWhenKeyConfigured(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	mv, err := NewManifestVerifier(hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}

	manifest := map[string]any{
		"id":             "com.test.unsigned",
		"name":           "Unsigned",
		"version":        "1.0.0",
		"canonical_type": "page",
		"device_arch":    "any",
		"runtime":        "none",
		// deliberately NO signature field
	}
	raw, _ := json.Marshal(manifest)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	result, err := mv.VerifyManifest(path)
	if err == nil {
		t.Fatalf("unsigned manifest passed verification with a public key configured (SignatureVerified=%v)", result.SignatureVerified)
	}
	if result != nil && result.SignatureVerified {
		t.Fatalf("SignatureVerified=true for an unsigned manifest")
	}
}

func TestVerifyManifestStillAllowsUnsignedInDevMode(t *testing.T) {
	// No public key configured (dev mode) — unsigned manifests must keep
	// working; the fix must only bite when a key is present.
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	manifest := map[string]any{
		"id":             "com.test.dev",
		"name":           "Dev",
		"version":        "1.0.0",
		"canonical_type": "page",
		"device_arch":    "any",
		"runtime":        "none",
	}
	raw, _ := json.Marshal(manifest)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := mv.VerifyManifest(path); err != nil {
		t.Fatalf("dev-mode unsigned manifest rejected: %v", err)
	}
}

func TestVerifyCompatibilityComparesVersionsNumerically(t *testing.T) {
	mv := &ManifestVerifier{}

	// Numerically 0.2.49 >= 0.2.5, lexicographically "0.2.49" < "0.2.5".
	m := &Manifest{DeviceArch: "any", MinPOSVersion: "0.2.5"}
	if err := mv.VerifyCompatibility(m, "linux/amd64", "0.2.49"); err != nil {
		t.Fatalf("plugin requiring >=0.2.5 rejected on POS 0.2.49: %v", err)
	}

	// Numerically 0.9.0 < 0.10.0, lexicographically "0.10.0" < "0.9.0".
	m = &Manifest{DeviceArch: "any", MinPOSVersion: "0.10.0"}
	if err := mv.VerifyCompatibility(m, "linux/amd64", "0.9.0"); err == nil {
		t.Fatalf("plugin requiring >=0.10.0 accepted on POS 0.9.0")
	}

	// Boundary: equal versions are compatible.
	m = &Manifest{DeviceArch: "any", MinPOSVersion: "1.2.3"}
	if err := mv.VerifyCompatibility(m, "linux/amd64", "1.2.3"); err != nil {
		t.Fatalf("equal versions rejected: %v", err)
	}

	// Arch gate still enforced.
	m = &Manifest{DeviceArch: "linux/arm64"}
	if err := mv.VerifyCompatibility(m, "linux/amd64", "1.0.0"); err == nil {
		t.Fatalf("wrong-arch plugin accepted")
	}
	m = &Manifest{DeviceArch: "any"}
	if err := mv.VerifyCompatibility(m, "linux/amd64", "1.0.0"); err != nil {
		t.Fatalf("arch=any rejected: %v", err)
	}
}

func TestDownloadChecksumMismatchIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("not the expected content"))
	}))
	defer srv.Close()

	dm := NewDownloadManager(t.TempDir())
	dm.retryBackoff = time.Millisecond // keep the pre-fix red fast

	_, err := dm.Download(context.Background(), &DownloadRequest{
		URL:              srv.URL + "/bundle.tar.gz",
		PluginID:         "com.test.mismatch",
		ExpectedChecksum: strings.Repeat("0", 64), // cannot match anything
	})
	if err == nil {
		t.Fatalf("download with wrong checksum succeeded")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("checksum mismatch was retried: server hit %d times, want 1", got)
	}
}
