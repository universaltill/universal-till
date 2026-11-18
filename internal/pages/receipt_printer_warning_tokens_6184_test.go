package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#6184: the receipt's "Printer unavailable" block hard-coded a light
// orange (#fff7ed / #fdba74 / #9a3412), a bright slab on the dark theme. It
// must take the theme's warning tokens, like .notice-block-warn, so every
// theme retunes it.
func TestReceiptPrinterWarning_UsesThemeTokens6184(t *testing.T) {
	body := tender6154(t, print.DestNone, "127.0.0.1:5000", &mgrUser)
	m := regexp.MustCompile(`\.receipt-printer-warning\s*\{([^}]*)\}`).FindStringSubmatch(cssCommentRe.ReplaceAllString(body, ""))
	if m == nil {
		t.Fatalf("receipt has no .receipt-printer-warning rule: %s", body)
	}
	decl := m[1]
	if lit := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(`).FindString(decl); lit != "" {
		t.Errorf(".receipt-printer-warning hard-codes colour %q; use the --warning-* tokens: %s", lit, decl)
	}
	for _, want := range []string{
		"background: var(--warning-tint)",
		"border: 1px solid var(--warning-tint-border)",
		"color: var(--warning-text)",
	} {
		if !strings.Contains(decl, want) {
			t.Errorf(".receipt-printer-warning lacks %q: %s", want, decl)
		}
	}
}
