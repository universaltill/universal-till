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

// ut-docs#6600, #6647: a non-zero exit on a payment authorize or refund must
// still hand the plugin's stdout (decline_code) to core.
func TestHandleEvent_AuthorizeNonZeroExitKeepsStdout(t *testing.T) {
	for _, tc := range []struct{ name, event, payload string }{
		{"authorize", "payment.demopay.authorize", `{}`},
		{"refund", "payment.demopay.refund", `{"x":"EXIT_NONZERO"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWasmRuntime(t.TempDir())
			const pluginID = "com.test.exitunknown"
			if err := w.load(pluginID, "1.0.0", buildExitUnknownGuest(t)); err != nil {
				t.Fatal(err)
			}
			ev := Event{ID: "e1", Type: tc.event, Timestamp: time.Now(), Payload: json.RawMessage(tc.payload)}
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
		})
	}
}

// ut-docs#6606, #6647: provider references never reach the log, on the clean
// exit and on the non-zero exit path alike.
func TestHandleEvent_PaymentRefsRedactedInRealLog(t *testing.T) {
	for _, tc := range []struct {
		name, event, payload string
		wantErr              bool
	}{
		{"refund exit 0", "payment.demopay.refund", `{}`, false},
		{"refund exit 2", "payment.demopay.refund", `{"x":"EXIT_NONZERO"}`, true},
		{"authorize exit 2", "payment.demopay.authorize", `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWasmRuntime(t.TempDir())
			const pluginID = "com.test.exitunknown"
			if err := w.load(pluginID, "1.0.0", buildExitUnknownGuest(t)); err != nil {
				t.Fatal(err)
			}
			ev := Event{ID: "e1", Type: tc.event, Timestamp: time.Now(), Payload: json.RawMessage(tc.payload)}
			var err error
			logged := captureRealStdout(t, func() { _, err = w.HandleEvent(context.Background(), pluginID, ev) })
			if (err != nil) != tc.wantErr {
				t.Errorf("HandleEvent err = %v, wantErr %v", err, tc.wantErr)
			}
			for _, secret := range []string{"txn-SECRET-123", "ref-SECRET-456", "ref-SECRET-789"} {
				if strings.Contains(logged, secret) {
					t.Errorf("log still carries %q:\n%s", secret, logged)
				}
			}
			if !strings.Contains(logged, "decline_code") {
				t.Errorf("decline_code should stay visible in the log:\n%s", logged)
			}
		})
	}
}

// ut-docs#6647: a non-zero exit on a payment refund keeps the decline_code
// too, and the stdout is logged through the same redaction.
func TestHandleEvent_RefundNonZeroExitKeepsStdoutRedacted(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.exitunknown"
	if err := w.load(pluginID, "1.0.0", buildExitUnknownGuest(t)); err != nil {
		t.Fatal(err)
	}
	ev := Event{ID: "e1", Type: "payment.demopay.refund", Timestamp: time.Now(), Payload: json.RawMessage(`{"x":"EXIT_NONZERO"}`)}
	var err error
	logged := captureRealStdout(t, func() { _, err = w.HandleEvent(context.Background(), pluginID, ev) })
	var exitErr *HandlerExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *HandlerExitError, got %T: %v", err, err)
	}
	if got := exitErr.DeclineCode(); got != "outcome_unknown" {
		t.Errorf("DeclineCode = %q (stdout %s)", got, exitErr.Stdout)
	}
	if strings.Contains(logged, "txn-SECRET-123") || strings.Contains(logged, "ref-SECRET-456") {
		t.Errorf("log carries a provider reference:\n%s", logged)
	}
}
