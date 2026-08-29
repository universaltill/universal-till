package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// panicHandler always panics — the deliberately-broken handler recovery is
// verified against.
func panicHandler(http.ResponseWriter, *http.Request) {
	panic("boom")
}

func initRecoveryTestI18n(t *testing.T) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

// A panicking handler behind recoverMiddleware must still produce a
// well-formed response — never a dropped connection (ut-docs#1271).
func TestRecoverMiddleware_APIPathReturnsJSONError(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(panicHandler))

	req := httptest.NewRequest(http.MethodGet, "/api/whatever", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Data  any `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Data != nil {
		t.Fatalf("data = %v, want nil", body.Data)
	}
	if body.Error.Code != "internal_error" {
		t.Fatalf("error.code = %q, want internal_error", body.Error.Code)
	}
	if body.Error.Message == "" || body.Error.Message == "common.error.server" {
		t.Fatalf("error.message = %q, want a localized string, not empty/raw key", body.Error.Message)
	}
}

// A panic on a non-API (page/HTMX-fragment) path must not drop the
// connection either — it gets a localized plain-text 500.
func TestRecoverMiddleware_NonAPIPathReturnsLocalizedText(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(panicHandler))

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if body == "" {
		t.Fatal("body is empty — connection would read as dropped, not a clean error")
	}
	want := httpx.T("en", "common.error.server")
	if want == "common.error.server" {
		t.Fatal("i18n not wired for the test — httpx.T fell back to the raw key")
	}
	// http.Error appends a trailing newline.
	if got := body; got != want+"\n" {
		t.Fatalf("body = %q, want %q", got, want+"\n")
	}
}

// A handler that already committed a response (status + some body, e.g. a
// CSV/export stream) before panicking mid-write must NOT get a second
// header or an appended error envelope — that would corrupt an in-flight
// 200 into a response that still LOOKS successful but is silently short or
// wrong. The honest outcome once output has started is: nothing more is
// written (the client sees the stream cut short, same as before this
// middleware existed) — found in review, ut-docs#1271.
func TestRecoverMiddleware_MidStreamPanicDoesNotCorruptCommittedResponse(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("id,name\n1,widget\n"))
		panic("boom mid-stream")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/export.csv", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d (the already-committed status must survive)", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("Content-Type = %q, want text/csv (must not be overwritten to application/json)", ct)
	}
	body := rec.Body.String()
	if body != "id,name\n1,widget\n" {
		t.Fatalf("body = %q, want exactly the pre-panic bytes with nothing appended", body)
	}
}

// http.ErrAbortHandler is net/http's own "abort this response silently"
// sentinel — recovery middleware must re-panic it, not swallow it into a
// 500, so net/http's Server can apply its own (silent, connection-closing)
// handling.
func TestRecoverMiddleware_RepanicsErrAbortHandler(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/whatever", nil)
	rec := httptest.NewRecorder()

	defer func() {
		got := recover()
		if got != http.ErrAbortHandler {
			t.Fatalf("recovered value = %v, want http.ErrAbortHandler to propagate", got)
		}
	}()
	h.ServeHTTP(rec, req)
	t.Fatal("expected http.ErrAbortHandler to panic back out of ServeHTTP")
}

// A handler that does NOT panic must be entirely unaffected by the wrap.
func TestRecoverMiddleware_PassesThroughWhenNoPanic(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("fine"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/whatever", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "fine" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "fine")
	}
}
