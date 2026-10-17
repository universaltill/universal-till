package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testPkgFile is one file of a test plugin package other than manifest.json.
type testPkgFile struct {
	name string // slash path relative to the package root
	data []byte
	mode int64
}

// signTestManifest signs m the way the marketplace does since ADR-0165
// (ut-docs#4546): content_digest over files, content_signature over the
// manifest with the digest kept, and the legacy signature over the manifest
// with all three emptied. Written out field by field here rather than via
// the verifier's own helpers, so a drift in either form fails the tests.
func signTestManifest(t *testing.T, m *Manifest, priv ed25519.PrivateKey, files []testPkgFile) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	digest, err := ComputeContentDigest(dir)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	m.ContentDigest = digest
	signForm := func(c Manifest) string {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal canonical manifest: %v", err)
		}
		return hex.EncodeToString(ed25519.Sign(priv, raw))
	}
	content := *m
	content.Signature, content.ContentSignature = "", ""
	legacy := *m
	legacy.Signature, legacy.ContentDigest, legacy.ContentSignature = "", "", ""
	m.ContentSignature = signForm(content)
	m.Signature = signForm(legacy)
}

// tarGzPackage packs manifest.json (m as it stands) plus files into a
// .tar.gz, the shape the marketplace serves and Import accepts.
func tarGzPackage(t *testing.T, m *Manifest, files []testPkgFile) []byte {
	t.Helper()
	manifestBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	writeMarketplaceTarFile(t, tw, "manifest.json", manifestBytes, 0o644)
	for _, f := range files {
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		writeMarketplaceTarFile(t, tw, f.name, f.data, mode)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return archive.Bytes()
}

// writeSignedPackageDir signs m for files and lays the package out on disk
// as an extracted bundle; returns the manifest.json path.
func writeSignedPackageDir(t *testing.T, m *Manifest, priv ed25519.PrivateKey, files []testPkgFile) string {
	t.Helper()
	signTestManifest(t, m, priv, files)
	return writePackageDir(t, m, files)
}

// writePackageDir lays m (as it stands) and files out on disk.
func writePackageDir(t *testing.T, m *Manifest, files []testPkgFile) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}
