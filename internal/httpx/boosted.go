package httpx

import (
	"net/http"
	"strings"
)

// BoostedSwap is how a boosted navigation's response replaces the live
// #ut-page: the response's own region, outerHTML, scrolled to the top,
// under a same-document View Transition (ADR-0097's motion as amended by
// ADR-0118, ADR-0098).
const BoostedSwap = "outerHTML show:window:top transition:true"

// BoostedNavigation tells htmx, per response, where a boosted navigation
// lands (ut-docs#2224, ADR-0098). base.html's #ut-page region carries
// hx-boost only: hx-target/hx-select/hx-swap on that element would be
// inherited by every hx-get chip and poller inside it, and their first
// `load` would then outerHTML-swap the whole region away with an empty
// selection — verified live. htmx's HX-Retarget/HX-Reselect/HX-Reswap
// response headers are read for the boosted request only, so the region
// is addressed here, for an HTML response, and nowhere else. A redirect
// is followed inside the XHR (the final response gets the headers). An
// HTML error page gets them too — RenderError renders base.html, so a
// boosted 403/404/500 swaps that page's own #ut-page in place with the
// rail intact (htmx's default for an error would otherwise be a bare
// body innerHTML swap, killing the on-screen keyboard); a non-HTML
// response is left to the client's own shell-signature fallback (a full
// document load).
func BoostedNavigation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsBoosted(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", ShellNavHeader)
		bw := &boostedWriter{ResponseWriter: w}
		next.ServeHTTP(bw, r)
		bw.finish()
	})
}

// ShellNavHeader is set by base.html's shell script on a request whose
// boost comes from the #ut-page region itself — not from a dialog form
// that carries its own hx-boost + hx-target (the list-and-dialog pattern,
// whose refusal fragments must keep landing in their own message slot).
// `HX-Boosted: true` alone cannot tell the two apart.
const ShellNavHeader = "UT-Shell-Nav"

// IsBoosted reports whether htmx issued this request for a boosted page
// navigation of the persistent shell (an <a> inside base.html's #ut-page
// region, ADR-0098).
func IsBoosted(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("HX-Boosted"), "true") && r.Header.Get(ShellNavHeader) == "1"
}

type boostedWriter struct {
	http.ResponseWriter
	wrote   bool
	pending int // a WriteHeader held back until the first Write can sniff Content-Type
}

// WriteHeader decides the retarget headers once the Content-Type is known.
// RenderError (every 403/404/500 page) calls WriteHeader BEFORE rendering
// and never sets Content-Type itself, so a status with no Content-Type yet
// is held until the first Write sniffs it — finish() sends it if the
// handler never writes a body.
func (b *boostedWriter) WriteHeader(status int) {
	if b.wrote {
		return
	}
	if b.Header().Get("Content-Type") == "" && status != http.StatusNoContent && status != http.StatusNotModified && (status < 300 || status >= 400) {
		b.pending = status
		return
	}
	b.commit(status)
}

func (b *boostedWriter) commit(status int) {
	b.wrote = true
	b.pending = 0
	if status != http.StatusNoContent && strings.HasPrefix(b.Header().Get("Content-Type"), "text/html") {
		b.Header().Set("HX-Retarget", "#ut-page")
		b.Header().Set("HX-Reselect", "#ut-page")
		b.Header().Set("HX-Reswap", BoostedSwap)
	}
	b.ResponseWriter.WriteHeader(status)
}

// finish sends a held-back status for a handler that wrote no body at all.
func (b *boostedWriter) finish() {
	if !b.wrote && b.pending != 0 {
		b.commit(b.pending)
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (b *boostedWriter) Unwrap() http.ResponseWriter { return b.ResponseWriter }

func (b *boostedWriter) Flush() {
	if !b.wrote {
		b.commit(b.pendingOr(http.StatusOK))
	}
	if f, ok := b.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (b *boostedWriter) pendingOr(status int) int {
	if b.pending != 0 {
		return b.pending
	}
	return status
}

func (b *boostedWriter) Write(p []byte) (int, error) {
	if !b.wrote {
		// Most page handlers never set Content-Type themselves and rely on
		// net/http sniffing it at the first Write — which happens in the
		// underlying writer, after this WriteHeader. Sniff the same way so
		// the decision above sees what the browser will.
		if b.Header().Get("Content-Type") == "" {
			b.Header().Set("Content-Type", http.DetectContentType(p))
		}
		b.commit(b.pendingOr(http.StatusOK))
	}
	return b.ResponseWriter.Write(p)
}
