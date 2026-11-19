package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildExitUnknownGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "exitunknown_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/exitunknown_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// ut-docs#6600: a non-zero exit on a payment authorize must still hand the
// plugin's stdout (decline_code) to core.
func TestHandleEvent_AuthorizeNonZeroExitKeepsStdout(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.exitunknown"
	if err := w.load(pluginID, "1.0.0", buildExitUnknownGuest(t)); err != nil {
		t.Fatal(err)
	}
	ev := Event{ID: "e1", Type: "payment.demopay.authorize", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}
	_, err := w.HandleEvent(context.Background(), pluginID, ev)
	if err == nil {
		t.Fatal("non-zero exit must still be an error")
	}
	var exitErr *HandlerExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *HandlerExitError, got %T: %v", err, err)
	}
	if exitErr.Code != 2 {
		t.Errorf("exit code = %d, want 2", exitErr.Code)
	}
	if got := exitErr.DeclineCode(); got != "outcome_unknown" {
		t.Errorf("DeclineCode = %q, want outcome_unknown (stdout %s)", got, exitErr.Stdout)
	}
}

// ut-docs#6606: provider references never reach the log.
func TestHandleEvent_PaymentRefsRedactedInRealLog(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.exitunknown"
	if err := w.load(pluginID, "1.0.0", buildExitUnknownGuest(t)); err != nil {
		t.Fatal(err)
	}
	ev := Event{ID: "e1", Type: "payment.demopay.refund", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}
	logged := captureRealStdout(t, func() {
		if _, err := w.HandleEvent(context.Background(), pluginID, ev); err != nil {
			t.Errorf("HandleEvent: %v", err)
		}
	})
	for _, secret := range []string{"txn-SECRET-123", "ref-SECRET-456", "ref-SECRET-789"} {
		if strings.Contains(logged, secret) {
			t.Errorf("log still carries %q:\n%s", secret, logged)
		}
	}
	if !strings.Contains(logged, "decline_code") {
		t.Errorf("decline_code should stay visible in the log:\n%s", logged)
	}
}
