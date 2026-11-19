package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// A refused code install must leave an already-installed copy of the SAME
// id+version (the bundled plugins) byte-for-byte alone: the early gate runs
// before the live dir is removed, so removing the gate is visible here even
// though PersistManifest would still refuse later.
func TestAppStore_RefusedImportKeepsExistingLiveDir(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	m := []byte(`{"id":"com.test.impkeep","name":"I","version":"1.0.0","entrypoint":"./p.wasm","runtime":"wasm","canonical_type":"page","device_arch":"any"}`)
	imp, base := newTestImporter(t)
	live := filepath.Join(base, "com.test.impkeep", "1.0.0")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(live, "plugin.wasm")
	if err := os.WriteFile(sentinel, []byte("bundled-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "p.zip")
	writeZipArchive(t, archive, map[string][]byte{"manifest.json": m, "p.wasm": []byte("evil")})
	_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, TrustLevel: "untrusted", SkipSignature: true})
	if !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("err = %v, want ErrCodePluginRefused", err)
	}
	if b, rerr := os.ReadFile(sentinel); rerr != nil || string(b) != "bundled-bytes" {
		t.Fatalf("the live plugin dir was touched by a refused import: %q, %v", b, rerr)
	}
}

func TestAppStore_RefusedBundleInstallKeepsExistingLiveDir(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{ID: "com.test.bunkeep", Name: "Gate", Version: "1.0.0", Entrypoint: "./plugin-bin",
		Executable: "plugin-bin", Runtime: "wasm", CanonicalType: "page", DeviceArch: "linux/amd64"}
	artifact := signedMarketplaceArtifactWithManifest(t, priv, m)
	sum := checksumSHA256Hex(t, artifact)
	inst := gateInstaller(t, "http://127.0.0.1:1", pub)
	live := filepath.Join(inst.pluginBaseDir, m.ID, m.Version)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(live, "plugin.wasm")
	if err := os.WriteFile(sentinel, []byte("bundled-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := os.WriteFile(bundle, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := inst.installBundleFile(context.Background(), bundleInstallSpec{
		BundlePath: bundle, ListingID: "listing-1", Checksum: sum, Signature: m.Signature, DeviceArch: "linux/amd64",
	})
	if !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("err = %v, want ErrCodePluginRefused", err)
	}
	if b, rerr := os.ReadFile(sentinel); rerr != nil || string(b) != "bundled-bytes" {
		t.Fatalf("the live plugin dir was touched by a refused install: %q, %v", b, rerr)
	}
}

// A go/native row that reached the DB any other way (restore) must not be
// exec'd on the App Store channel, even on a GOOS that can run processes.
func TestAppStore_SupervisorRefusesProcessPlugins(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	prev := supervisorGOOS
	t.Cleanup(func() { supervisorGOOS = prev })
	supervisorGOOS = "darwin"
	s := NewSupervisor(nil)
	err := s.StartPlugin(context.Background(), "com.test.native", "/usr/bin/true", nil, RestartPolicy{})
	if !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("StartPlugin err = %v, want ErrCodePluginRefused", err)
	}
	if len(s.processes) != 0 {
		t.Fatal("a process was registered despite the refusal")
	}
}
