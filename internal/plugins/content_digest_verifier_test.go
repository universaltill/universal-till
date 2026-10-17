package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0165 (ut-docs#4546): with a key configured, VerifyManifest also
// requires content_digest + content_signature and checks the digest against
// the package directory, so the signature covers every file, not only
// manifest.json.

func contentTestManifest() *Manifest {
	return &Manifest{
		ID: "com.test.content", Name: "Content", Version: "1.0.0",
		Entrypoint: "./plugin.wasm", Runtime: "wasm",
		CanonicalType: "page", DeviceArch: "any",
	}
}

func contentTestFiles() []testPkgFile {
	return []testPkgFile{
		{name: "plugin.wasm", data: []byte("\x00asm\x01\x00\x00\x00")},
		{name: "locales/de.json", data: []byte(`{"k":"v"}`)},
	}
}

func contentTestVerifier(t *testing.T) (*ManifestVerifier, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mv, err := NewManifestVerifier(hex.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	return mv, priv
}

func requireRefused(t *testing.T, mv *ManifestVerifier, manifestPath, wantSubstr string) {
	t.Helper()
	res, err := mv.VerifyManifest(manifestPath)
	if err == nil {
		t.Fatalf("package accepted, want refusal naming %q", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("error %q does not name %q", err, wantSubstr)
	}
	if res != nil && res.SignatureVerified {
		t.Fatalf("SignatureVerified=true on a refused package: %v", err)
	}
}

func TestVerifyManifest_ContentDigestValidPackagePasses(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	path := writeSignedPackageDir(t, contentTestManifest(), priv, contentTestFiles())
	res, err := mv.VerifyManifest(path)
	if err != nil {
		t.Fatalf("validly signed package refused: %v", err)
	}
	if !res.SignatureVerified {
		t.Fatal("SignatureVerified=false for a valid package")
	}
}

// The hole ADR-0165 closes: the manifest and its legacy signature are
// genuine, a file next to it is not.
func TestVerifyManifest_ChangedFileRefusedDespiteValidSignature(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	path := writeSignedPackageDir(t, contentTestManifest(), priv, contentTestFiles())
	wasm := filepath.Join(filepath.Dir(path), "plugin.wasm")
	if err := os.WriteFile(wasm, []byte("\x00asm\x01\x00\x00\x01"), 0o644); err != nil {
		t.Fatal(err)
	}
	requireRefused(t, mv, path, "content digest mismatch")
}

func TestVerifyManifest_AddedOrRemovedFileRefused(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	path := writeSignedPackageDir(t, contentTestManifest(), priv, contentTestFiles())
	extra := filepath.Join(filepath.Dir(path), "locales", "fr.json")
	if err := os.WriteFile(extra, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	requireRefused(t, mv, path, "content digest mismatch")

	path = writeSignedPackageDir(t, contentTestManifest(), priv, contentTestFiles())
	if err := os.Remove(filepath.Join(filepath.Dir(path), "locales", "de.json")); err != nil {
		t.Fatal(err)
	}
	requireRefused(t, mv, path, "content digest mismatch")
}

func TestVerifyManifest_SymlinkInPackageRefused(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	path := writeSignedPackageDir(t, contentTestManifest(), priv, contentTestFiles())
	if err := os.Symlink("/etc/passwd", filepath.Join(filepath.Dir(path), "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	requireRefused(t, mv, path, "symlink")
}

// A missing digest is refused, not treated as legacy: otherwise stripping
// both fields would turn any signed manifest back into one that covers
// nothing (ADR-0165 §4).
func TestVerifyManifest_MissingContentFieldsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		strip func(m *Manifest)
	}{
		{"both", func(m *Manifest) { m.ContentDigest, m.ContentSignature = "", "" }},
		{"digest", func(m *Manifest) { m.ContentDigest = "" }},
		{"content_signature", func(m *Manifest) { m.ContentSignature = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mv, priv := contentTestVerifier(t)
			m := contentTestManifest()
			signTestManifest(t, m, priv, contentTestFiles())
			tc.strip(m)
			path := writePackageDir(t, m, contentTestFiles())
			requireRefused(t, mv, path, "manifest missing required field: content_digest/content_signature")
			// A pre-ADR-0165 bundle (an export of an older install) gets the
			// actionable "install it from the Plugin Store" message, not the
			// generic import failure (ut-docs#4546 review M1).
			if _, err := mv.VerifyManifest(path); !errors.Is(err, ErrManifestUnsigned) {
				t.Fatalf("err = %v, want it to wrap ErrManifestUnsigned", err)
			}
		})
	}
}

func TestVerifyManifest_BadContentSignatureRefused(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)

	// Signed by a different key.
	m := contentTestManifest()
	signTestManifest(t, m, priv, contentTestFiles())
	other := contentTestManifest()
	signTestManifest(t, other, otherPriv, contentTestFiles())
	m.ContentSignature = other.ContentSignature
	requireRefused(t, mv, writePackageDir(t, m, contentTestFiles()), "content signature verification failed")

	// Not hex.
	m = contentTestManifest()
	signTestManifest(t, m, priv, contentTestFiles())
	m.ContentSignature = "zz"
	requireRefused(t, mv, writePackageDir(t, m, contentTestFiles()), "invalid content_signature hex")
}

// Re-pointing content_digest at swapped files without a matching
// content_signature: the digest matches the directory, the signature over
// it does not.
func TestVerifyManifest_ForgedDigestRefused(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	m := contentTestManifest()
	signTestManifest(t, m, priv, contentTestFiles())
	swapped := []testPkgFile{{name: "plugin.wasm", data: []byte("evil")}, contentTestFiles()[1]}
	forged, err := ComputeContentDigest(filepath.Dir(writePackageDir(t, &Manifest{}, swapped)))
	if err != nil {
		t.Fatal(err)
	}
	m.ContentDigest = forged
	requireRefused(t, mv, writePackageDir(t, m, swapped), "content signature verification failed")
}

// The legacy signature still has to be valid on its own.
func TestVerifyManifest_BadLegacySignatureRefusedEvenWithValidContent(t *testing.T) {
	mv, priv := contentTestVerifier(t)
	m := contentTestManifest()
	signTestManifest(t, m, priv, contentTestFiles())
	m.Name = "Renamed"
	// Re-sign only the content form, so only the legacy signature is stale.
	c := *m
	c.Signature, c.ContentSignature = "", ""
	raw, _ := json.Marshal(c)
	m.ContentSignature = hex.EncodeToString(ed25519.Sign(priv, raw))
	requireRefused(t, mv, writePackageDir(t, m, contentTestFiles()), "manifest signature verification failed")
}

// Backward compatibility (ADR-0165 §3): the legacy canonical bytes of a
// manifest signed the new way carry no content_ key and equal what a till
// without this change marshals (its struct has no such fields), so its
// `signature` check still passes.
func TestVerifyManifest_LegacyCanonicalBytesUnchanged(t *testing.T) {
	_, priv := contentTestVerifier(t)
	pub := priv.Public().(ed25519.PublicKey)
	m := contentTestManifest()
	signTestManifest(t, m, priv, contentTestFiles())

	legacy, err := legacySignedBytes(*m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("content_")) {
		t.Fatalf("legacy canonical bytes carry a content_ key: %s", legacy)
	}
	// What an older till computes: the manifest with the new fields absent
	// (dropped on Unmarshal) and signature emptied.
	old := *m
	old.ContentDigest, old.ContentSignature, old.Signature = "", "", ""
	oldBytes, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacy, oldBytes) {
		t.Fatalf("legacy canonical bytes differ from an older till's:\n%s\n%s", legacy, oldBytes)
	}
	sig, _ := hex.DecodeString(m.Signature)
	if !ed25519.Verify(pub, oldBytes, sig) {
		t.Fatal("a new-style signature does not verify under the older till's canonical form")
	}

	// And the content form keeps the digest but not either signature.
	content, err := contentSignedBytes(*m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`"content_digest":"`+m.ContentDigest+`"`)) || bytes.Contains(content, []byte("content_signature")) || bytes.Contains(content, []byte(m.Signature)) {
		t.Fatalf("content canonical form wrong: %s", content)
	}
}

// devplugins (no key, allowUnsigned) keeps today's structure-only check:
// no digest required (ADR-0158, ADR-0165 §4).
func TestVerifyManifest_StructuralOnlyNeedsNoContentDigest(t *testing.T) {
	path := writePackageDir(t, contentTestManifest(), contentTestFiles())
	if _, err := structuralOnlyVerifier().VerifyManifest(path); err != nil {
		t.Fatalf("structure-only verifier refused an unsigned package: %v", err)
	}
}
