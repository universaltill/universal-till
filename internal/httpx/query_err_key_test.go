package httpx

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/config"
)

func fakeLocaleFS(enJSON string) fstest.MapFS {
	return fstest.MapFS{"en.json": &fstest.MapFile{Data: []byte(enJSON)}}
}

// TestQueryErrKey_UnrecognisedValueFallsBackToGeneric is the actual
// vulnerability this guards (ut-docs#2148): a page's error banner reads
// `?err=` and renders it through `{{ T .errKey }}`, whose own fallback is
// to print an unresolved key verbatim — so a crafted link like
// `?err=Your+card+was+declined` used to make the till itself display
// attacker-chosen text in its red error banner.
func TestQueryErrKey_UnrecognisedValueFallsBackToGeneric(t *testing.T) {
	i, err := config.NewI18nFS(fakeLocaleFS(`{"common.error.server":"Something went wrong. Please try again."}`), "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	r := httptest.NewRequest("GET", "/tables?err=Your+card+was+declined", nil)
	if got := QueryErrKey(r); got != genericErrKey {
		t.Fatalf("QueryErrKey(unrecognised) = %q, want the generic fallback %q", got, genericErrKey)
	}
}

// A real, known key (the normal case — a page redirecting to itself after a
// validation failure) must still pass through unchanged.
func TestQueryErrKey_RealKeyPassesThrough(t *testing.T) {
	i, err := config.NewI18nFS(fakeLocaleFS(`{"tables.error.load_failed":"Could not load tables."}`), "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	r := httptest.NewRequest("GET", "/tables?err=tables.error.load_failed", nil)
	if got := QueryErrKey(r); got != "tables.error.load_failed" {
		t.Fatalf("QueryErrKey(real key) = %q, want it unchanged", got)
	}
}

// No ?err= at all must stay empty — the templates gate the whole banner on
// `{{ if .errKey }}`, and a non-empty generic fallback here would make every
// page missing the query param suddenly show a banner it never had before.
func TestQueryErrKey_EmptyStaysEmpty(t *testing.T) {
	i, err := config.NewI18nFS(fakeLocaleFS(`{}`), "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	r := httptest.NewRequest("GET", "/tables", nil)
	if got := QueryErrKey(r); got != "" {
		t.Fatalf("QueryErrKey(no query) = %q, want empty", got)
	}
}

// No translator wired (InitI18n never called, or called with nil) can't
// verify a key either way — mirrors T's own nil-safety rather than failing
// closed on every test that renders a page without bootstrapping i18n.
func TestQueryErrKey_NilTranslatorPassesThrough(t *testing.T) {
	InitI18n(nil, "en")
	r := httptest.NewRequest("GET", "/tables?err=anything", nil)
	if got := QueryErrKey(r); got != "anything" {
		t.Fatalf("QueryErrKey(nil translator) = %q, want the raw value unchanged", got)
	}
}

// TestQueryMsgKey_UnrecognisedValueFallsBackToGeneric mirrors QueryErrKey's
// own vulnerability, found in review (ut-docs#2148): fiscal_device_page.go's
// `?msg=` feeds a "login-ok" SUCCESS banner through the identical
// `{{ T .msgKey }}` fallback-to-key hazard — arguably worse than the error
// case, since a crafted link can make the till display a fake "confirmed"
// message.
func TestQueryMsgKey_UnrecognisedValueFallsBackToGeneric(t *testing.T) {
	i, err := config.NewI18nFS(fakeLocaleFS(`{"common.error.server":"Something went wrong. Please try again."}`), "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	r := httptest.NewRequest("GET", "/fiscal-device?msg=Device+confirmed+successfully", nil)
	if got := QueryMsgKey(r); got != genericErrKey {
		t.Fatalf("QueryMsgKey(unrecognised) = %q, want the generic fallback %q", got, genericErrKey)
	}
}

func TestQueryMsgKey_RealKeyPassesThrough(t *testing.T) {
	i, err := config.NewI18nFS(fakeLocaleFS(`{"fiscaldevice.confirm.success":"Device confirmed."}`), "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	r := httptest.NewRequest("GET", "/fiscal-device?msg=fiscaldevice.confirm.success", nil)
	if got := QueryMsgKey(r); got != "fiscaldevice.confirm.success" {
		t.Fatalf("QueryMsgKey(real key) = %q, want it unchanged", got)
	}
}

func TestQueryMsgKey_EmptyStaysEmpty(t *testing.T) {
	i, err := config.NewI18nFS(fakeLocaleFS(`{}`), "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	InitI18n(i, "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	r := httptest.NewRequest("GET", "/fiscal-device", nil)
	if got := QueryMsgKey(r); got != "" {
		t.Fatalf("QueryMsgKey(no query) = %q, want empty", got)
	}
}
