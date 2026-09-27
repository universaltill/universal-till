package main

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
)

// TestBrowserFallback_OpensBrowserWiresNoOpOpsAndLogsReason is ut-docs#2761:
// when no native window can be created, the shell must open the till in the
// default browser, keep serving until the server stops, answer live toggles
// with a silent success (not 503), and leave a line in desktop.log saying
// why — the user previously saw the app "open and close" with nothing
// anywhere.
func TestBrowserFallback_OpensBrowserWiresNoOpOpsAndLogsReason(t *testing.T) {
	var logBuf syncBuffer
	restore := logging.CaptureForTest(&logBuf)
	defer restore()

	cs, err := newControlServer()
	if err != nil {
		t.Fatalf("newControlServer() = %v", err)
	}
	addr, token := cs.Addr(), cs.Token()

	var opened, waited string
	var statusDuringWait int
	browserFallback("http://127.0.0.1:8080", cs, "WebView2 init failed (HRESULT 0x8007139F)",
		func(u string) { opened = u },
		func(u string) {
			waited = u
			// While the fallback is "running", a live toggle must reach
			// the no-op ops (204), not ops==nil (503).
			resp := authedPost(t, addr, "/exit-to-os", token, "")
			statusDuringWait = resp.StatusCode
			resp.Body.Close()
		})

	if opened != "http://127.0.0.1:8080" || waited != "http://127.0.0.1:8080" {
		t.Fatalf("open/wait got %q/%q, want the till URL for both", opened, waited)
	}
	if statusDuringWait != http.StatusNoContent {
		t.Errorf("exit-to-os during fallback = %d, want %d (no-op ops wired)", statusDuringWait, http.StatusNoContent)
	}
	// browserFallback owns ctl.Close(): the listener is gone afterwards.
	if _, err := http.Get("http://" + addr + "/diagnostics"); err == nil {
		t.Errorf("control listener still accepting after browserFallback returned")
	}
	if got := logBuf.String(); !strings.Contains(got, "0x8007139F") || !strings.Contains(got, "default browser") {
		t.Errorf("fallback log line missing reason/browser: %q", got)
	}
}

// A nil ctl (control listener failed to bind) must not panic.
func TestBrowserFallback_NilControlServer(t *testing.T) {
	called := 0
	browserFallback("http://x", nil, "test", func(string) { called++ }, func(string) { called++ })
	if called != 2 {
		t.Fatalf("open+wait called %d times, want 2", called)
	}
}

func TestShellDataDir(t *testing.T) {
	cases := []struct{ dataEnv, def, want string }{
		{"", filepath.Join("C:", "Users", "u", "AppData", "Local", "UniversalTill"), filepath.Join("C:", "Users", "u", "AppData", "Local", "UniversalTill", "webview2")},
		{filepath.Join("D:", "till"), "ignored", filepath.Join("D:", "till", "webview2")},
	}
	for _, c := range cases {
		if got := filepath.Join(shellDataDir(c.dataEnv, c.def), "webview2"); got != c.want {
			t.Errorf("shellDataDir(%q, %q)/webview2 = %q, want %q", c.dataEnv, c.def, got, c.want)
		}
	}
}

func TestWebViewInitFailureReason(t *testing.T) {
	cases := []struct {
		hr      int32
		version string
		want    []string
	}{
		{int32(-2147019873) /* 0x8007139F */, "153.0.4234.48", []string{"0x8007139F", "153.0.4234.48"}},
		{0, "", []string{"unknown", "runtime version unknown"}},
	}
	for _, c := range cases {
		got := webViewInitFailureReason(c.hr, c.version)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("webViewInitFailureReason(%d, %q) = %q, missing %q", c.hr, c.version, got, w)
			}
		}
	}
}

// syncBuffer: logging.CaptureForTest requires a writer safe for concurrent use.
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
