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

// buildBigAuthorizeGuest compiles the wasip1 test fixture that answers any
// ".authorize" event with a large auth_token field (ut-docs#245) — same
// pattern as buildBigAskGuest.
func buildBigAuthorizeGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bigauthorize_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/bigauthorize_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// buildBigNestedAuthorizeGuest compiles the wasip1 test fixture that
// answers any ".authorize" event with a SMALL auth_token field NESTED under
// "provider" (ut-docs#384) -- see the guest's own doc comment for why the
// token is small rather than large, unlike buildBigAuthorizeGuest.
func buildBigNestedAuthorizeGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bignestedauthorize_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/bignestedauthorize_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// buildStringNestedAuthorizeGuest compiles the wasip1 test fixture that
// answers any ".authorize" event with a SMALL auth_token nested one level
// down inside a JSON-encoded *string* field (ut-docs#393), one encoding
// layer removed from buildBigNestedAuthorizeGuest's directly-nested object.
func buildStringNestedAuthorizeGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stringnestedauthorize_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/stringnestedauthorize_guest")
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

// TestHandleEvent_AuthorizeResultRedactedInRealLog is the true end-to-end
// proof for ut-docs#245, mirroring TestHandleEvent_AskResultRedactedInRealLog
// for ".authorize" events: a REAL compiled WASM module, run through the REAL
// wazero runtime and the REAL logging package, answers a payment-authorize
// event with a large auth_token field. Payment-authorization plugin
// responses are the most credential-adjacent plugin output in the system,
// and unit-testing only the redaction helper in isolation was shown (by the
// independent review of ut-docs#202) to leave a wiring gap that a pure
// string-transform test can't catch — this test observes real bytes written
// to the real log, so it fails unless HandleEvent's own call site routes
// ".authorize" through redaction too.
func TestHandleEvent_AuthorizeResultRedactedInRealLog(t *testing.T) {
	guest := buildBigAuthorizeGuest(t)
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.bigauthorize"
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load guest module: %v", err)
	}

	ev := Event{ID: "ev1", Type: "payment.demopay.authorize", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}

	var resp json.RawMessage
	var handleErr error
	logged := captureRealStdout(t, func() {
		resp, handleErr = w.HandleEvent(context.Background(), pluginID, ev)
	})
	if handleErr != nil {
		t.Fatalf("HandleEvent: %v", handleErr)
	}

	var parsed struct {
		AuthToken string `json:"auth_token"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse guest response %s: %v", resp, err)
	}
	if len(parsed.AuthToken) < 5000 {
		t.Fatalf("test setup: guest response too small to prove anything, got %d bytes", len(parsed.AuthToken))
	}

	// The REAL returned answer (what the payment flow would actually act on)
	// must be completely untouched by the logging fix.
	if !strings.Contains(string(resp), parsed.AuthToken) {
		t.Fatalf("HandleEvent's returned value was altered -- redaction must only affect the log, never the real plugin answer")
	}

	// The REAL log line, from the REAL logging package, must not.
	if strings.Contains(logged, parsed.AuthToken) {
		t.Fatalf("the real process log still contains the full auth_token payload verbatim:\n%s", logged)
	}
	if !strings.Contains(logged, "payment.demopay.authorize") {
		t.Errorf("expected the event type in the real log line for debuggability, got:\n%s", logged)
	}
	if !strings.Contains(logged, "omitted") {
		t.Errorf("expected a size placeholder in the real log line, got:\n%s", logged)
	}
}

// TestHandleEvent_NestedAuthorizeTokenRedactedInRealLog is the true
// end-to-end proof for ut-docs#384: a REAL compiled WASM module answers a
// payment-authorize event with a SMALL auth_token NESTED under "provider"
// -- the shape a real gateway SDK commonly uses, and exactly the shape
// safeAskResultForLog's pre-fix top-level-only field check missed. Mirrors
// TestHandleEvent_AuthorizeResultRedactedInRealLog, one level deeper; per
// that test's own precedent, a helper-only unit test isn't enough to prove
// HandleEvent's real call site actually redacts nested fields.
//
// The token is deliberately SMALL, unlike the sibling large-payload E2E
// tests above -- independent review of this fix found that a large nested
// token makes the *parent* "provider" field's raw JSON exceed
// maxAskFieldBytes on its own, so the pre-existing top-level size check
// would redact it wholesale regardless of whether nested-by-name redaction
// worked at all. That made an earlier version of this test pass unchanged
// against the pre-fix, unpatched code -- a vacuous regression test that
// looked like it proved the fix while actually exercising a code path this
// fix didn't touch. A small token forces the test through the actual
// nested-by-NAME path ut-docs#384 adds.
func TestHandleEvent_NestedAuthorizeTokenRedactedInRealLog(t *testing.T) {
	guest := buildBigNestedAuthorizeGuest(t)
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.bignestedauthorize"
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load guest module: %v", err)
	}

	ev := Event{ID: "ev1", Type: "payment.demopay.authorize", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}

	var resp json.RawMessage
	var handleErr error
	logged := captureRealStdout(t, func() {
		resp, handleErr = w.HandleEvent(context.Background(), pluginID, ev)
	})
	if handleErr != nil {
		t.Fatalf("HandleEvent: %v", handleErr)
	}

	var parsed struct {
		Provider struct {
			AuthToken string `json:"auth_token"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse guest response %s: %v", resp, err)
	}
	if parsed.Provider.AuthToken == "" {
		t.Fatalf("test setup: guest response missing auth_token, got %s", resp)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(resp, &topLevel); err != nil {
		t.Fatalf("test setup: could not measure raw provider field size: %v", err)
	}
	if rawProvider := len(topLevel["provider"]); rawProvider > maxAskFieldBytes {
		t.Fatalf("test setup: nested provider field is %d bytes, over maxAskFieldBytes (%d) -- this would make the pre-existing top-level size check redact it wholesale regardless of nested-by-name redaction, silently making this test vacuous again (see ut-docs#384 review)", rawProvider, maxAskFieldBytes)
	}

	// The REAL returned answer (what the payment flow would actually act on)
	// must be completely untouched by the logging fix.
	if !strings.Contains(string(resp), parsed.Provider.AuthToken) {
		t.Fatalf("HandleEvent's returned value was altered -- redaction must only affect the log, never the real plugin answer")
	}

	// The REAL log line, from the REAL logging package, must not -- this is
	// the exact gap ut-docs#384 reported: pre-fix, the nested token reached
	// the log verbatim because only top-level fields were inspected.
	if strings.Contains(logged, parsed.Provider.AuthToken) {
		t.Fatalf("the real process log still contains the full nested auth_token payload verbatim:\n%s", logged)
	}
	if !strings.Contains(logged, "payment.demopay.authorize") {
		t.Errorf("expected the event type in the real log line for debuggability, got:\n%s", logged)
	}
	if !strings.Contains(logged, "omitted") {
		t.Errorf("expected a size placeholder in the real log line, got:\n%s", logged)
	}
}

// TestHandleEvent_StringEmbeddedNestedAuthorizeTokenRedactedInRealLog is the
// true end-to-end proof for ut-docs#393: a REAL compiled WASM module answers
// a payment-authorize event with a SMALL auth_token nested inside a
// JSON-encoded *string* field ({"provider":"{\"auth_token\":\"…\"}"}) --
// one encoding layer removed from TestHandleEvent_NestedAuthorizeTokenRedactedInRealLog's
// directly-nested object, and exactly the shape ut-docs#384's fix didn't
// handle (json.Unmarshal into a map fails for a string value, so recursion
// never triggered). Per that test's own precedent, a helper-only unit test
// isn't enough to prove HandleEvent's real call site redacts this shape too.
func TestHandleEvent_StringEmbeddedNestedAuthorizeTokenRedactedInRealLog(t *testing.T) {
	guest := buildStringNestedAuthorizeGuest(t)
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.stringnestedauthorize"
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load guest module: %v", err)
	}

	ev := Event{ID: "ev1", Type: "payment.demopay.authorize", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}

	var resp json.RawMessage
	var handleErr error
	logged := captureRealStdout(t, func() {
		resp, handleErr = w.HandleEvent(context.Background(), pluginID, ev)
	})
	if handleErr != nil {
		t.Fatalf("HandleEvent: %v", handleErr)
	}

	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(resp, &topLevel); err != nil {
		t.Fatalf("parse guest response %s: %v", resp, err)
	}
	var providerStr string
	if err := json.Unmarshal(topLevel["provider"], &providerStr); err != nil {
		t.Fatalf("test setup: guest's \"provider\" field isn't a JSON string: %v\n%s", err, resp)
	}
	var providerInner struct {
		AuthToken string `json:"auth_token"`
	}
	if err := json.Unmarshal([]byte(providerStr), &providerInner); err != nil {
		t.Fatalf("test setup: guest's \"provider\" string content isn't the expected JSON object: %v\n%s", err, providerStr)
	}
	if providerInner.AuthToken == "" {
		t.Fatalf("test setup: guest response missing embedded auth_token, got %s", resp)
	}
	if rawProvider := len(topLevel["provider"]); rawProvider > maxAskFieldBytes {
		t.Fatalf("test setup: provider field is %d bytes, over maxAskFieldBytes (%d) -- this would make the pre-existing top-level size check redact it wholesale regardless of the embedded-JSON-string path, silently making this test vacuous again (see ut-docs#384's review precedent)", rawProvider, maxAskFieldBytes)
	}

	// The REAL returned answer (what the payment flow would actually act on)
	// must be completely untouched by the logging fix.
	if !strings.Contains(string(resp), providerInner.AuthToken) {
		t.Fatalf("HandleEvent's returned value was altered -- redaction must only affect the log, never the real plugin answer")
	}

	// The REAL log line, from the REAL logging package, must not -- this is
	// the exact gap ut-docs#393 reported: pre-fix, a token nested inside an
	// embedded JSON string reached the log verbatim because redactField only
	// recursed into directly-nested objects/arrays, not a string whose own
	// content happened to be JSON.
	if strings.Contains(logged, providerInner.AuthToken) {
		t.Fatalf("the real process log still contains the full embedded-JSON-string auth_token verbatim:\n%s", logged)
	}
	if !strings.Contains(logged, "payment.demopay.authorize") {
		t.Errorf("expected the event type in the real log line for debuggability, got:\n%s", logged)
	}
	if !strings.Contains(logged, "omitted") {
		t.Errorf("expected a size placeholder in the real log line, got:\n%s", logged)
	}
}
