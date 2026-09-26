package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2788: POST /api/diag/reload-reason turns a whole-page reload the
// browser just did into one server log line naming the cause.

func postReloadReason(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/diag/reload-reason", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	var buf lockedBuffer
	t.Cleanup(logging.CaptureForTest(&buf))
	return &buf
}

func TestReloadReason_RouteRegistered(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	registerReloadReason(mux)
	rec := postReloadReason(mux, `{"reason":"settings-save","path":"/settings","nav_type":"reload"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), `page reload: reason="settings-save" path="/settings" nav="reload"`) {
		t.Fatalf("log line missing, got %q", buf.String())
	}
}

func TestReloadReason_ValidBodies(t *testing.T) {
	buf := captureLog(t)
	h := newReloadReasonHandler(time.Now)
	cases := []struct{ body, want string }{
		{`{"reason":"hx-refresh:/api/pos/basket:poll","path":"/pos","nav_type":"reload"}`,
			`reason="hx-refresh:/api/pos/basket:poll" path="/pos" nav="reload"`},
		{`{"reason":"unattributed","path":"/","nav_type":""}`,
			`reason="unattributed" path="/" nav=""`},
		// Query strings are stripped: they may carry tokens/ids and are
		// never needed to name a reload's cause.
		{`{"reason":"shell-fallback:signature","path":"/items?q=secret","nav_type":"navigate"}`,
			`reason="shell-fallback:signature" path="/items" nav="navigate"`},
		{`{"reason":"update-restarted","path":"/","nav_type":"back_forward"}`,
			`nav="back_forward"`},
		{`{"reason":"x","path":"/","nav_type":"prerender"}`, `nav="prerender"`},
	}
	for _, c := range cases {
		rec := postReloadReason(h, c.body)
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s: status %d, want 204 (%s)", c.body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(buf.String(), c.want) {
			t.Errorf("%s: log missing %q; got %q", c.body, c.want, buf.String())
		}
	}
}

func TestReloadReason_RejectsBadInput(t *testing.T) {
	buf := captureLog(t)
	h := newReloadReasonHandler(time.Now)
	bad := map[string]string{
		"uppercase reason":       `{"reason":"Settings","path":"/","nav_type":"reload"}`,
		"newline in reason":      `{"reason":"a\nINFO fake line","path":"/","nav_type":"reload"}`,
		"quote in reason":        `{"reason":"a\"b","path":"/","nav_type":"reload"}`,
		"empty reason":           `{"reason":"","path":"/","nav_type":"reload"}`,
		"reason too long":        `{"reason":"` + strings.Repeat("a", 121) + `","path":"/","nav_type":"reload"}`,
		"path not absolute":      `{"reason":"x","path":"settings","nav_type":"reload"}`,
		"path absolute URL":      `{"reason":"x","path":"http://evil/","nav_type":"reload"}`,
		"protocol-relative path": `{"reason":"x","path":"//evil/x","nav_type":"reload"}`,
		"path too long":          `{"reason":"x","path":"/` + strings.Repeat("a", 200) + `","nav_type":"reload"}`,
		"control char in path":   `{"reason":"x","path":"/a\u0007b","nav_type":"reload"}`,
		"unknown nav_type":       `{"reason":"x","path":"/","nav_type":"teleport"}`,
		"not JSON":               `reason=x`,
	}
	for name, body := range bad {
		rec := postReloadReason(h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		var env struct {
			Data  any    `json:"data"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data != nil || env.Error == "" {
			t.Errorf("%s: want {data:null,error:…} envelope, got %q", name, rec.Body.String())
		}
	}
	if strings.Contains(buf.String(), "page reload:") {
		t.Fatalf("a refused body must not be logged; got %q", buf.String())
	}
}

func TestReloadReason_OversizedBodyRefused(t *testing.T) {
	buf := captureLog(t)
	h := newReloadReasonHandler(time.Now)
	body := `{"reason":"x","path":"/","nav_type":"reload","pad":"` + strings.Repeat("a", 4096) + `"}`
	rec := postReloadReason(h, body)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status %d, want 4xx", rec.Code)
	}
	if strings.Contains(buf.String(), "page reload:") {
		t.Fatalf("oversized body must not be logged; got %q", buf.String())
	}
}

// A reload loop must not be able to flood the till log: at most
// reloadReasonMaxPerWindow lines per window, one "dropping" notice, then
// logging resumes in the next window.
func TestReloadReason_RateCap(t *testing.T) {
	buf := captureLog(t)
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	h := newReloadReasonHandler(func() time.Time { return now })
	body := `{"reason":"loop","path":"/","nav_type":"reload"}`
	for i := 0; i < reloadReasonMaxPerWindow+10; i++ {
		if rec := postReloadReason(h, body); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: status %d, want 204 even when capped", i, rec.Code)
		}
	}
	out := buf.String()
	if got := strings.Count(out, `page reload: reason="loop"`); got != reloadReasonMaxPerWindow {
		t.Fatalf("logged %d lines in one window, want %d", got, reloadReasonMaxPerWindow)
	}
	if got := strings.Count(out, "page reload: rate cap"); got != 1 {
		t.Fatalf("want exactly one rate-cap notice, got %d in %q", got, out)
	}

	now = now.Add(reloadReasonWindow + time.Second)
	postReloadReason(h, body)
	if got := strings.Count(buf.String(), `page reload: reason="loop"`); got != reloadReasonMaxPerWindow+1 {
		t.Fatalf("logging did not resume in the next window (count %d)", got)
	}
}

// Every whole-page reload a base.html-rendered template triggers must go
// through UT.reload(reason), or the till log can't name it. The only bare
// location.reload() left in those templates is UT.reload's own body.
// Standalone documents (no base.html: self_order.html, login, setup, …)
// don't have UT.reload and are skipped.
// bareReload catches the usual spellings of "reload this page" that would
// bypass UT.reload: location.reload(…), history.go(0), and re-assigning the
// current URL.
var bareReload = regexp.MustCompile(`location\.reload\(|history\.go\(0\)|location\.(href|assign\()\s*(=\s*)?\(?\s*(window\.)?location\.href`)

func TestBaseRenderedTemplatesReloadWithAReason(t *testing.T) {
	var files []string
	for _, g := range []string{"web/ui/pages/*.html", "web/ui/partials/*.html"} {
		m, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no templates found (wrong working directory?)")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(strings.ToLower(s[:min(len(s), 64)]), "<!doctype") {
			continue // standalone document, no UT.reload
		}
		if bareReload.MatchString(s) {
			t.Errorf("%s: bare whole-page reload (%q); use UT.reload('<reason>') (ut-docs#2788)", f, bareReload.FindString(s))
		}
	}
	base, err := os.ReadFile("web/ui/layouts/base.html")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(base), "location.reload()"); got != 1 {
		t.Errorf("base.html: %d bare location.reload() calls, want exactly 1 (UT.reload's own body)", got)
	}
	for _, want := range []string{"UT.reload = function", "UT.noteNav = function", "htmx:beforeOnLoad", "/api/diag/reload-reason", "'ut-reload-reason'"} {
		if !strings.Contains(string(base), want) {
			t.Errorf("base.html: missing %q", want)
		}
	}
}
