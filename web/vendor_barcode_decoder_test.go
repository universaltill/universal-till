package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"regexp"
	"testing"
)

// ut-docs#696: the sale screen's camera scan lazy-loads a vendored
// BarcodeDetector ponyfill plus the zxing-wasm reader it was built against.
// The ponyfill embeds that wasm's SHA-256; a half-done upgrade (one file
// replaced, not the other) would ship a decoder that fails on iPhone/iPad
// only, where nobody runs CI. Both must also be embedded in the binary, since
// the iOS/Android apps serve /public/ from the embed FS.
func TestVendoredBarcodeDecoderMatchesItsWasm(t *testing.T) {
	const dir = "public/vendor/barcode-detector/"
	js, err := fs.ReadFile(FS, dir+"ponyfill.js")
	if err != nil {
		t.Fatalf("ponyfill not embedded: %v", err)
	}
	wasm, err := fs.ReadFile(FS, dir+"zxing_reader.wasm")
	if err != nil {
		t.Fatalf("zxing reader wasm not embedded: %v", err)
	}

	// Minified as `ZXING_WASM_SHA256=<ident>` with `<ident>=\`<hex>\`` elsewhere.
	ident := regexp.MustCompile(`ZXING_WASM_SHA256=([A-Za-z_$][\w$]*)`).FindSubmatch(js)
	if ident == nil {
		t.Fatal("ponyfill.js no longer exports ZXING_WASM_SHA256; re-check the vendored build")
	}
	// Minifiers reuse one-letter names across scopes, so accept the match
	// only if exactly one distinct 64-hex value is bound to that name.
	vals := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:^|[^\w$])`+regexp.QuoteMeta(string(ident[1]))+"=`([0-9a-f]{64})`").FindAllSubmatch(js, -1) {
		vals[string(m[1])] = true
	}
	if len(vals) != 1 {
		t.Fatalf("expected one sha256 bound to %s in ponyfill.js, found %d — re-check the vendored build", ident[1], len(vals))
	}
	sum := sha256.Sum256(wasm)
	got := hex.EncodeToString(sum[:])
	if !vals[got] {
		t.Fatalf("zxing_reader.wasm sha256 = %s, ponyfill.js was built against another wasm (%v) — replace both from the same release (web/public/vendor/barcode-detector/README.md)", got, vals)
	}

	// The ponyfill's default wasm location is a CDN; app.js must override it.
	// Guard against a future upgrade that renames the hook app.js relies on.
	for _, export := range []string{"prepareZXingModule", "purgeZXingModule", "BarcodeDetector"} {
		if !regexp.MustCompile(`\.` + export + `=`).Match(js) {
			t.Errorf("ponyfill.js no longer exports %s, which web/public/app.js calls", export)
		}
	}
}
