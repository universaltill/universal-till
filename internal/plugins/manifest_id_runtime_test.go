package plugins

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#2891: the plugin id and version are joined raw into filesystem
// paths (data/plugins/<id>/<version>/) by the marketplace installer and the
// manual importer, so a manifest id like "../../pos" must never get that
// far. Every real id (com.universaltill.*, the com.test.* fixtures) is a
// lower-case reverse-DNS name.
func TestValidatePluginID(t *testing.T) {
	good := []string{
		"com.universaltill.tax-de",
		"com.universaltill.ut-faq",
		"com.test.hostfn",
		"test",
		"a",
		"x_y.z-1",
	}
	for _, id := range good {
		if err := validatePluginID(id); err != nil {
			t.Errorf("validatePluginID(%q) = %v, want nil", id, err)
		}
	}
	bad := []string{
		"", "../../pos", "..", ".", ".hidden", "-x", "_x",
		"com/evil", `com\evil`, "Com.Upper", "a b", "a:b",
		"a..b/../c", strings.Repeat("a", 129),
		// Windows strips a trailing dot, so "com.foo." would alias
		// "com.foo"'s directory; reserved device names aren't directories.
		"com.foo.", "nul", "con", "com1", "lpt9", "aux.txt",
	}
	for _, id := range bad {
		if err := validatePluginID(id); err == nil {
			t.Errorf("validatePluginID(%q) = nil, want an error", id)
		}
	}
}

func TestValidatePluginVersion(t *testing.T) {
	for _, v := range []string{"1.0.0", "0.7.0", "1.1.128", "1.0.0-rc.1", "1.0.0+build.5"} {
		if err := validatePluginVersion(v); err != nil {
			t.Errorf("validatePluginVersion(%q) = %v, want nil", v, err)
		}
	}
	for _, v := range []string{"", "..", ".", "../1.0.0", "1.0/../..", `1\0`, "1..0", strings.Repeat("1", 65), "1.0.", "NUL"} {
		if err := validatePluginVersion(v); err == nil {
			t.Errorf("validatePluginVersion(%q) = nil, want an error", v)
		}
	}
}

func TestParseManifest_RefusesTraversalID(t *testing.T) {
	_, err := ParseManifest(strings.NewReader(`{"id":"../../pos","name":"x","version":"1.0.0","runtime":"wasm","entrypoint":"./p.wasm"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid plugin id") {
		t.Fatalf("ParseManifest(id=../../pos) err = %v, want an invalid plugin id error", err)
	}
	_, err = ParseManifest(strings.NewReader(`{"id":"com.test.ok","name":"x","version":"../../1","runtime":"wasm","entrypoint":"./p.wasm"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid plugin version") {
		t.Fatalf("ParseManifest(version=../../1) err = %v, want an invalid plugin version error", err)
	}
}

func writeVerifierManifest(t *testing.T, m map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(m)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The marketplace installer reads manifests through VerifyManifest, not
// ParseManifest, so the id check and the runtime default must hold there too.
func TestVerifyManifest_RefusesTraversalID(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	path := writeVerifierManifest(t, map[string]any{
		"id": "../../pos", "name": "x", "version": "1.0.0",
		"canonical_type": "page", "device_arch": "any", "runtime": "none",
	})
	if _, err := mv.VerifyManifest(path); err == nil || !strings.Contains(err.Error(), "invalid plugin id") {
		t.Fatalf("VerifyManifest(id=../../pos) err = %v, want an invalid plugin id error", err)
	}
}

func TestVerifyManifest_MissingRuntimeNeverProcess(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	path := writeVerifierManifest(t, map[string]any{
		"id": "com.test.noruntime", "name": "x", "version": "1.0.0",
		"canonical_type": "page", "device_arch": "any", "entrypoint": "./plugin.wasm",
	})
	res, err := mv.VerifyManifest(path)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if res.Manifest.Runtime != "wasm" {
		t.Fatalf("runtime after verify = %q, want \"wasm\" (never the process runtime)", res.Manifest.Runtime)
	}
}

// Defence in depth: the importer re-checks id/version right before joining
// them into its install path, whatever manifestParser returned.
func TestImport_RefusesTraversalIDBeforePathJoin(t *testing.T) {
	db := openMarketplaceInstallerDB(t)
	t.Cleanup(func() { db.Close() })
	// Nest the base two levels deep so a traversal that slips through lands
	// inside this test's temp dir, never in the shared $TMPDIR.
	root := t.TempDir()
	base := filepath.Join(root, "a", "b")
	imp := NewImporter(db, base, nil)
	imp.manifestParser = func(io.Reader) (*Manifest, error) {
		return &Manifest{ID: "../../pos", Name: "x", Version: "1.0.0", Runtime: "none"}, nil
	}
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	writeZipArchive(t, archive, map[string][]byte{
		"manifest.json": validImportManifest("com.test.ignored"),
		"assets/a.txt":  []byte("hello"),
	})
	_, err := imp.Import(context.Background(), &ImportRequest{
		FilePath: archive, TrustLevel: "untrusted", Uploader: "tester", SkipSignature: true,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid plugin id") {
		t.Fatalf("Import(id=../../pos) err = %v, want an invalid plugin id error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "pos")); statErr == nil {
		t.Fatal("importer wrote outside its plugin base dir")
	}
}
