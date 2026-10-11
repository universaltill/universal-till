package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#4137 (ADR-0154): the built-in background-removal engine is the
// vendored onnxruntime-web runtime plus the u2netp model. All three files
// must be embedded (the iOS/Android apps serve /public/ from the embed FS)
// and match the hashes in the provenance README, so a half-done upgrade (a
// runtime bundle with another release's wasm) or an unrecorded model swap
// fails here, not on a shop's tablet.
func TestVendoredBackgroundRemovalMatchesItsReadme(t *testing.T) {
	const dir = "public/vendor/bg-removal/"
	readme, err := fs.ReadFile(FS, dir+"README.md")
	if err != nil {
		t.Fatalf("provenance README not embedded: %v", err)
	}
	row := regexp.MustCompile("(?m)^\\| `([^`]+)` \\|[^|]*\\| `([0-9a-f]{64})` \\|")
	rows := row.FindAllSubmatch(readme, -1)
	want := map[string]bool{"ort.wasm.bundle.min.mjs": true, "ort-wasm-simd-threaded.wasm": true, "u2netp.onnx": true}
	if len(rows) != len(want) {
		t.Fatalf("README lists %d hashed files, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		name, sum := string(r[1]), string(r[2])
		if !want[name] {
			t.Fatalf("README lists unexpected file %q", name)
		}
		b, err := fs.ReadFile(FS, dir+name)
		if err != nil {
			t.Fatalf("%s not embedded: %v", name, err)
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != sum {
			t.Fatalf("%s sha256 = %x, README says %s — update both together (README.md)", name, got, sum)
		}
	}
	for _, lic := range []string{"LICENSE.onnxruntime-web", "LICENSE.u2net"} {
		b, err := fs.ReadFile(FS, dir+lic)
		if err != nil || !strings.Contains(string(b), "License") {
			t.Fatalf("%s missing or empty: %v", lic, err)
		}
	}
}
