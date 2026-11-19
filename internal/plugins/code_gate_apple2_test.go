package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
)

// L1 (fable early review): rollback moves a row to another version without
// PersistManifest, so on the App Store channel it refuses a code target
// (the live row and files stay as they were) and still rolls an asset-only
// plugin back.
func TestAppStore_RollbackRefusesCodeTargetKeepsRow(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	const id = "com.test.rbcode"
	seedInstalledPlugin(t, db, id, "RB", "2.0.0", "none", true)
	writeVersionDir(t, base, id, "2.0.0", true)
	dir := writeVersionDir(t, base, id, "1.0.0", true)
	manifest := `{"id":"` + id + `","name":"RB","version":"1.0.0","entrypoint":"./run","runtime":"wasm","canonical_type":"page","device_arch":"any"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rm.Rollback(ctx, id, "1.0.0", "tester"); !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("Rollback to a wasm version on appstore = %v, want ErrCodePluginRefused", err)
	}
	if v, _, _ := data.NewPluginRepo(db).GetInstalledPluginVersion(ctx, id); v != "2.0.0" {
		t.Fatalf("row moved to %q by a refused rollback", v)
	}
	if _, err := os.Stat(filepath.Join(base, id, "1.0.0")); !os.IsNotExist(err) {
		t.Fatalf("a refused rollback restored the live dir (stat err %v)", err)
	}
}

// L2: a listing the catalogue mislabelled (type theme, manifest wasm) is
// staged on disk by DownloadToStore; the install refusal removes the staged
// bundle and its metadata instead of leaving wasm lying around.
func TestAppStore_InstallFromStoreRefusalDeletesStagedBundle(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := &Manifest{ID: "com.test.mislabel", Name: "M", Version: "1.0.0", Entrypoint: "./plugin-bin",
		Executable: "plugin-bin", Runtime: "wasm", CanonicalType: "page", DeviceArch: "linux/amd64"}
	artifact := signedMarketplaceArtifactWithManifest(t, priv, m)
	codeGateChannel(t, buildinfo.ChannelAppStore)
	inst := gateInstaller(t, "http://127.0.0.1:1", pub)
	bundle, meta := inst.storeDownloadPaths("listing-m")
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(StoreDownload{ListingID: "listing-m", Version: "1.0.0", Checksum: checksumSHA256Hex(t, artifact),
		Signature: m.Signature, BundlePath: bundle, DownloadedAt: time.Now().UTC()})
	if err := os.WriteFile(meta, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.InstallFromStore(context.Background(), "listing-m", "official"); !errors.Is(err, ErrCodePluginRefused) {
		t.Fatalf("InstallFromStore = %v, want ErrCodePluginRefused", err)
	}
	for _, p := range []string{bundle, meta} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s stays on disk after the refusal (stat err %v)", p, err)
		}
	}
}
