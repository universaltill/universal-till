package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0214 D3 (design B 4.3): the apply seam reads the INSTALLED manifest
// through the marketplace verifier at apply time, so a manifest edited on
// disk after install declares nothing.

func installVerifiedFixture(t *testing.T, id string, tamper bool) (*ManifestVerifier, string) {
	t.Helper()
	mv, priv := contentTestVerifier(t)
	m := contentTestManifest()
	m.ID = id
	m.Settings = []ManifestSetting{{Key: "merchant_code", Type: "text"}, {Key: "api_token", Type: "secret"}}
	src := writeSignedPackageDir(t, m, priv, contentTestFiles())
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	dst := filepath.Join(root, "plugins", id, "1.0.0")
	if err := os.MkdirAll(filepath.Join(dst, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"manifest.json", "plugin.wasm", "locales/de.json"} {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(src), filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if tamper && f == "manifest.json" {
			// A hand edit after install: one more declared setting.
			b = []byte(strings.Replace(string(b), `"merchant_code"`, `"merchant_code_x"`, 1))
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.FromSlash(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return mv, id
}

func TestVerifiedManifestSettings_SignedInstallDeclaresItsSettings(t *testing.T) {
	mv, id := installVerifiedFixture(t, "com.test.content", false)
	d := hostfnTestDB(t)
	seedPlugin(t, d, id)
	m, installed, err := VerifiedManifestSettings(context.Background(), d, mv, id)
	if err != nil || !installed {
		t.Fatalf("installed=%v err=%v, want a verified install", installed, err)
	}
	if m.SettingDecl("merchant_code") == nil || !m.SettingDeclaredSecret("api_token") {
		t.Fatalf("settings not declared: %+v", m.Settings)
	}
}

func TestVerifiedManifestSettings_ChangedManifestIsRefusedNotRetried(t *testing.T) {
	mv, id := installVerifiedFixture(t, "com.test.content", true)
	d := hostfnTestDB(t)
	seedPlugin(t, d, id)
	_, installed, err := VerifiedManifestSettings(context.Background(), d, mv, id)
	if installed || !errors.Is(err, ErrManifestUnverified) {
		t.Fatalf("installed=%v err=%v, want ErrManifestUnverified", installed, err)
	}
}

func TestVerifiedManifestSettings_NotInstalledAndNoVerifier(t *testing.T) {
	mv, _ := installVerifiedFixture(t, "com.test.content", false)
	d := hostfnTestDB(t)
	if _, installed, err := VerifiedManifestSettings(context.Background(), d, mv, "com.test.absent"); installed || err != nil {
		t.Fatalf("absent plugin: installed=%v err=%v, want false nil", installed, err)
	}
	seedPlugin(t, d, "com.test.content")
	if _, installed, err := VerifiedManifestSettings(context.Background(), d, nil, "com.test.content"); installed || !errors.Is(err, ErrManifestUnverified) {
		t.Fatalf("nil verifier: installed=%v err=%v, want ErrManifestUnverified (never an unverified read)", installed, err)
	}
}
