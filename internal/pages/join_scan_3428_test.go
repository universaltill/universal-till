package pages

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/web"
	"github.com/universaltill/universal-till/web/locales"
)

// ut-docs#3428: the "Scan QR code" button on both join screens. The scan only
// fills the existing join form (fill-then-confirm, UX verdict), so these pin
// the markup, the English strings and that the server side is unchanged.

var joinScanIDs = map[string][]string{
	"/setup": {"setup-join-scan", "setup-join-scan-overlay", "setup-join-scan-video", "setup-join-scan-status",
		"setup-join-scan-close", "setup-join-scan-type", "setup-join-scanned"},
	"/tills": {"tills-join-scan", "tills-join-scan-overlay", "tills-join-scan-video", "tills-join-scan-status",
		"tills-join-scan-close", "tills-join-scan-type", "tills-join-scanned"},
}

func joinScanPage(t *testing.T, path string) string {
	t.Helper()
	var h http.Handler
	if path == "/setup" {
		withOSLocale(t, "", "")
		mux, _, _ := newFullAuthDeps(t)
		h = mux
	} else {
		t.Setenv("UT_AUTH", "off")
		mux, _ := newSyncAPITestDeps(t)
		h = mux
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, rec.Code)
	}
	return rec.Body.String()
}

func assertJoinScanMarkup(t *testing.T, path string) {
	t.Helper()
	body := joinScanPage(t, path)
	for _, id := range joinScanIDs[path] {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("%s: missing element id=%q", path, id)
		}
	}
	btn := regexp.MustCompile(`<button[^>]*data-join-scan[^>]*>`).FindString(body)
	for _, want := range []string{`type="button"`, `class="btn secondary"`, `data-testid="join-scan-btn"`} {
		if !strings.Contains(btn, want) {
			t.Errorf("%s: scan button %q lacks %s", path, btn, want)
		}
	}
	ov := regexp.MustCompile(`<div[^>]*-join-scan-overlay"[^>]*>`).FindString(body)
	for _, want := range []string{`role="dialog"`, `aria-modal="true"`, `data-decoder-v="`,
		`data-msg-permission-denied="`, `data-msg-no-camera="`, `data-msg-busy="`, `data-msg-not-ut="`,
		`data-msg-scanning="`, `data-msg-scanned-next="`} {
		if !strings.Contains(ov, want) {
			t.Errorf("%s: overlay lacks %s: %s", path, want, ov)
		}
	}
	if regexp.MustCompile(`data-msg-[a-z-]+=""`).MatchString(ov) {
		t.Errorf("%s: overlay has an empty data-msg-*: %s", path, ov)
	}
	// Button sits above the code field.
	if bi, ci := strings.Index(body, "data-join-scan"), strings.Index(body, `name="code"`); bi < 0 || ci < 0 || bi > ci {
		t.Errorf("%s: scan button must precede input[name=code] (btn=%d code=%d)", path, bi, ci)
	}
	// The scanned host goes in via textContent: no hardcoded English, no token slot.
	if !regexp.MustCompile(`<script[^>]*src="[^"]*qr-scan\.js`).MatchString(body) {
		t.Errorf("%s: qr-scan.js not loaded", path)
	}
	if !regexp.MustCompile(`<script[^>]*src="[^"]*join-scan\.js`).MatchString(body) {
		t.Errorf("%s: join-scan.js not loaded", path)
	}
}

func TestSetupJoinFormHasScanButton(t *testing.T) { assertJoinScanMarkup(t, "/setup") }
func TestTillsJoinFormHasScanButton(t *testing.T) {
	assertJoinScanMarkup(t, "/tills?tab=add&need=join")
}

func TestJoinScanStringsInEnglishLocale(t *testing.T) {
	raw, err := locales.FS.ReadFile("en.json")
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"scan.pair.button":       "Scan QR code",
		"scan.pair.title":        "Scan the main till's QR code",
		"scan.pair.type_instead": "Type the code instead",
		"scan.pair.close":        "Cancel",
		"scan.pair.scanned_from": "Code scanned from %s",
		"scan.pair.scanned_next": "Code scanned. Enter a till name and press Join.",
		"tills.join_scan.clear":  "Clear scanned code",
		"scan.pair.no_camera":    "No camera is available here. Type the code and the main till's address instead.",
	}
	for k, v := range want {
		if flat[k] != v {
			t.Errorf("en.json[%q] = %q, want %q", k, flat[k], v)
		}
	}
	for _, k := range []string{"scan.pair.scanning", "scan.pair.permission_denied", "scan.pair.not_ut_code"} {
		if strings.TrimSpace(flat[k]) == "" {
			t.Errorf("en.json missing %q", k)
		}
	}
}

// A QR from another shop is, to this till, a long code the main till does not
// know: it must get exactly the refusal a typed wrong long code gets (R4).
func TestJoinForeignShopQRSameRefusal_3428(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	other, _ := newSyncDepsWithPath(t, "other.db")
	omux := http.NewServeMux()
	registerSyncAPI(omux, other)
	_, _, foreignLong := issueShortCode(t, omux, srv.URL) // token the main till never minted

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	join := func(code string) (int, string) {
		form := url.Values{"code": {code}, "name": {"Till 2"}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sync/join", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rmux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	typedCode, typedBody := join(encodeEnrollCode(srv.URL, strings.Repeat("0", 32)))
	scanCode, scanBody := join(foreignLong)
	if typedCode != scanCode || typedBody != scanBody {
		t.Fatalf("foreign QR refusal differs from a typed wrong long code:\n typed %d %q\n scan  %d %q", typedCode, typedBody, scanCode, scanBody)
	}
	if typedCode == http.StatusOK {
		t.Fatal("a token the main till never minted must be refused")
	}
}

func TestJoinNoTokenInLogs_3428(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	buf := &bytes.Buffer{}
	t.Cleanup(logging.CaptureForTest(buf))
	token := "0123456789abcdef0123456789abcdef"
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	for _, u := range []string{"http://127.0.0.1:1", "http://127.0.0.1:2"} {
		form := url.Values{"code": {encodeEnrollCode(u, token)}, "name": {"Till 2"}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sync/join", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rmux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatal("join to a dead address must fail")
		}
	}
	if strings.Contains(buf.String(), token) || strings.Contains(buf.String(), encodeEnrollCode("http://127.0.0.1:1", token)) {
		t.Fatalf("one-time token leaked into the log: %s", buf.String())
	}
}

func TestJoinScanScriptsEmbedded_3428(t *testing.T) {
	for _, f := range []string{"public/qr-scan.js", "public/join-scan.js"} {
		b, err := web.FS.ReadFile(f)
		if err != nil || len(b) == 0 {
			t.Errorf("%s not embedded: %v", f, err)
			continue
		}
		s := string(b)
		if regexp.MustCompile(`console\.(log|info|warn|error|debug)`).MatchString(s) {
			t.Errorf("%s must not use console.* (a scan value must never be logged)", f)
		}
		if strings.Contains(s, "innerHTML") || strings.Contains(s, "eval(") {
			t.Errorf("%s must not use innerHTML/eval on scan data", f)
		}
	}
}
