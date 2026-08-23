package httpx

import (
	"html/template"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	config "github.com/universaltill/universal-till/internal/config"
)

// RenderNotice writes the ut-docs#213 .pos-notice markup
// (docs/sale-screen-notifications.md) for handlers whose response fragment
// IS a notice — the same shape web/ui/partials/basket.html renders inline
// for the sale screen. ut-docs#238 extends the pattern to non-sale-screen
// message spots (catalog's export-save / labels-print handlers).

func TestRenderNotice_ErrorLevelIsAlertAndPersistent(t *testing.T) {
	InitI18n(realI18n(t), "en")
	var b strings.Builder
	RenderNotice(&b, "en", "error", "settings.enrol.forbidden")
	got := b.String()

	if !strings.Contains(got, `class="pos-notice error"`) {
		t.Fatalf("expected pos-notice error class, got: %s", got)
	}
	if !strings.Contains(got, `role="alert"`) {
		t.Fatalf("error notice must carry role=alert, got: %s", got)
	}
	if !strings.Contains(got, `class="notice-dismiss"`) {
		t.Fatalf("expected a dismiss button, got: %s", got)
	}
	want := T("en", "settings.enrol.forbidden")
	if !strings.Contains(got, want) {
		t.Fatalf("expected translated message %q, got: %s", want, got)
	}
}

func TestRenderNotice_SuccessLevelIsStatus(t *testing.T) {
	InitI18n(realI18n(t), "en")
	var b strings.Builder
	RenderNotice(&b, "en", "success", "settings.backup.saved_to")
	got := b.String()

	if !strings.Contains(got, `class="pos-notice success"`) {
		t.Fatalf("expected pos-notice success class, got: %s", got)
	}
	if !strings.Contains(got, `role="status"`) {
		t.Fatalf("success notice must carry role=status, got: %s", got)
	}
}

func TestRenderNotice_InfoLevelIsStatus(t *testing.T) {
	InitI18n(realI18n(t), "en")
	var b strings.Builder
	RenderNotice(&b, "en", "info", "notice.dismiss")
	got := b.String()

	if !strings.Contains(got, `class="pos-notice info"`) {
		t.Fatalf("expected pos-notice info class, got: %s", got)
	}
	if !strings.Contains(got, `role="status"`) {
		t.Fatalf("info notice must carry role=status, got: %s", got)
	}
}

func TestRenderNotice_DismissButtonHasTranslatedAriaLabel(t *testing.T) {
	InitI18n(realI18n(t), "en")
	var b strings.Builder
	RenderNotice(&b, "en", "error", "settings.enrol.forbidden")
	got := b.String()

	wantLabel := T("en", "notice.dismiss")
	if !strings.Contains(got, `aria-label="`+wantLabel+`"`) {
		t.Fatalf("expected dismiss aria-label %q, got: %s", wantLabel, got)
	}
}

// The message MUST come from the locale files, not a Go string literal —
// same rule the sale-screen pattern already enforces
// (TestScanUnknownItemNoticeIsTranslated in main_test.go's sibling package).
func TestRenderNotice_MessageIsLocaleBound(t *testing.T) {
	InitI18n(realI18n(t), "en")
	var en strings.Builder
	RenderNotice(&en, "en", "error", "settings.enrol.forbidden")

	var fa strings.Builder
	RenderNotice(&fa, "fa", "error", "settings.enrol.forbidden")

	enMsg := T("en", "settings.enrol.forbidden")
	faMsg := T("fa", "settings.enrol.forbidden")
	if enMsg == faMsg {
		t.Skip("en/fa translations happen to be identical for this key; locale-binding not observable")
	}
	if !strings.Contains(fa.String(), faMsg) {
		t.Fatalf("expected fa translation %q in fa-locale notice, got: %s", faMsg, fa.String())
	}
	if strings.Contains(fa.String(), enMsg) {
		t.Fatalf("fa-locale notice leaked the en message: %s", fa.String())
	}
}

// Some call sites append non-translatable content after the translated text
// (a filesystem path, a copies count) — the pre-#238 fmt.Fprintf call sites
// this replaces did exactly that.
func TestRenderNotice_AppendsExtraContentAfterMessage(t *testing.T) {
	InitI18n(realI18n(t), "en")
	var b strings.Builder
	RenderNotice(&b, "en", "success", "settings.backup.saved_to", "<code>/home/user/Downloads/catalog-2026-08-23.csv</code>")
	got := b.String()

	want := T("en", "settings.backup.saved_to") + " <code>/home/user/Downloads/catalog-2026-08-23.csv</code>"
	if !strings.Contains(got, want) {
		t.Fatalf("expected message + appended path, got: %s", got)
	}
}

func TestRenderNotice_WithoutTranslatorFallsBackToKey(t *testing.T) {
	InitI18n(nil, "en")
	var b strings.Builder
	RenderNotice(&b, "en", "error", "some.missing.key")
	got := b.String()
	if !strings.Contains(got, "some.missing.key") {
		t.Fatalf("expected fallback to bare key, got: %s", got)
	}
}

// Every other place a translation reaches a page is a {{ T "key" }} inside
// an html/template, which escapes it automatically. This helper writes
// straight to the ResponseWriter, so it has to escape the message itself —
// and it matters: this repo's own locale values already carry
// HTML-significant characters ("Help & Support", `... "Ask me later" ...`),
// and a locale can be supplied by a third-party language-pack plugin
// (ADR-0009) rather than by this repo at all.
func TestRenderNotice_TranslatedMessageIsHTMLEscaped(t *testing.T) {
	const hostile = `<img src=x onerror="alert(1)"> & 'quoted'`
	fsys := fstest.MapFS{"en.json": &fstest.MapFile{
		Data: []byte(`{"evil.key":` + strconv.Quote(hostile) + `,"notice.dismiss":"Dismiss"}`),
	}}
	i18n, err := config.NewI18nFS(fsys, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i18n, "en")
	t.Cleanup(func() { InitI18n(realI18n(t), "en") })

	var b strings.Builder
	RenderNotice(&b, "en", "error", "evil.key")
	got := b.String()

	if strings.Contains(got, "<img") {
		t.Fatalf("translated message written as live markup: %s", got)
	}
	if !strings.Contains(got, template.HTMLEscapeString(hostile)) {
		t.Fatalf("expected the escaped message, got: %s", got)
	}
}

// The dismiss aria-label is a translation too, and it lands inside a
// double-quoted attribute — an unescaped quote there would break out of the
// attribute entirely.
func TestRenderNotice_DismissAriaLabelIsHTMLEscaped(t *testing.T) {
	fsys := fstest.MapFS{"en.json": &fstest.MapFile{
		Data: []byte(`{"some.key":"hello","notice.dismiss":"say \"no\""}`),
	}}
	i18n, err := config.NewI18nFS(fsys, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i18n, "en")
	t.Cleanup(func() { InitI18n(realI18n(t), "en") })

	var b strings.Builder
	RenderNotice(&b, "en", "info", "some.key")
	got := b.String()

	if !strings.Contains(got, `aria-label="say &#34;no&#34;"`) {
		t.Fatalf("expected an escaped aria-label, got: %s", got)
	}
}
