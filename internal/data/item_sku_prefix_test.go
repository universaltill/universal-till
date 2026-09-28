package data

import "testing"

// ut-docs#3087: the prefix a category name turns into must be readable
// Latin letters, never a UUID fragment, and fall back to ITEM when the
// name can't make one.
func TestSKUPrefixFromName(t *testing.T) {
	cases := map[string]string{
		"Kuchen":      "KUC",
		"kaffee & co": "KAF",
		"Übergrößen":  "UBE",
		"Café":        "CAF",
		"Straße":      "STR",
		"ß":           "ITEM", // one letter only
		"7 Up":        "7UP",
		"123":         "ITEM", // no letter
		"":            "ITEM",
		"نوشیدنی":     "ITEM", // Persian: no Latin letters
		"Ab":          "AB",
		" - ":         "ITEM",
	}
	for in, want := range cases {
		if got := skuPrefixFromName(in); got != want {
			t.Errorf("skuPrefixFromName(%q) = %q, want %q", in, got, want)
		}
	}
}
