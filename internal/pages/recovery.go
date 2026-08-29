package pages

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
)

// recoverMiddleware wraps the whole request-handling chain (ut-docs#1271):
// internal/pages had no general HTTP panic-recovery — only
// sync_admin.go's syncPullPlugins recovers, and that's scoped to one
// background loop, unrelated to request handling. Without this, an
// unexpected handler panic drops the connection with no response at all;
// on the Android till that's indistinguishable from a plain network
// failure to the operator.
//
// Deliberately the OUTERMOST wrap in Init() (wraps auth.Middleware itself,
// not just mux) so a panic anywhere in the chain — including inside auth
// resolution — still gets a clean response instead of a dropped connection.
//
// Response shape mirrors auth.Middleware's own api-vs-page split: `/api/`
// paths get the same `{"data":null,"error":{code,message}}` JSON envelope
// every API handler uses; everything else (full-page loads and HTMX
// fragment loads alike) gets a localized plain-text 500 via http.Error —
// simpler than auth.Middleware's redirect-vs-fragment branch, since a
// recovery response never needs to navigate anywhere, just show a message
// wherever htmx or the browser renders it.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &recoveryResponseWriter{ResponseWriter: w}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// net/http's own convention: a handler panicking with this
			// sentinel means "abort this response silently" — net/http's
			// Server recovers it itself and just closes the connection.
			// Recovery middleware is expected to re-panic it rather than
			// write a response. Nothing in this repo panics with it today
			// (no httputil.ReverseProxy, no Hijack), but registerExternalProxy
			// is exactly the kind of surface that could grow one, so honor
			// the convention now rather than silently turning a deliberate
			// abort into a 500 later. Checked first, ahead of the log call
			// and the wroteHeader branch below, so it's never suppressed.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			// Full panic value + stack, for real server-side diagnosis (the
			// acceptance criterion this card was filed for). logging.Errorf
			// also feeds the operator-visible Problems ring
			// (backoffice_page.go) with this same string — a multi-KB stack
			// there is a real readability cost on a repeating panic, noted
			// as a follow-up rather than fixed here: trimming it would mean
			// either losing the full stack server-side too (logging.Logger
			// has no "log full, remember short" split today) or changing
			// the shared logging package, both bigger than this card's
			// scope.
			logging.L().Errorf("panic recovered: %v\n%s", rec, debug.Stack())

			// A handler that already wrote status/body before panicking (a
			// CSV/export stream that panics mid-write, e.g. reports_page.go,
			// eod_api.go, invoice_page.go, audit_page.go, import_page.go) has
			// already committed a response to the wire — writing a fresh
			// header or appending the error envelope now would corrupt it
			// into a 200 that LOOKS successful but silently ends short/wrong
			// (found in review, ut-docs#1271). Re-panicking ErrAbortHandler
			// here — rather than just returning — is what actually reproduces
			// the pre-existing "obviously failed" signal: returning normally
			// lets net/http finalize a well-formed-but-truncated 200 (verified
			// in review: for a Content-Length response the client sees a clean
			// read with no error at all, which is a WORSE silent-failure than
			// either outcome), whereas re-panicking here is recovered by
			// net/http's own top-level handler, which aborts the connection
			// the same way an unhandled panic always has. The panic is still
			// logged above either way — this only changes what, if anything,
			// reaches the client.
			if rw.wroteHeader {
				panic(http.ErrAbortHandler)
			}

			locale := httpx.ResolveLocale(w, r)
			msg := httpx.T(locale, "common.error.server")

			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": nil, "error": map[string]string{"code": "internal_error", "message": msg},
				})
				return
			}
			http.Error(w, msg, http.StatusInternalServerError)
		}()
		next.ServeHTTP(rw, r)
	})
}

// recoveryResponseWriter tracks whether the wrapped handler already started
// writing a response (status line and/or body), so recoverMiddleware can
// tell a mid-stream panic (response already committed) apart from one before
// any output (safe to still send a clean error response).
//
// Passes through Flush/Hijack/ReadFrom to the underlying ResponseWriter
// (found in review, ut-docs#1271): without these, wrapping the ResponseWriter
// silently drops optional capabilities from EVERY request in the app, since
// this middleware sits outermost. Concretely, io.Copy(w, someReader) — which
// http.ServeFile/http.ServeContent/http.FileServerFS use internally, and
// which several handlers call directly for backup/asset downloads
// (backup_api.go, plugin_icons.go, themes.go, sync_assets.go, sync_api.go,
// static_page.go) — only takes the zero-copy sendfile-style fast path when
// the destination itself implements io.ReaderFrom; a bare embedding would
// fail that type assertion against THIS wrapper and silently fall back to a
// byte-by-byte copy for every download in the app, not just this middleware's
// own concern.
type recoveryResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (rw *recoveryResponseWriter) WriteHeader(code int) {
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recoveryResponseWriter) Write(b []byte) (int, error) {
	// http.ResponseWriter.Write implicitly calls WriteHeader(200) on the
	// first call if it hasn't been called yet — mirror that here so a
	// handler that writes a body without ever calling WriteHeader directly
	// (e.g. `fmt.Fprintf(w, ...)`) is still tracked correctly.
	rw.wroteHeader = true
	return rw.ResponseWriter.Write(b)
}

// Flush lets a streaming handler (SSE, chunked progress) push partial output
// immediately, same as if this middleware weren't in the chain.
func (rw *recoveryResponseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack lets a handler take over the raw connection (websockets), same as
// if this middleware weren't in the chain. Nothing in this repo hijacks
// today, but failing the type assertion silently (rather than delegating)
// would be a correctness trap for whatever adds the first one.
func (rw *recoveryResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("recoveryResponseWriter: underlying ResponseWriter does not support Hijack")
	}
	rw.wroteHeader = true // hijacking hands the raw conn to the handler; treat as committed
	return h.Hijack()
}

// ReadFrom restores the zero-copy fast path documented on
// recoveryResponseWriter above (falls back to an explicit io.Copy, which is
// exactly what io.Copy itself would have done had this wrapper not
// implemented io.ReaderFrom at all — so this is never worse than not having
// the method, only ever better when the underlying writer can go faster).
func (rw *recoveryResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	rw.wroteHeader = true
	if rf, ok := rw.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(rw.ResponseWriter, r)
}
