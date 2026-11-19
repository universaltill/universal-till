package httpx

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserPrintOff_Source(t *testing.T) {
	t.Cleanup(SnapshotStateForTests())
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	InitBrowserPrint(nil)
	if BrowserPrintOff(r) || BrowserPrintOff(nil) {
		t.Fatal("nil source must never be off")
	}
	InitBrowserPrint(func(*http.Request) bool { return true })
	if !BrowserPrintOff(r) {
		t.Fatal("source true must be off")
	}
	if BrowserPrintOff(nil) {
		t.Fatal("nil request must not be off")
	}
}

// WithBrowserPrint binds BOTH browserprintoff and the viewer's `allowed`
// (ut-docs#6154 UX: the tender receipt path doesn't pass withHelpHref, where
// the request-less `allowed` fails closed for everyone).
func TestWithBrowserPrint_BindsFlagAndAllowed(t *testing.T) {
	t.Cleanup(SnapshotStateForTests())
	InitBrowserPrint(func(*http.Request) bool { return true })
	InitRailVisibility(func(_ *http.Request, p string) bool { return p == "settings" })
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	base := FuncsFor("en")
	out := WithBrowserPrint(base, r)
	tpl := template.Must(template.New("x").Funcs(out).Parse(
		`{{ if browserprintoff }}OFF{{ end }}|{{ if allowed "settings" }}S{{ end }}|{{ if allowed "stock" }}K{{ end }}`))
	var sb strings.Builder
	if err := tpl.Execute(&sb, nil); err != nil {
		t.Fatal(err)
	}
	if got := sb.String(); got != "OFF|S|" {
		t.Fatalf("got %q, want OFF|S|", got)
	}
	// the default (unbound) map is "on" (not off)
	tpl2 := template.Must(template.New("y").Funcs(base).Parse(`{{ if browserprintoff }}OFF{{ else }}ON{{ end }}`))
	sb.Reset()
	_ = tpl2.Execute(&sb, nil)
	if sb.String() != "ON" {
		t.Fatalf("default browserprintoff must be false, got %q", sb.String())
	}
}
