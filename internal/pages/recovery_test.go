package pages

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
// wrong (found in review, ut-docs#1271). The first fix for this just
// returned without writing more, but that turned out to be its own silent
// failure: net/http then finalizes a well-formed-but-truncated 200, which a
// client reading by Content-Length sees as a clean, complete read — worse
// than the corrupted-body bug it replaced, because there is no signal at
// all that anything went wrong. Re-panicking http.ErrAbortHandler instead
// (second fix, same as the sentinel case above) reproduces the pre-existing
// "obviously failed" outcome: net/http's own top-level recover sees it and
// aborts the connection.
func TestRecoverMiddleware_MidStreamPanicAbortsRatherThanFinalizingATruncatedResponse(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("id,name\n1,widget\n"))
		panic("boom mid-stream")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/export.csv", nil)
	rec := httptest.NewRecorder()

	defer func() {
		got := recover()
		if got != http.ErrAbortHandler {
			t.Fatalf("recovered value = %v, want http.ErrAbortHandler (net/http's own server aborts the connection on this, rather than finalizing a truncated response)", got)
		}
		// The pre-panic bytes are still whatever the client received up to
		// that point (this is what "cut short" means) — no error envelope,
		// no second header, appended on top of them.
		if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
			t.Fatalf("Content-Type = %q, want text/csv (must not be overwritten to application/json)", ct)
		}
		if body := rec.Body.String(); body != "id,name\n1,widget\n" {
			t.Fatalf("body = %q, want exactly the pre-panic bytes with nothing appended", body)
		}
	}()
	h.ServeHTTP(rec, req)
	t.Fatal("expected http.ErrAbortHandler to panic back out of ServeHTTP")
}

// A handler that writes a body WITHOUT ever calling WriteHeader explicitly
// (net/http implicitly sends 200 on the first Write) must be tracked as
// committed too — pins the Write() branch specifically, since the other
// mid-stream test above calls WriteHeader explicitly and so cannot catch a
// regression there on its own (found in review, ut-docs#1271).
func TestRecoverMiddleware_ImplicitWriteHeaderAlsoCountsAsCommitted(t *testing.T) {
	initRecoveryTestI18n(t)
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial")) // no explicit WriteHeader
		panic("boom mid-stream, implicit 200")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/whatever", nil)
	rec := httptest.NewRecorder()

	defer func() {
		got := recover()
		if got != http.ErrAbortHandler {
			t.Fatalf("recovered value = %v, want http.ErrAbortHandler", got)
		}
		if body := rec.Body.String(); body != "partial" {
			t.Fatalf("body = %q, want exactly the pre-panic bytes with nothing appended", body)
		}
	}()
	h.ServeHTTP(rec, req)
	t.Fatal("expected http.ErrAbortHandler to panic back out of ServeHTTP")
}

// fakeStreamingResponseWriter implements the optional http.ResponseWriter
// capabilities (Flusher, Hijacker, io.ReaderFrom) so recoveryResponseWriter's
// pass-through can be pinned without a real network connection.
type fakeStreamingResponseWriter struct {
	http.ResponseWriter
	flushed     bool
	hijacked    bool
	readFromArg io.Reader
}

func (f *fakeStreamingResponseWriter) Flush() { f.flushed = true }

func (f *fakeStreamingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	f.hijacked = true
	return nil, nil, nil
}

func (f *fakeStreamingResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	f.readFromArg = r
	n, err := io.Copy(io.Discard, r)
	return n, err
}

// recoveryResponseWriter must delegate Flush/Hijack/ReadFrom to an
// underlying ResponseWriter that supports them (found in review,
// ut-docs#1271) — without this, wrapping the ResponseWriter silently drops
// the sendfile-style fast path for every download in the app (backup_api.go,
// static_page.go, plugin_icons.go, …), since this middleware sits outermost.
func TestRecoveryResponseWriter_DelegatesOptionalInterfaces(t *testing.T) {
	fake := &fakeStreamingResponseWriter{ResponseWriter: httptest.NewRecorder()}
	rw := &recoveryResponseWriter{ResponseWriter: fake}

	rw.Flush()
	if !fake.flushed {
		t.Fatal("Flush did not reach the underlying ResponseWriter")
	}

	if _, _, err := rw.Hijack(); err != nil {
		t.Fatalf("Hijack returned an error, want it to delegate cleanly: %v", err)
	}
	if !fake.hijacked {
		t.Fatal("Hijack did not reach the underlying ResponseWriter")
	}
	if !rw.wroteHeader {
		t.Fatal("Hijack must mark the response as committed (raw conn handed to the handler)")
	}

	src := strings.NewReader("payload")
	n, err := rw.ReadFrom(src)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("payload")) {
		t.Fatalf("ReadFrom n = %d, want %d", n, len("payload"))
	}
	if fake.readFromArg != src {
		t.Fatal("ReadFrom did not delegate to the underlying io.ReaderFrom — the fast path was not taken")
	}
}

// When the underlying ResponseWriter does NOT implement io.ReaderFrom (the
// common case — httptest.ResponseRecorder doesn't), ReadFrom must still
// work correctly via a plain io.Copy fallback, not fail or drop bytes.
func TestRecoveryResponseWriter_ReadFromFallsBackWithoutUnderlyingReaderFrom(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &recoveryResponseWriter{ResponseWriter: rec}

	n, err := rw.ReadFrom(strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("payload")) {
		t.Fatalf("ReadFrom n = %d, want %d", n, len("payload"))
	}
	if rec.Body.String() != "payload" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "payload")
	}
	if !rw.wroteHeader {
		t.Fatal("ReadFrom must mark the response as committed, same as Write")
	}
}

// Hijack must report failure (not panic, not silently succeed) when the
// underlying ResponseWriter genuinely doesn't support it — httptest's
// recorder is exactly that case.
func TestRecoveryResponseWriter_HijackFailsCleanlyWithoutUnderlyingHijacker(t *testing.T) {
	rw := &recoveryResponseWriter{ResponseWriter: httptest.NewRecorder()}
	if _, _, err := rw.Hijack(); err == nil {
		t.Fatal("Hijack = nil error, want an error (httptest.ResponseRecorder does not support Hijack)")
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
