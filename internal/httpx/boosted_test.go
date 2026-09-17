package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ut-docs#2224 / ADR-0098: a boosted navigation (`HX-Boosted: true`) must
// swap the response's #ut-page over the live one. The wrapper region is
// the only element that may carry hx-boost — hx-target/hx-select/hx-swap
// on it would be INHERITED by every rail chip and poller inside it (their
// first `load` would outerHTML-swap the whole region away with an empty
// selection, verified live). So the server says it instead, per response,
// through htmx's own HX-Retarget/HX-Reselect/HX-Reswap headers.
func TestBoostedNavigation_SetsRetargetHeadersOnBoostedHTML(t *testing.T) {
	h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><div id=\"ut-page\"></div></body></html>"))
	}))
	r := httptest.NewRequest("GET", "/menu", nil)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Boosted", "true")
	r.Header.Set(ShellNavHeader, "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("HX-Retarget"); got != "#ut-page" {
		t.Errorf("HX-Retarget = %q", got)
	}
	if got := w.Header().Get("HX-Reselect"); got != "#ut-page" {
		t.Errorf("HX-Reselect = %q", got)
	}
	if got := w.Header().Get("HX-Reswap"); got != BoostedSwap {
		t.Errorf("HX-Reswap = %q, want %q", got, BoostedSwap)
	}
	if got := w.Header().Values("Vary"); len(got) != 1 || got[0] != ShellNavHeader {
		t.Errorf("Vary = %q, want [%s]", got, ShellNavHeader)
	}
}

// Page handlers mostly rely on net/http's Content-Type sniffing at the first
// Write — the middleware must see HTML in that case too.
func TestBoostedNavigation_SniffsUnsetContentType(t *testing.T) {
	h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body><div id=\"ut-page\"></div></body></html>"))
	}))
	r := httptest.NewRequest("GET", "/menu", nil)
	r.Header.Set("HX-Boosted", "true")
	r.Header.Set(ShellNavHeader, "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("HX-Retarget"); got != "#ut-page" {
		t.Errorf("HX-Retarget = %q (Content-Type %q)", got, w.Header().Get("Content-Type"))
	}
}

// A boosted 403/404/500 whose body is a base.html error page (RenderError)
// is retargeted like any page: the error renders INSIDE the shell with the
// rail intact instead of htmx's default body-innerHTML swap on error.
func TestBoostedNavigation_RetargetsHTMLErrorPages(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write([]byte("<html><body><div id=\"ut-page\">nope</div></body></html>"))
		}))
		r := httptest.NewRequest("GET", "/admin", nil)
		r.Header.Set("HX-Boosted", "true")
		r.Header.Set(ShellNavHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("HX-Retarget"); got != "#ut-page" {
			t.Errorf("%d: HX-Retarget = %q", status, got)
		}
		if w.Code != status {
			t.Errorf("status %d rewritten to %d", status, w.Code)
		}
	}
}

// RenderError's exact shape: WriteHeader(status) first, Content-Type
// sniffed later by net/http from the template output. The status must be
// held until the first Write so the decision sees text/html.
func TestBoostedNavigation_RetargetsWhenStatusIsWrittenBeforeContentType(t *testing.T) {
	h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body><div id=\"ut-page\">gone</div></body></html>"))
	}))
	r := httptest.NewRequest("GET", "/kitchen-display/nope", nil)
	r.Header.Set("HX-Boosted", "true")
	r.Header.Set(ShellNavHeader, "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
	if got := w.Header().Get("HX-Retarget"); got != "#ut-page" {
		t.Errorf("HX-Retarget = %q", got)
	}
}

// A handler that writes a status and no body at all must still get that
// status out (a held-back WriteHeader that never commits would let
// net/http default to 200 at handler return).
func TestBoostedNavigation_HeldStatusIsSentWithoutABody(t *testing.T) {
	h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("HX-Boosted", "true")
	r.Header.Set(ShellNavHeader, "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", w.Code)
	}
	if got := w.Header().Get("HX-Retarget"); got != "" {
		t.Errorf("bodiless 403 got HX-Retarget %q", got)
	}
}

// The edges the held-status logic must not disturb: a redirect passes
// straight through, http.Error (text/plain set before WriteHeader) never
// gets retargeted, and a Flush before any Write commits the held status.
func TestBoostedNavigation_HeldStatusEdges(t *testing.T) {
	shellReq := func(path string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("HX-Boosted", "true")
		r.Header.Set(ShellNavHeader, "1")
		return r
	}
	t.Run("redirect passes through untouched", func(t *testing.T) {
		h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/menu", http.StatusSeeOther)
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, shellReq("/go"))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/menu" {
			t.Fatalf("redirect mangled: %d %q", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("http.Error is never retargeted", func(t *testing.T) {
		h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, shellReq("/x"))
		if w.Code != http.StatusInternalServerError || w.Header().Get("HX-Retarget") != "" {
			t.Fatalf("http.Error: %d retarget=%q", w.Code, w.Header().Get("HX-Retarget"))
		}
	})
	t.Run("Flush before any write commits the held status", func(t *testing.T) {
		h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.(http.Flusher).Flush()
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, shellReq("/x"))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503", w.Code)
		}
	})
}

func TestBoostedNavigation_LeavesEverythingElseAlone(t *testing.T) {
	cases := []struct {
		name    string
		boosted bool
		shell   bool
		ctype   string
		status  int
	}{
		{"plain browser GET", false, false, "text/html; charset=utf-8", 200},
		{"ordinary htmx fragment request", false, false, "text/html; charset=utf-8", 200},
		// The list-and-dialog pattern: a form with its own hx-boost +
		// hx-target. htmx marks it boosted, but it is not a shell navigation
		// and its refusal fragment must land in its own message slot.
		{"boosted dialog form without the shell header", true, false, "text/html; charset=utf-8", 200},
		{"boosted but JSON", true, true, "application/json", 200},
		{"boosted 204", true, true, "text/html; charset=utf-8", 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := BoostedNavigation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("x"))
			}))
			r := httptest.NewRequest("GET", "/menu", nil)
			r.Header.Set("HX-Request", "true")
			if tc.boosted {
				r.Header.Set("HX-Boosted", "true")
			}
			if tc.shell {
				r.Header.Set(ShellNavHeader, "1")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			for _, k := range []string{"HX-Retarget", "HX-Reselect", "HX-Reswap"} {
				if got := w.Header().Get(k); got != "" {
					t.Errorf("%s = %q, want unset", k, got)
				}
			}
		})
	}
}

// A page handler that serves a bare "content" fragment to htmx must NOT do
// so for a boosted navigation — the client needs the whole document (its
// #ut-page and the shell signature). Otherwise every such page would fall
// back to a full load: two requests, none of the benefit.
func TestIsFragmentSwap_BoostedNavigationIsNotAFragment(t *testing.T) {
	r := httptest.NewRequest("GET", "/catalog", nil)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Boosted", "true")
	r.Header.Set(ShellNavHeader, "1")
	if IsFragmentSwap(httptest.NewRecorder(), r) {
		t.Error("boosted navigation reported as a fragment swap")
	}
}
