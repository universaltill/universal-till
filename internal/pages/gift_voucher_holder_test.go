package pages

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ut-docs#4214 review: the tender validation bounds HolderLabel by BYTES
// (len > 200), so the popup must too. Bounding by runes let a 150-letter
// Arabic name (300 bytes) through the popup and then fail checkout with a
// bare 400.
func TestBoundGiftVoucherHolder_BytesOnRuneBoundary(t *testing.T) {
	arabic := strings.Repeat("ع", 150) // 2 bytes each = 300 bytes
	got := boundGiftVoucherHolder(arabic)
	if len(got) > 200 {
		t.Fatalf("len(got) = %d bytes, want <= 200", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8 (cut mid-rune)")
	}
	if got != strings.Repeat("ع", 100) {
		t.Fatalf("want the first 100 letters, got %d runes", utf8.RuneCountInString(got))
	}
	if s := "Anna"; boundGiftVoucherHolder(s) != s {
		t.Fatalf("short name changed")
	}
}
