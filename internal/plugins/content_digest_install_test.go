package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0165 end to end (ut-docs#4546): both install paths — marketplace
// installBundleFile and manual Importer.Import — refuse a package whose
// manifest and signature are genuine but one of whose other files was
// changed, and install the untampered package. For a wasm plugin and for an
// asset-only language bundle.

type contentInstallCase struct {
	name     string
	manifest func() *Manifest
	files    []testPkgFile
	tamper   string // file whose bytes the tampered variant changes
}

func contentInstallCases() []contentInstallCase {
	return []contentInstallCase{
		{
			name: "wasm",
			manifest: func() *Manifest {
				return &Manifest{
					ID: "com.test.contentwasm", Name: "Content Wasm", Version: "1.0.0",
					Entrypoint: "./plugin.wasm", Runtime: "wasm",
					CanonicalType: "page", DeviceArch: "any",
				}
			},
			files:  []testPkgFile{{name: "plugin.wasm", data: []byte("\x00asm\x01\x00\x00\x00")}},
			tamper: "plugin.wasm",
		},
		{
			name: "language",
			manifest: func() *Manifest {
				return &Manifest{
					ID: "com.test.language-de", Name: "German", Version: "1.0.0",
					Runtime: "none", CanonicalType: "language", DeviceArch: "any",
				}
			},
			files: []testPkgFile{
				{name: "bundle.json", data: []byte(`{"generated_at":"2026-10-01T00:00:00Z"}`)},
				{name: "locales/de.json", data: []byte(`{"nav.sale":"Verkauf"}`)},
			},
			tamper: "locales/de.json",
		},
	}
}

// packageVariant signs c's package with priv and returns the .tar.gz,
// tampered (one file's bytes changed after signing) or not.
func packageVariant(t *testing.T, c contentInstallCase, priv ed25519.PrivateKey, tampered bool) ([]byte, *Manifest) {
	t.Helper()
	m := c.manifest()
	signTestManifest(t, m, priv, c.files)
	files := c.files
	if tampered {
		files = make([]testPkgFile, len(c.files))
		copy(files, c.files)
		for i := range files {
			if files[i].name == c.tamper {
				files[i].data = append(append([]byte{}, files[i].data...), ' ')
			}
		}
	}
	return tarGzPackage(t, m, files), m
}

func TestContentDigest_MarketplaceInstallBundleFile(t *testing.T) {
	for _, c := range contentInstallCases() {
		for _, tampered := range []bool{false, true} {
			name := c.name + "/untampered"
			if tampered {
				name = c.name + "/tampered"
			}
			t.Run(name, func(t *testing.T) {
				pub, priv, _ := ed25519.GenerateKey(rand.Reader)
				installer := installerForBundleTests(t, pub)
				artifact, m := packageVariant(t, c, priv, tampered)
				// The served checksum matches the served bytes: only the
				// signed content digest can catch the change.
				p := stageBundle(t, installer, "content-"+c.name, artifact)
				_, err := installer.installBundleFile(context.Background(), bundleInstallSpec{
					BundlePath: p, ListingID: "content-" + c.name,
					Checksum: checksumSHA256Hex(t, artifact), Signature: m.Signature,
					SourceURL: "https://x", TrustTier: "verified", DeviceArch: "any",
				})
				if tampered {
					if err == nil || !strings.Contains(err.Error(), "content digest mismatch") {
						t.Fatalf("tampered package: want content digest mismatch, got %v", err)
					}
					assertPluginNotInstalled(t, installer.db, m.ID)
					if _, statErr := os.Stat(filepath.Join(installer.pluginBaseDir, m.ID)); !os.IsNotExist(statErr) {
						t.Fatalf("tampered package left files under the plugin dir: %v", statErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("untampered package refused: %v", err)
				}
				for _, f := range c.files {
					if _, err := os.Stat(filepath.Join(installer.pluginBaseDir, m.ID, m.Version, filepath.FromSlash(f.name))); err != nil {
						t.Fatalf("installed package missing %s: %v", f.name, err)
					}
				}
			})
		}
	}
}

func TestContentDigest_ImporterImport(t *testing.T) {
	for _, c := range contentInstallCases() {
		for _, tampered := range []bool{false, true} {
			name := c.name + "/untampered"
			if tampered {
				name = c.name + "/tampered"
			}
			t.Run(name, func(t *testing.T) {
				pub, priv, _ := ed25519.GenerateKey(rand.Reader)
				mv, err := NewManifestVerifier(hex.EncodeToString(pub))
				if err != nil {
					t.Fatal(err)
				}
				db := openMarketplaceInstallerDB(t)
				t.Cleanup(func() { db.Close() })
				base := t.TempDir()
				imp := NewImporter(db, base, mv)

				artifact, m := packageVariant(t, c, priv, tampered)
				archive := filepath.Join(t.TempDir(), "pkg.tar.gz")
				if err := os.WriteFile(archive, artifact, 0o644); err != nil {
					t.Fatal(err)
				}
				_, err = imp.Import(context.Background(), &ImportRequest{FilePath: archive})
				if tampered {
					if err == nil || !strings.Contains(err.Error(), "content digest mismatch") {
						t.Fatalf("tampered package: want content digest mismatch, got %v", err)
					}
					assertPluginNotInstalled(t, db, m.ID)
					if _, statErr := os.Stat(filepath.Join(base, m.ID)); !os.IsNotExist(statErr) {
						t.Fatalf("tampered package left files under the plugin dir: %v", statErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("untampered package refused: %v", err)
				}
				for _, f := range c.files {
					if _, err := os.Stat(filepath.Join(base, m.ID, m.Version, filepath.FromSlash(f.name))); err != nil {
						t.Fatalf("imported package missing %s: %v", f.name, err)
					}
				}
			})
		}
	}
}
