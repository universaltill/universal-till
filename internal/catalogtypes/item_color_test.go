package catalogtypes_test

// ut-docs#1901: ValidItemColor is the server-side allowlist a submitted
// item color is checked against before it ever reaches storage (and, from
// there, a CSS custom property) — see ItemColors' own doc comment for why
// this is a real security control, not just UX. These tests pin the
// allowlist's shape directly, independent of the HTTP-layer wiring
// (internal/pages/catalog's validateLookups) that calls it.

import (
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
)

func TestValidItemColor_AcceptsEveryPaletteHexAndEmpty(t *testing.T) {
	if !catalogtypes.ValidItemColor("") {
		t.Fatal("expected the empty string (no color set) to be valid")
	}
	palette := catalogtypes.ItemColors()
	if len(palette) == 0 {
		t.Fatal("expected a non-empty palette")
	}
	for _, c := range palette {
		if !catalogtypes.ValidItemColor(c.Hex) {
			t.Fatalf("expected palette color %q (%s) to be valid", c.Hex, c.Key)
		}
	}
}

func TestValidItemColor_RejectsAnythingOutsideThePalette(t *testing.T) {
	cases := []string{
		"#ffffff",                // syntactically valid hex, but not in the palette
		"#0f172a;background:red", // CSS-injection shaped
		"red",
		"not-a-color",
		"#0F172A", // wrong case — the palette is matched literally, not case-insensitively
	}
	for _, hex := range cases {
		if catalogtypes.ValidItemColor(hex) {
			t.Fatalf("expected %q to be rejected", hex)
		}
	}
}

// TestItemColors_KeysAndHexAreUniqueAndWellFormed guards against a typo'd
// duplicate entry (two swatches sharing a Key or Hex) silently shrinking
// the picker, and against an entry missing its I18nKey (which would
// render a template's {{ T .I18nKey }} call as a bare, untranslated key
// string).
func TestItemColors_KeysAndHexAreUniqueAndWellFormed(t *testing.T) {
	seenKey := map[string]bool{}
	seenHex := map[string]bool{}
	for _, c := range catalogtypes.ItemColors() {
		if c.Key == "" || c.Hex == "" || c.I18nKey == "" {
			t.Fatalf("incomplete palette entry: %+v", c)
		}
		if len(c.Hex) != 7 || c.Hex[0] != '#' {
			t.Fatalf("expected a 6-digit #hex value, got %q", c.Hex)
		}
		if seenKey[c.Key] {
			t.Fatalf("duplicate palette key %q", c.Key)
		}
		if seenHex[c.Hex] {
			t.Fatalf("duplicate palette hex %q", c.Hex)
		}
		seenKey[c.Key] = true
		seenHex[c.Hex] = true
	}
}
