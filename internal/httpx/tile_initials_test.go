package httpx

import "testing"

// ut-docs#3059: a phone tile without a picture shows its item's initials on
// the item colour, like an app icon. Rune-safe: never half a UTF-8 letter.
func TestTileInitials(t *testing.T) {
	cases := map[string]string{
		"Cappuccino":            "C",
		"Latte Macchiato":       "LM",
		"Espresso-Bohnen 250 g": "EB",
		"über Kuchen":           "ÜK",
		"  (Chai) latte ":       "CL",
		"چای ماسالا":            "چم",
		"Şiş kebap":             "ŞK",
		"0,5 Pils":              "0P",
		"Three word name":       "TW",
		"":                      "",
		"—":                     "",
	}
	for in, want := range cases {
		if got := TileInitials(in); got != want {
			t.Errorf("TileInitials(%q) = %q, want %q", in, got, want)
		}
	}
}
