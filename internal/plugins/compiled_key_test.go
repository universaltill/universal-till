package plugins

// ADR-0158 (ut-docs#4292 part a): the marketplace key compiled into the
// binary is the only key a release till trusts, a verifier with no key
// refuses every manifest, and unsigned plugins run only in a `-tags
// devplugins` build.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// structuralOnlyVerifier is a verifier that checks manifest structure only —
// what NewManifestVerifier("") used to mean everywhere. In-package test
// helper; production code has no such bypass (ADR-0158).
func structuralOnlyVerifier() *ManifestVerifier {
	return &ManifestVerifier{allowUnsigned: true}
}

// setCompiledKey overrides the -ldflags key for one test.
func setCompiledKey(t *testing.T, hexKey string) {
	t.Helper()
	orig := buildinfo.MarketplacePublicKey
	buildinfo.MarketplacePublicKey = hexKey
	t.Cleanup(func() { buildinfo.MarketplacePublicKey = orig })
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func compiledKeyManifest(id string) *Manifest {
	return &Manifest{
		ID:            id,
		Name:          "Compiled Key",
		Version:       "1.0.0",
		Runtime:       "none",
		CanonicalType: "page",
		DeviceArch:    "any",
	}
}

// writeManifestFile writes m (signed with priv when non-nil, ADR-0165 way,
// for a package holding only manifest.json) to a temp file.
func writeManifestFile(t *testing.T, m *Manifest, priv ed25519.PrivateKey) string {
	t.Helper()
	if priv != nil {
		signTestManifest(t, m, priv, nil)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestNewManifestVerifier_CompiledKeyWinsOverArgument(t *testing.T) {
	compiledPub, compiledPriv := newKey(t)
	otherPub, otherPriv := newKey(t)
	setCompiledKey(t, hex.EncodeToString(compiledPub))

	mv, err := NewManifestVerifier(hex.EncodeToString(otherPub))
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	if _, err := mv.VerifyManifest(writeManifestFile(t, compiledKeyManifest("com.test.compiled"), compiledPriv)); err != nil {
		t.Fatalf("manifest signed with the compiled key must verify: %v", err)
	}
	if _, err := mv.VerifyManifest(writeManifestFile(t, compiledKeyManifest("com.test.other"), otherPriv)); err == nil {
		t.Fatalf("manifest signed with the caller's (env/settings) key must be refused when a key is compiled in")
	}
}

func TestNewManifestVerifier_CompiledKeyUsedWhenArgumentEmpty(t *testing.T) {
	pub, _ := newKey(t)
	setCompiledKey(t, hex.EncodeToString(pub))
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	if !mv.HasPublicKey() {
		t.Fatalf("HasPublicKey must be true when a key is compiled in, whatever the caller passed")
	}
}

func TestNewManifestVerifier_InvalidCompiledKeyErrors(t *testing.T) {
	setCompiledKey(t, "abcd")
	pub, _ := newKey(t)
	if _, err := NewManifestVerifier(hex.EncodeToString(pub)); err == nil {
		t.Fatalf("an invalid compiled key must be an error, not fall back to the caller's key")
	}
}

func TestVerifyManifest_NoKeyRefusesEveryManifest(t *testing.T) {
	if UnsignedPluginsAllowed() {
		t.Skip("devplugins build: no key means structural checks only")
	}
	setCompiledKey(t, "")
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	_, priv := newKey(t)
	for name, path := range map[string]string{
		"unsigned": writeManifestFile(t, compiledKeyManifest("com.test.nokey"), nil),
		"signed":   writeManifestFile(t, compiledKeyManifest("com.test.nokeysigned"), priv),
	} {
		res, err := mv.VerifyManifest(path)
		if !errors.Is(err, ErrNoMarketplaceKey) {
			t.Fatalf("%s: want ErrNoMarketplaceKey with no key, got %v", name, err)
		}
		if res == nil || res.SignatureVerified {
			t.Fatalf("%s: SignatureVerified must be false with no key: %+v", name, res)
		}
	}
	// Structural errors are still reported alongside.
	bad := compiledKeyManifest("")
	_, err = mv.VerifyManifest(writeManifestFile(t, bad, nil))
	if !errors.Is(err, ErrNoMarketplaceKey) || !strings.Contains(err.Error(), "id") {
		t.Fatalf("want ErrNoMarketplaceKey plus the structural reason, got %v", err)
	}
}

func TestVerifyManifest_NoKeyDevpluginsIsStructuralOnly(t *testing.T) {
	if !UnsignedPluginsAllowed() {
		t.Skip("only a -tags devplugins build accepts unsigned manifests")
	}
	setCompiledKey(t, "")
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	res, err := mv.VerifyManifest(writeManifestFile(t, compiledKeyManifest("com.test.devplugins"), nil))
	if err != nil || !res.SignatureVerified {
		t.Fatalf("devplugins build with no key must accept an unsigned manifest: %v %+v", err, res)
	}
}

func TestImporter_NilVerifierRefusesWithoutDevplugins(t *testing.T) {
	if UnsignedPluginsAllowed() {
		t.Skip("devplugins build honours a nil verifier + SkipSignature")
	}
	db := openMarketplaceInstallerDB(t)
	t.Cleanup(func() { db.Close() })
	base := t.TempDir()
	imp := NewImporter(db, base, nil)
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	writeZipArchive(t, archive, map[string][]byte{"manifest.json": validImportManifest("com.test.nilverifier")})

	_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if !errors.Is(err, ErrNoMarketplaceKey) {
		t.Fatalf("nil verifier must refuse with ErrNoMarketplaceKey, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "com.test.nilverifier")); !os.IsNotExist(statErr) {
		t.Fatalf("refused import must not extract the plugin (stat err %v)", statErr)
	}
}

func TestImporter_SkipSignatureIgnoredWithoutDevplugins(t *testing.T) {
	if UnsignedPluginsAllowed() {
		t.Skip("devplugins build honours SkipSignature")
	}
	setCompiledKey(t, "")
	pub, _ := newKey(t)
	mv, err := NewManifestVerifier(hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	db := openMarketplaceInstallerDB(t)
	t.Cleanup(func() { db.Close() })
	imp := NewImporter(db, t.TempDir(), mv)
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	writeZipArchive(t, archive, map[string][]byte{"manifest.json": validImportManifest("com.test.skipsig")})

	_, err = imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if !errors.Is(err, ErrManifestUnsigned) {
		t.Fatalf("SkipSignature must be ignored outside devplugins (want ErrManifestUnsigned), got %v", err)
	}
}
