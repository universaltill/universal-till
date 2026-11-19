package bundled

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/secrets"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	paths.Init(dir)
	prev := secrets.Default()
	secrets.SetDefault(secrets.NewKeyStoreAt(filepath.Join(dir, "secrets", "plugin_settings_key.bin"), nil))
	t.Cleanup(func() { secrets.SetDefault(prev); paths.Init("") })
	d, err := db.Open(filepath.Join(dir, "unitill-pos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// fixture builds a fake embedded tree and the lock pinning it.
func fixture(t *testing.T, version string) (fstest.MapFS, *Lock) {
	t.Helper()
	manifest := `{"id":"com.universaltill.payment-fake","name":"Fake Pay","version":"` + version + `","description":"d",
"author":"Universal Till","canonical_type":"payment","runtime":"wasm","entrypoint":"./bin/plugin.wasm","device_arch":"any",
"min_pos_version":"1.0.0","permissions":["pos.tender","events:receive","storage"],
"entries":[{"type":"payment","key":"fakepay","label":"Card (Fake)","sort_order":4,"trigger_event":"payment.fakepay.requested"}],
"settings":[{"key":"api_key","default_value":"","scope":"global","type":"secret"}]}`
	wasm := []byte("\x00asm-fake-" + version)
	loc := []byte(`{"a":"b"}`)
	fsys := fstest.MapFS{
		"com.universaltill.payment-fake/manifest.json":   {Data: []byte(manifest)},
		"com.universaltill.payment-fake/bin/plugin.wasm": {Data: wasm},
		"com.universaltill.payment-fake/locales/en.json": {Data: loc},
	}
	lock := &Lock{Schema: 1, Plugins: []Entry{{
		ID: "com.universaltill.payment-fake", Version: version, Repo: "universaltill/ut-plugin-payment-fake",
		ReleaseTag: "v" + version, BundleName: "x.tar.gz", BundleSHA256: strings.Repeat("a", 64),
		ManifestSHA256: sum([]byte(manifest)), WasmPath: "bin/plugin.wasm", WasmSHA256: sum(wasm),
		Files: map[string]string{"locales/en.json": sum(loc)},
	}}}
	return fsys, lock
}

const fakeID = "com.universaltill.payment-fake"

func TestSync_FirstInstallIsDisabledAndWritesVerifiedFilesOnAppStore(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := openTestDB(t)
	fsys, lock := fixture(t, "1.0.0")
	changed, err := syncFrom(context.Background(), d.DB, fsys, lock)
	if err != nil || !changed {
		t.Fatalf("syncFrom = %v, %v", changed, err)
	}
	repo := data.NewPluginRepo(d.DB)
	if v, ok, _ := repo.GetInstalledPluginVersion(context.Background(), fakeID); !ok || v != "1.0.0" {
		t.Fatalf("installed = %q %v", v, ok)
	}
	if on, _ := repo.PluginActive(context.Background(), fakeID); on {
		t.Fatal("a first install must be DISABLED until the shop switches it on")
	}
	if _, err := os.Stat(paths.Plugins(fakeID, "1.0.0", "bin", "plugin.wasm")); err != nil {
		t.Fatalf("wasm not on disk: %v", err)
	}
	// Idempotent: a second boot changes nothing.
	if changed, err := syncFrom(context.Background(), d.DB, fsys, lock); err != nil || changed {
		t.Fatalf("second sync = %v, %v; want no-op", changed, err)
	}
}

func TestSync_TamperedBytesRefusedNeverPersisted(t *testing.T) {
	d := openTestDB(t)
	fsys, lock := fixture(t, "1.0.0")
	fsys[fakeID+"/bin/plugin.wasm"] = &fstest.MapFile{Data: []byte("evil")}
	changed, err := syncFrom(context.Background(), d.DB, fsys, lock)
	if err == nil || changed {
		t.Fatalf("syncFrom = %v, %v; want refusal", changed, err)
	}
	if _, ok, _ := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(context.Background(), fakeID); ok {
		t.Fatal("a plugin that failed the lock check was persisted")
	}
	if _, err := os.Stat(paths.Plugins(fakeID)); err == nil {
		t.Fatal("files of a refused plugin were written")
	}
	// A tampered manifest, and a tampered locale, likewise.
	fsys, lock = fixture(t, "1.0.0")
	fsys[fakeID+"/manifest.json"] = &fstest.MapFile{Data: []byte(`{}`)}
	if _, err := syncFrom(context.Background(), d.DB, fsys, lock); err == nil {
		t.Fatal("tampered manifest accepted")
	}
	fsys, lock = fixture(t, "1.0.0")
	fsys[fakeID+"/locales/en.json"] = &fstest.MapFile{Data: []byte(`{"x":"y"}`)}
	if _, err := syncFrom(context.Background(), d.DB, fsys, lock); err == nil {
		t.Fatal("tampered locale accepted")
	}
}

func TestSync_UpgradeKeepsEnabledStateAndSettings(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.DB)
	if err := repo.SetPluginActive(ctx, nil, fakeID, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertPluginSetting(ctx, fakeID, "api_key", `"k-123"`); err != nil {
		t.Fatal(err)
	}
	fsys2, lock2 := fixture(t, "1.1.0")
	changed, err := syncFrom(ctx, d.DB, fsys2, lock2)
	if err != nil || !changed {
		t.Fatalf("upgrade = %v, %v", changed, err)
	}
	if v, _, _ := repo.GetInstalledPluginVersion(ctx, fakeID); v != "1.1.0" {
		t.Fatalf("version = %q, want 1.1.0", v)
	}
	if on, _ := repo.PluginActive(ctx, fakeID); !on {
		t.Fatal("an upgrade switched the shop's enabled plugin off")
	}
	rows, err := repo.ListPluginSettings(ctx, fakeID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("settings after upgrade = %v, %v; want the shop's key kept", rows, err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = ? AND key = 'api_key' AND value_json != '""'`, fakeID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the shop's api_key value was reset by the upgrade (%d non-empty rows, %v)", n, err)
	}
	if _, err := os.Stat(paths.Plugins(fakeID, "1.0.0")); err == nil {
		t.Fatal("the old version's files were left behind")
	}
}

func TestSync_UpgradeKeepsDisabledStateAndEmbeddedVersionWins(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.1.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	// Older app build: the loader only runs the locked bytes, so the embedded
	// 1.0.0 replaces the newer row (version skew; see bundled_apple2_test.go).
	fsysOld, lockOld := fixture(t, "1.0.0")
	if changed, err := syncFrom(ctx, d.DB, fsysOld, lockOld); err != nil || !changed {
		t.Fatalf("downgrade sync = %v, %v; want the embedded version installed", changed, err)
	}
	fsysNew, lockNew := fixture(t, "1.2.0")
	if _, err := syncFrom(ctx, d.DB, fsysNew, lockNew); err != nil {
		t.Fatal(err)
	}
	if on, _ := data.NewPluginRepo(d.DB).PluginActive(ctx, fakeID); on {
		t.Fatal("a disabled bundled plugin was switched on by an upgrade")
	}
}

func TestSync_RewritesAlteredFilesOfSameVersion(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	wasm := paths.Plugins(fakeID, "1.0.0", "bin", "plugin.wasm")
	if err := os.WriteFile(wasm, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := syncFrom(ctx, d.DB, fsys, lock); err != nil || !changed {
		t.Fatalf("sync = %v, %v; want the altered file rewritten", changed, err)
	}
	if raw, _ := os.ReadFile(wasm); string(raw) != "\x00asm-fake-1.0.0" {
		t.Fatalf("wasm after resync = %q", raw)
	}
}

func TestVerifyAgainst_OnlyTheLockedBytesAtTheLockedPath(t *testing.T) {
	_ = openTestDB(t)
	fsys, lock := fixture(t, "1.0.0")
	_ = fsys
	good := filepath.Join(paths.Plugins(fakeID, "1.0.0"), "bin", "plugin.wasm")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("\x00asm-fake-1.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := []byte("\x00asm-fake-1.0.0")
	if !verifyAgainst(lock, fakeID, "1.0.0", good, locked) {
		t.Fatal("the locked bytes were not accepted")
	}
	if verifyAgainst(lock, fakeID, "1.0.1", good, locked) || verifyAgainst(lock, "com.universaltill.other", "1.0.0", good, locked) {
		t.Fatal("an unlisted id/version was accepted")
	}
	if verifyAgainst(lock, fakeID, "1.0.0", filepath.Join(filepath.Dir(good), "other.wasm"), locked) {
		t.Fatal("the locked bytes were accepted at a path that is not the locked wasm path")
	}
	if verifyAgainst(lock, fakeID, "1.0.0", good, nil) {
		t.Fatal("no bytes were accepted")
	}
	if err := os.WriteFile(good, []byte("changed!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if verifyAgainst(lock, fakeID, "1.0.0", good, []byte("changed!")) {
		t.Fatal("changed bytes were accepted")
	}
}

func TestDefaultBuildEmbedsNothing(t *testing.T) {
	if Available() {
		t.Skip("built with -tags bundledplugins")
	}
	d := openTestDB(t)
	if changed, err := Sync(context.Background(), d.DB); err != nil || changed {
		t.Fatalf("Sync with nothing embedded = %v, %v", changed, err)
	}
	if Verify("com.universaltill.payment-stripe", "1.5.0", "/x", []byte("x")) {
		t.Fatal("Verify must be false when nothing is embedded")
	}
}

// The committed lock: exactly the plugins we own, well-formed.
func TestCommittedLock_IsExactlyStripeSumUpAndUKTax(t *testing.T) {
	l, err := compiledLock()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range l.Plugins {
		got[e.ID] = e.Version
	}
	want := map[string]string{"com.universaltill.payment-stripe": "1.5.0", "com.universaltill.payment-sumup": "1.5.1", "com.universaltill.tax-uk": "1.3.1"}
	if len(got) != len(want) {
		t.Fatalf("bundled set = %v, want %v", got, want)
	}
	for id, v := range want {
		if got[id] != v {
			t.Fatalf("%s = %q, want %q", id, got[id], v)
		}
	}
	if !IsBundled("com.universaltill.payment-stripe") || IsBundled("com.example.third-party") {
		t.Fatal("IsBundled is wrong")
	}
}

func TestParseLock_RejectsMalformed(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{"schema": 1, "plugins": []any{map[string]any{
			"id": "com.universaltill.payment-x", "version": "1.0.0", "repo": "universaltill/ut-plugin-payment-x",
			"release_tag": "v1.0.0", "bundle_name": "x.tar.gz", "bundle_sha256": strings.Repeat("a", 64),
			"manifest_sha256": strings.Repeat("b", 64), "wasm_path": "bin/plugin.wasm", "wasm_sha256": strings.Repeat("c", 64),
		}}}
	}
	enc := func(m map[string]any) []byte { b, _ := json.Marshal(m); return b }
	if _, err := ParseLock(enc(good())); err != nil {
		t.Fatalf("good lock rejected: %v", err)
	}
	mut := func(f func(e map[string]any)) []byte {
		m := good()
		f(m["plugins"].([]any)[0].(map[string]any))
		return enc(m)
	}
	for name, raw := range map[string][]byte{
		"not json":          []byte("{"),
		"third-party id":    mut(func(e map[string]any) { e["id"] = "com.example.pay" }),
		"short sha":         mut(func(e map[string]any) { e["wasm_sha256"] = "abc" }),
		"upper-case sha":    mut(func(e map[string]any) { e["bundle_sha256"] = strings.Repeat("A", 64) }),
		"tag mismatch":      mut(func(e map[string]any) { e["release_tag"] = "v2.0.0" }),
		"foreign repo":      mut(func(e map[string]any) { e["repo"] = "evil/ut-plugin-payment-x" }),
		"path traversal":    mut(func(e map[string]any) { e["wasm_path"] = "../x.wasm" }),
		"bad version":       mut(func(e map[string]any) { e["version"] = "latest" }),
		"bundle name slash": mut(func(e map[string]any) { e["bundle_name"] = "a/b.tar.gz" }),
		"locale traversal":  mut(func(e map[string]any) { e["files"] = map[string]any{"../x": strings.Repeat("d", 64)} }),
	} {
		if _, err := ParseLock(raw); err == nil {
			t.Errorf("%s: malformed lock accepted", name)
		}
	}
}

// The bundled package never imports net/http or the marketplace client:
// bundled code arrives by build, not by network.
func TestBundledPackageImportsNoNetwork(t *testing.T) {
	entries, _ := filepath.Glob("*.go")
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			p := strings.Trim(im.Path.Value, `"`)
			if p == "net/http" || strings.Contains(p, "plugins/marketplace") || p == "net" {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}

// A disabled bundled plugin does nothing: no tender entry, and the wasm
// runtime loads no module for it (nothing runs, nothing reaches the network).
// Switching it on makes the tender appear.
func TestDisabledBundledPluginHasNoTenderAndLoadsNothing(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.DB)
	tender := func() int {
		if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods WHERE id = 'fakepay' AND is_active = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := tender(); n != 0 {
		t.Fatalf("a disabled bundled plugin offers %d tender(s), want 0", n)
	}
	rt := plugins.NewWasmRuntime(paths.Plugins())
	t.Cleanup(func() { rt.Close(ctx) })
	plugins.SetBundledCodeVerifier(func(id, version, path string, wasm []byte) bool { return verifyAgainst(lock, id, version, path, wasm) })
	t.Cleanup(func() { plugins.SetBundledCodeVerifier(nil) })
	rt.Sync(ctx, d.DB)
	if _, err := rt.HandleEvent(ctx, fakeID, plugins.Event{Type: "payment.fakepay.authorize"}); err == nil || !strings.Contains(err.Error(), "module not loaded") {
		t.Fatalf("a disabled bundled plugin handled an event: %v", err)
	}
	if err := repo.SetPluginActive(ctx, nil, fakeID, true); err != nil {
		t.Fatal(err)
	}
	if n := tender(); n != 1 {
		t.Fatalf("an enabled bundled plugin offers %d tender(s), want 1", n)
	}
}
