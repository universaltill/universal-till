package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#2913 slice 1: a report-only Content-Security-Policy plus a
// violation sink, behind UT_CSP_REPORT_ONLY, to inventory what an enforced
// policy would break before any slice enforces one.

const wantCSPReportOnly = "default-src 'self'; script-src 'self' 'report-sample' 'wasm-unsafe-eval'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; report-uri /csp-report"

// syncBuffer is a bytes.Buffer safe for logging.CaptureForTest.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func cspTestMux(store *cspReportStore) *http.ServeMux {
	mux := http.NewServeMux()
	registerCSPReport(mux, store, func(*http.Request) bool { return true })
	return mux
}

func postCSP(t *testing.T, h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/csp-report", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type cspInventory struct {
	Data  []cspEntryJSON `json:"data"`
	Error any            `json:"error"`
}

type cspEntryJSON struct {
	EffectiveDirective string `json:"effective_directive"`
	BlockedURI         string `json:"blocked_uri"`
	DocumentPath       string `json:"document_path"`
	SourceFile         string `json:"source_file"`
	Line               int    `json:"line"`
	Sample             string `json:"sample"`
	Count              int    `json:"count"`
}

func getInventory(t *testing.T, h http.Handler) cspInventory {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/csp-report", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /csp-report status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET /csp-report Content-Type = %q", ct)
	}
	var inv cspInventory
	raw := rec.Body.Bytes()
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("decode inventory: %v; body %s", err, raw)
	}
	// The envelope must carry an explicit "error": null, and the data
	// array must be an array even when empty.
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(raw, &envelope)
	if string(envelope["error"]) != "null" {
		t.Fatalf(`inventory "error" = %s, want null`, envelope["error"])
	}
	if !strings.HasPrefix(string(envelope["data"]), "[") {
		t.Fatalf(`inventory "data" = %s, want a JSON array`, envelope["data"])
	}
	return inv
}

const legacyReport = `{"csp-report":{
	"document-uri":"http://127.0.0.1:8096/items?session=secret#frag",
	"referrer":"",
	"violated-directive":"script-src-elem",
	"effective-directive":"script-src-elem",
	"original-policy":"default-src 'self'",
	"blocked-uri":"inline",
	"source-file":"http://127.0.0.1:8096/items",
	"line-number":42,
	"column-number":7,
	"script-sample":"htmx.process(document.body)",
	"status-code":200}}`

const reportsJSON = `[
	{"type":"csp-violation","age":1,"url":"http://127.0.0.1:8096/settings?x=1","user_agent":"t",
	 "body":{"documentURL":"http://127.0.0.1:8096/settings?x=1","effectiveDirective":"style-src-attr",
	 "blockedURL":"inline","sourceFile":"http://127.0.0.1:8096/settings","lineNumber":3,"columnNumber":9,
	 "sample":"color:red","disposition":"report"}},
	{"type":"deprecation","url":"http://127.0.0.1:8096/","body":{"id":"x","message":"m"}}
]`

// The header goes on every response — HTML, JSON and 404 alike — and the
// middleware hands the handler the ResponseWriter it was given: never a
// wrapper, so SSE (http.Flusher) and Hijack keep working.
func TestCSPReportOnlyMiddleware_SetsHeaderOnEveryResponseWithoutWrapping(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
		ctype      string
	}{
		{"html", "/", 200, "text/html; charset=utf-8"},
		{"json", "/api/x", 200, "application/json"},
		{"notfound", "/nope", 404, "text/plain"},
		{"sse", "/api/orders/stream", 200, "text/event-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outer := httptest.NewRecorder()
			var got http.ResponseWriter
			h := cspReportOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = w
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
			}))
			h.ServeHTTP(outer, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if v := outer.Header().Get("Content-Security-Policy-Report-Only"); v != wantCSPReportOnly {
				t.Fatalf("Content-Security-Policy-Report-Only = %q, want %q", v, wantCSPReportOnly)
			}
			if got != http.ResponseWriter(outer) {
				t.Fatalf("handler got %T, want the original ResponseWriter unwrapped", got)
			}
			if outer.Header().Get("Content-Security-Policy") != "" {
				t.Fatal("report-only middleware must not set an enforced Content-Security-Policy")
			}
		})
	}
}

// Wired inside recoverMiddleware, the header survives a handler panic: the
// clean 500 recoverMiddleware writes still carries it.
func TestCSPReportOnlyMiddleware_ThroughRecoverMiddleware(t *testing.T) {
	h := recoverMiddleware(cspReportOnlyMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if v := rec.Header().Get("Content-Security-Policy-Report-Only"); v != wantCSPReportOnly {
		t.Fatalf("header on recovered 500 = %q, want the report-only policy", v)
	}
}

func TestCSPReport_LegacyFormatParsed(t *testing.T) {
	store := newCSPReportStore(cspReportMaxEntries)
	h := cspTestMux(store)
	if rec := postCSP(t, h, "application/csp-report", legacyReport); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	inv := getInventory(t, h)
	if len(inv.Data) != 1 {
		t.Fatalf("inventory = %+v, want 1 entry", inv.Data)
	}
	want := cspEntryJSON{
		EffectiveDirective: "script-src-elem",
		BlockedURI:         "inline",
		DocumentPath:       "/items",
		SourceFile:         "http://127.0.0.1:8096/items",
		Line:               42,
		Sample:             "htmx.process(document.body)",
		Count:              1,
	}
	if inv.Data[0] != want {
		t.Fatalf("entry = %+v, want %+v", inv.Data[0], want)
	}
}

// Older browsers send only violated-directive; its first token is the
// effective directive.
func TestCSPReport_LegacyFallsBackToViolatedDirective(t *testing.T) {
	store := newCSPReportStore(cspReportMaxEntries)
	h := cspTestMux(store)
	body := `{"csp-report":{"document-uri":"http://h/","violated-directive":"img-src 'self'","blocked-uri":"data"}}`
	if rec := postCSP(t, h, "application/csp-report", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	inv := getInventory(t, h)
	if len(inv.Data) != 1 || inv.Data[0].EffectiveDirective != "img-src" || inv.Data[0].DocumentPath != "/" {
		t.Fatalf("inventory = %+v, want one img-src entry at /", inv.Data)
	}
}

func TestCSPReport_ReportingAPIFormatParsed(t *testing.T) {
	store := newCSPReportStore(cspReportMaxEntries)
	h := cspTestMux(store)
	if rec := postCSP(t, h, "application/reports+json", reportsJSON); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	inv := getInventory(t, h)
	if len(inv.Data) != 1 {
		t.Fatalf("inventory = %+v, want exactly the csp-violation report (deprecation ignored)", inv.Data)
	}
	want := cspEntryJSON{
		EffectiveDirective: "style-src-attr",
		BlockedURI:         "inline",
		DocumentPath:       "/settings",
		SourceFile:         "http://127.0.0.1:8096/settings",
		Line:               3,
		Sample:             "color:red",
		Count:              1,
	}
	if inv.Data[0] != want {
		t.Fatalf("entry = %+v, want %+v", inv.Data[0], want)
	}
}

// Content-Type parameters (charset) are tolerated.
func TestCSPReport_ContentTypeParametersAccepted(t *testing.T) {
	h := cspTestMux(newCSPReportStore(cspReportMaxEntries))
	if rec := postCSP(t, h, "application/csp-report; charset=utf-8", legacyReport); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// The same violation reported again bumps the count, never adds a row;
// a different line is a different row. Order is deterministic.
func TestCSPReport_DedupeAndCount(t *testing.T) {
	store := newCSPReportStore(cspReportMaxEntries)
	h := cspTestMux(store)
	for range 3 {
		postCSP(t, h, "application/csp-report", legacyReport)
	}
	// Same key, but via the other format and a different query string.
	postCSP(t, h, "application/reports+json", `[{"type":"csp-violation","url":"http://127.0.0.1:8096/items?other=1",
		"body":{"documentURL":"http://127.0.0.1:8096/items?other=1","effectiveDirective":"script-src-elem",
		"blockedURL":"inline","sourceFile":"http://127.0.0.1:8096/items","lineNumber":42,"columnNumber":99}}]`)
	// Different line -> new entry.
	postCSP(t, h, "application/csp-report", strings.Replace(legacyReport, `"line-number":42`, `"line-number":43`, 1))
	// Different directive, sorts first.
	postCSP(t, h, "application/csp-report", strings.Replace(legacyReport, `"effective-directive":"script-src-elem"`, `"effective-directive":"img-src"`, 1))

	inv := getInventory(t, h)
	if len(inv.Data) != 3 {
		t.Fatalf("inventory has %d entries, want 3: %+v", len(inv.Data), inv.Data)
	}
	got := []string{}
	for _, e := range inv.Data {
		got = append(got, fmt.Sprintf("%s:%d=%d", e.EffectiveDirective, e.Line, e.Count))
	}
	want := []string{"img-src:42=1", "script-src-elem:42=4", "script-src-elem:43=1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("inventory = %v, want %v", got, want)
	}
	again := getInventory(t, h)
	for i := range inv.Data {
		if inv.Data[i] != again.Data[i] {
			t.Fatalf("inventory order is not deterministic: %+v vs %+v", inv.Data, again.Data)
		}
	}
}

// Past the cap new unique violations are dropped (not stored), while
// already-known ones keep counting.
func TestCSPReport_CapBoundsUniqueEntries(t *testing.T) {
	store := newCSPReportStore(3)
	h := cspTestMux(store)
	for i := range 10 {
		body := strings.Replace(legacyReport, `"line-number":42`, fmt.Sprintf(`"line-number":%d`, i+1), 1)
		if rec := postCSP(t, h, "application/csp-report", body); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d: status = %d, want 204 even past the cap", i, rec.Code)
		}
	}
	// A known one still counts.
	postCSP(t, h, "application/csp-report", strings.Replace(legacyReport, `"line-number":42`, `"line-number":1`, 1))
	inv := getInventory(t, h)
	if len(inv.Data) != 3 {
		t.Fatalf("inventory has %d entries, want the cap of 3", len(inv.Data))
	}
	if inv.Data[0].Line != 1 || inv.Data[0].Count != 2 {
		t.Fatalf("first entry = %+v, want line 1 counted twice", inv.Data[0])
	}
	if d := store.droppedCount(); d != 7 {
		t.Fatalf("dropped = %d, want 7", d)
	}
}

func TestCSPReport_ErrorStatuses(t *testing.T) {
	big := `{"csp-report":{"document-uri":"http://h/","effective-directive":"img-src","script-sample":"` +
		strings.Repeat("a", cspReportMaxBody) + `"}}`
	cases := []struct {
		name, method, ctype, body string
		want                      int
	}{
		{"too large", http.MethodPost, "application/csp-report", big, http.StatusRequestEntityTooLarge},
		{"malformed json", http.MethodPost, "application/csp-report", `{"csp-report":`, http.StatusBadRequest},
		{"empty body", http.MethodPost, "application/csp-report", ``, http.StatusBadRequest},
		{"missing csp-report key", http.MethodPost, "application/csp-report", `{"other":{}}`, http.StatusBadRequest},
		{"wrong shape legacy", http.MethodPost, "application/csp-report", `[1,2]`, http.StatusBadRequest},
		{"wrong shape reports", http.MethodPost, "application/reports+json", `{"type":"csp-violation"}`, http.StatusBadRequest},
		{"malformed reports", http.MethodPost, "application/reports+json", `[{`, http.StatusBadRequest},
		{"no directive", http.MethodPost, "application/csp-report", `{"csp-report":{"document-uri":"http://h/"}}`, http.StatusBadRequest},
		{"text/plain", http.MethodPost, "text/plain", legacyReport, http.StatusUnsupportedMediaType},
		{"application/json", http.MethodPost, "application/json", legacyReport, http.StatusUnsupportedMediaType},
		{"no content type", http.MethodPost, "", legacyReport, http.StatusUnsupportedMediaType},
		{"PUT", http.MethodPut, "application/csp-report", legacyReport, http.StatusMethodNotAllowed},
		{"DELETE", http.MethodDelete, "", "", http.StatusMethodNotAllowed},
		{"PATCH", http.MethodPatch, "application/csp-report", legacyReport, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newCSPReportStore(cspReportMaxEntries)
			h := cspTestMux(store)
			req := httptest.NewRequest(tc.method, "/csp-report", strings.NewReader(tc.body))
			if tc.ctype != "" {
				req.Header.Set("Content-Type", tc.ctype)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			if n := len(store.snapshot()); n != 0 {
				t.Fatalf("a rejected report was stored (%d entries)", n)
			}
		})
	}
}

// Control characters (log injection) are stripped, every field is capped,
// and query strings / fragments (which may carry tokens) never reach the
// inventory or the log.
func TestCSPReport_Sanitises(t *testing.T) {
	buf := &syncBuffer{}
	restore := logging.CaptureForTest(buf)
	t.Cleanup(restore)

	store := newCSPReportStore(cspReportMaxEntries)
	h := cspTestMux(store)
	long := strings.Repeat("é", 1000)
	report := map[string]any{"csp-report": map[string]any{
		"document-uri":        "http://h/sale/screen?token=SECRET1&x=1#frag\r\nfake",
		"effective-directive": "script-src\n2026-01-01T00:00:00Z [ERROR] forged",
		"blocked-uri":         "https://cdn.example/x.js?key=SECRET2",
		"source-file":         "http://h/public/app.js?v=SECRET3",
		"line-number":         5,
		"script-sample":       "a\x00b\x1bc\u2028d" + long,
	}}
	raw, _ := json.Marshal(report)
	if rec := postCSP(t, h, "application/csp-report", string(raw)); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	inv := getInventory(t, h)
	if len(inv.Data) != 1 {
		t.Fatalf("inventory = %+v", inv.Data)
	}
	e := inv.Data[0]
	if e.DocumentPath != "/sale/screen" {
		t.Errorf("document_path = %q, want /sale/screen (query and fragment dropped)", e.DocumentPath)
	}
	if e.BlockedURI != "https://cdn.example/x.js" {
		t.Errorf("blocked_uri = %q, want the query dropped", e.BlockedURI)
	}
	if e.SourceFile != "http://h/public/app.js" {
		t.Errorf("source_file = %q, want the query dropped", e.SourceFile)
	}
	if strings.ContainsAny(e.EffectiveDirective, "\r\n") || !strings.HasPrefix(e.EffectiveDirective, "script-src") {
		t.Errorf("effective_directive = %q, want control chars stripped", e.EffectiveDirective)
	}
	if !strings.HasPrefix(e.Sample, "abcd") {
		t.Errorf("sample = %q, want control/line-separator chars stripped", e.Sample[:20])
	}
	if n := utf8.RuneCountInString(e.Sample); n != cspReportMaxFieldRunes {
		t.Errorf("sample has %d runes, want truncated to %d", n, cspReportMaxFieldRunes)
	}
	logged := buf.String()
	for _, secret := range []string{"SECRET1", "SECRET2", "SECRET3", "forged\n", "\x1b"} {
		if strings.Contains(logged, secret) {
			t.Errorf("log contains %q:\n%s", secret, logged)
		}
	}
	// CaptureForTest is process-global; count only this sink's lines.
	if n := strings.Count(logged, "csp-report:"); n != 1 {
		t.Errorf("one violation must log exactly one csp-report: line, got %d:\n%s", n, logged)
	}
}

// Each new unique violation is logged once at INFO with a csp-report:
// prefix; repeats are only counted.
func TestCSPReport_LogsNewViolationOnce(t *testing.T) {
	buf := &syncBuffer{}
	restore := logging.CaptureForTest(buf)
	t.Cleanup(restore)

	h := cspTestMux(newCSPReportStore(cspReportMaxEntries))
	for range 5 {
		postCSP(t, h, "application/csp-report", legacyReport)
	}
	postCSP(t, h, "application/reports+json", reportsJSON)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, "csp-report:") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 csp-report log lines (one per unique violation), got %d:\n%s", len(lines), buf.String())
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "[INFO] csp-report:") {
			t.Errorf("log line %q: want INFO level with a csp-report: prefix", l)
		}
	}
	if !strings.Contains(lines[0], "script-src-elem") || !strings.Contains(lines[1], "style-src-attr") {
		t.Errorf("log lines don't name the directives: %v", lines)
	}
}

// Concurrent reports and reads are race-free (run with -race).
func TestCSPReport_ConcurrentSafe(t *testing.T) {
	store := newCSPReportStore(50)
	h := cspTestMux(store)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := strings.Replace(legacyReport, `"line-number":42`, fmt.Sprintf(`"line-number":%d`, i%5), 1)
			// No t.Fatalf off the test goroutine: hit the handlers directly.
			req := httptest.NewRequest(http.MethodPost, "/csp-report", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/csp-report")
			h.ServeHTTP(httptest.NewRecorder(), req)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/csp-report", nil))
		}()
	}
	wg.Wait()
	total := 0
	for _, e := range store.snapshot() {
		total += e.Count
	}
	if total != 20 {
		t.Fatalf("total count = %d, want 20", total)
	}
}

// Through the real pages.Init chain (auth ON): with the flag on, every
// response — including the anonymous login redirect — carries the header,
// anonymous POST /csp-report is accepted, and the GET inventory stays behind
// auth. With the flag off there is no header and no route.
func TestInit_CSPReportOnlyFlag(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("csp=%v", on), func(t *testing.T) {
			chdirRoot(t)
			paths.Init(t.TempDir())
			d := &db.DB{DB: openPagesTestDB(t)}
			defer d.Close()
			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}, CSPReportOnly: on}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			pm, err := plugins.Init(ctx, cfg, d.DB)
			if err != nil {
				t.Fatalf("plugins.Init: %v", err)
			}
			var wg sync.WaitGroup
			h, _ := Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
			t.Cleanup(func() { httpx.InitRailVisibility(nil) })

			for _, path := range []string{"/healthz", "/login", "/", "/settings"} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				got := rec.Header().Get("Content-Security-Policy-Report-Only")
				if on && got != wantCSPReportOnly {
					t.Errorf("flag on: GET %s (status %d) header = %q, want the report-only policy", path, rec.Code, got)
				}
				if !on && got != "" {
					t.Errorf("flag off: GET %s sent Content-Security-Policy-Report-Only %q", path, got)
				}
			}

			rec := postCSP(t, h, "application/csp-report", legacyReport)
			if on && rec.Code != http.StatusNoContent {
				t.Errorf("flag on: anonymous POST /csp-report = %d, want 204", rec.Code)
			}
			if !on && rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
				t.Errorf("flag off: anonymous POST /csp-report = %d, want auth's own 303/401 (no exemption)", rec.Code)
			}

			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/csp-report", nil))
			if rec.Code == http.StatusOK {
				t.Errorf("anonymous GET /csp-report = 200 — the inventory must stay behind auth")
			}
		})
	}
}

// With auth off (UT_AUTH=off, the e2e setup) the GET inventory is served
// through the real chain, and absent when the flag is off.
func TestInit_CSPReportOnlyFlag_AuthDisabled(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("csp=%v", on), func(t *testing.T) {
			// canPerform reads UT_AUTH itself, as on a real till where
			// cfg.AuthDisabled comes from the same variable.
			t.Setenv("UT_AUTH", "off")
			chdirRoot(t)
			paths.Init(t.TempDir())
			d := &db.DB{DB: openPagesTestDB(t)}
			defer d.Close()
			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}, CSPReportOnly: on, AuthDisabled: true}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			pm, err := plugins.Init(ctx, cfg, d.DB)
			if err != nil {
				t.Fatalf("plugins.Init: %v", err)
			}
			var wg sync.WaitGroup
			h, _ := Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
			t.Cleanup(func() { httpx.InitRailVisibility(nil) })

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if got := rec.Header().Get("Content-Security-Policy-Report-Only"); (got != "") != on {
				t.Fatalf("csp=%v: /healthz header = %q", on, got)
			}
			postCSP(t, h, "application/csp-report", legacyReport)
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/csp-report", nil))
			if !on {
				if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "effective_directive") {
					t.Fatalf("flag off: GET /csp-report served the inventory")
				}
				return
			}
			inv := getInventory(t, h)
			if len(inv.Data) != 1 || inv.Data[0].DocumentPath != "/items" {
				t.Fatalf("inventory through Init = %+v", inv.Data)
			}
		})
	}
}

// The inventory is settings-tier data: a session without the settings
// permission (a cashier) gets 403, not the page paths and script samples.
func TestCSPReport_InventoryNeedsReadPermission(t *testing.T) {
	store := newCSPReportStore(cspReportMaxEntries)
	mux := http.NewServeMux()
	registerCSPReport(mux, store, func(*http.Request) bool { return false })
	if rec := postCSP(t, mux, "application/csp-report", legacyReport); rec.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want 204", rec.Code)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/csp-report", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET status = %d, want 403; body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "script-src") {
		t.Fatalf("forbidden GET leaked the inventory: %s", rec.Body)
	}
}

// droppedCount lives in the test file: only tests read it (the deadcode
// baseline guard rejects a production method nothing calls).
func (s *cspReportStore) droppedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}
