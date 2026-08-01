package plugins

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildBigAskGuest compiles the wasip1 test fixture that answers any
// ".ask" event with a large content_b64 field — same pattern as
// buildExportGuest/buildHostfnGuest.
func buildBigAskGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bigask_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/bigask_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// captureRealStdout redirects the process's real fd 1 (not just the Go
// os.Stdout variable) to a pipe for the duration of fn, and returns
// everything written to it. logging.go binds its *log.Logger to os.Stdout
// once, inside a sync.Once, well before this test runs -- reassigning the
// os.Stdout package variable afterward would NOT affect where that already-
// constructed logger writes, since it holds the *os.File it captured at
// Init time. Redirecting the underlying OS file descriptor does affect it,
// because writes go through the fd number, not the Go-level variable.
func captureRealStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	savedFD, err := syscall.Dup(1)
	if err != nil {
		t.Fatalf("dup original stdout fd: %v", err)
	}
	if err := syscall.Dup2(int(w.Fd()), 1); err != nil {
		t.Fatalf("dup2 pipe onto stdout fd: %v", err)
	}

	fn()

	w.Close()
	if err := syscall.Dup2(savedFD, 1); err != nil {
		t.Fatalf("restore original stdout fd: %v", err)
	}
	syscall.Close(savedFD)

	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

// TestHandleEvent_AskResultRedactedInRealLog is the true end-to-end proof
// for ut-docs#202: a REAL compiled WASM module, run through the REAL
// wazero runtime and the REAL logging package (actual process stdout, not
// a mock), answers an ".ask" event with a large content_b64 field. The
// independent review of this fix found that unit-testing only the
// redaction helper (or even the extracted formatting function) in
// isolation leaves a gap -- reverting HandleEvent's actual call site could
// silently reintroduce the original bug with the rest of the suite green.
// This test observes real bytes written to the real log, so it fails
// unless HandleEvent's own code path -- not just a helper it happens to
// have nearby -- keeps the payload out.
func TestHandleEvent_AskResultRedactedInRealLog(t *testing.T) {
	guest := buildBigAskGuest(t)
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.bigask"
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load guest module: %v", err)
	}

	ev := Event{ID: "ev1", Type: "export.requested.ask", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}

	var resp json.RawMessage
	var handleErr error
	logged := captureRealStdout(t, func() {
		resp, handleErr = w.HandleEvent(context.Background(), pluginID, ev)
	})
	if handleErr != nil {
		t.Fatalf("HandleEvent: %v", handleErr)
	}

	// The base64 encoding of 5000 'A' bytes -- what a real leak would look
	// like character-for-character in the log.
	var parsed struct {
		ContentB64 string `json:"content_b64"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse guest response %s: %v", resp, err)
	}
	if len(parsed.ContentB64) < 5000 {
		t.Fatalf("test setup: guest response too small to prove anything, got %d bytes", len(parsed.ContentB64))
	}

	// The REAL returned answer (what data_api.go would actually act on)
	// must be completely untouched by the logging fix.
	if !strings.Contains(string(resp), parsed.ContentB64) {
		t.Fatalf("HandleEvent's returned value was altered -- redaction must only affect the log, never the real plugin answer")
	}

	// The REAL log line, from the REAL logging package, must not.
	if strings.Contains(logged, parsed.ContentB64) {
		t.Fatalf("the real process log still contains the full content_b64 payload verbatim:\n%s", logged)
	}
	if !strings.Contains(logged, "export.requested.ask") {
		t.Errorf("expected the event type in the real log line for debuggability, got:\n%s", logged)
	}
	if !strings.Contains(logged, "omitted") {
		t.Errorf("expected a size placeholder in the real log line, got:\n%s", logged)
	}
}
