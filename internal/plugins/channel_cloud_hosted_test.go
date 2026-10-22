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

	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0169 section 3 / ut-docs#4759: a till lists a plugin whose hosts is
// ["cloud"] but never installs it, never loads it and never starts it. The
// refusal takes no input about the till's role: a main till, an additional
// till and a replica all hit the same gate (the plugin-sync follower reaches
// it through the same installer, see internal/pages).

func channelManifestStruct(t *testing.T) *Manifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/channel_demo_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func TestPersistManifest_RefusesCloudHostedPlugin(t *testing.T) {
	d := openRealDB(t)
	m := channelManifestStruct(t)
	err := PersistManifest(context.Background(), d.DB, m, InstallOptions{})
	if !errors.Is(err, ErrCloudHostedPlugin) {
		t.Fatalf("PersistManifest must refuse a cloud-hosted plugin, got %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = ?`, m.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no plugins row may be written (n=%d, err=%v)", n, err)
	}
}

func TestInstallBundleFile_RefusesCloudHostedPlugin(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	installer := installerForBundleTests(t, publicKey)
	m := channelManifestStruct(t)
	m.DeviceArch = "any"
	// The package carries its entrypoint, so the only thing that can stop
	// the install is the hosts refusal, and it must stop it before anything
	// is written to the plugin directory.
	m.Entrypoint = "./plugin-bin"
	artifact := signedMarketplaceArtifactWithManifest(t, privateKey, m)
	bundlePath := stageBundle(t, installer, "chan", artifact)

	_, err := installer.installBundleFile(context.Background(), bundleInstallSpec{
		BundlePath: bundlePath, ListingID: "chan",
		Checksum: checksumSHA256Hex(t, artifact), Signature: m.Signature,
		SourceURL: "https://x", TrustTier: "verified", DeviceArch: "any",
	})
	if !errors.Is(err, ErrCloudHostedPlugin) {
		t.Fatalf("a signed, valid channel bundle must still be refused on a till: %v", err)
	}
	assertPluginNotInstalled(t, installer.db, m.ID)
	if _, statErr := os.Stat(filepath.Join(paths.Plugins(), m.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("a refused cloud-hosted plugin left files on the till: %v", statErr)
	}
}

func TestImporter_RefusesCloudHostedPlugin(t *testing.T) {
	imp, base := newTestImporter(t)
	m := channelManifestStruct(t)
	raw, _ := json.Marshal(m)
	archive := filepath.Join(t.TempDir(), "channel.zip")
	writeZipArchive(t, archive, map[string][]byte{"manifest.json": raw, "plugin.wasm": []byte("\x00asm")})
	_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if !errors.Is(err, ErrCloudHostedPlugin) {
		t.Fatalf("a file import must refuse a cloud-hosted plugin: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, m.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("files left behind: %v", statErr)
	}
	// The refusal is the importer's own, before PersistManifest could be the backstop.
	var n int
	if err := imp.db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = ?`, m.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plugins rows %d %v", n, err)
	}
}

func TestClassifyInstallError_CloudHostedIsPermanent(t *testing.T) {
	f := ClassifyInstallError(errors.Join(errors.New("install"), ErrCloudHostedPlugin))
	if f.Retryable {
		t.Fatal("a cloud-hosted plugin can never install on a till: not retryable")
	}
	if f.MessageKey != "plugins.install.error.incompatible" {
		t.Fatalf("message key %q: reuse the existing 'not compatible with this till' key", f.MessageKey)
	}
}

// WasmRuntime.Sync must not load a cloud-hosted plugin even if its row is
// active (a row from before this change, a restored backup, a hand-copied
// directory), while a normal plugin beside it still loads.
func TestWasmRuntimeSync_SkipsCloudHostedPlugin(t *testing.T) {
	ctx := context.Background()
	raw := evGuestBytes(t)
	const normal = "com.test.sync-normal"
	cloud := channelManifestStruct(t)

	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	for _, id := range []string{normal, cloud.ID} {
		if err := writeFileWithParents(paths.Plugins(id, "1.0.0", "plugin.wasm"), raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFileWithParents(filepath.Join(paths.Plugins(normal, "1.0.0"), "manifest.json"),
		[]byte(`{"id":"`+normal+`","name":"n","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm"}`)); err != nil {
		t.Fatal(err)
	}
	cm, _ := json.Marshal(cloud)
	if err := writeFileWithParents(filepath.Join(paths.Plugins(cloud.ID, "1.0.0"), "manifest.json"), cm); err != nil {
		t.Fatal(err)
	}

	d := streamDB(t, normal, "events:receive")
	seedPlugin(t, d, cloud.ID)

	w := NewWasmRuntime(paths.Plugins())
	t.Cleanup(func() { w.Close(ctx) })
	w.Sync(ctx, d)

	w.mu.Lock()
	_, normalLoaded := w.modules[normal]
	_, cloudLoaded := w.modules[cloud.ID]
	w.mu.Unlock()
	if !normalLoaded {
		t.Fatal("a normal wasm plugin must still load")
	}
	if cloudLoaded {
		t.Fatal("a cloud-hosted plugin must never be loaded on a till (ADR-0169)")
	}
	var state string
	if err := d.QueryRow(`SELECT install_state FROM plugins WHERE id = ?`, cloud.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "broken" {
		t.Fatal("a skipped cloud-hosted plugin is not 'broken': it is listed, not run")
	}
}
