package pages

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// signPluginManifest signs m the way the marketplace does since ADR-0165
// (ut-docs#4546), for a package whose files other than manifest.json are
// files (slash path -> bytes; nil for a manifest-only package such as
// writePluginBundle packs): content_digest over files, content_signature
// over the manifest with the digest kept, and the legacy signature over the
// manifest with signature and both content fields emptied.
func signPluginManifest(t *testing.T, m *plugins.Manifest, priv ed25519.PrivateKey, files map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	digest, err := plugins.ComputeContentDigest(dir)
	if err != nil {
		t.Fatalf("content digest: %v", err)
	}
	m.ContentDigest = digest
	sign := func(c plugins.Manifest) string {
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
	m.ContentSignature = sign(content)
	m.Signature = sign(legacy)
}
