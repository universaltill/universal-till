package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildBigViewGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bigview_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/bigview_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// ut-docs#3160: a ui.* answer is capped (maxUIAnswerBytes); a plugin that
// writes more fails the call instead of growing the host's memory without
// bound. Other events keep their previous behaviour (an export answer can
// legitimately be large).
func TestWasmHandleEvent_UIAnswerStdoutCapped_3160(t *testing.T) {
	guest := buildBigViewGuest(t)
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.bigview"
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load guest module: %v", err)
	}
	for _, typ := range []string{"ui.view.ask", "ui.action.ask"} {
		ev := Event{ID: "ev-" + typ, Type: typ, Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}
		resp, err := w.HandleEvent(context.Background(), pluginID, ev)
		if err == nil {
			t.Fatalf("%s: 2 MiB answer accepted (%d bytes), want a failed call", typ, len(resp))
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("%s: err = %v, want it to name the size cap", typ, err)
		}
	}
	ev := Event{ID: "ev-other", Type: "com.test.bigview.other", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}
	resp, err := w.HandleEvent(context.Background(), pluginID, ev)
	if err != nil {
		t.Fatalf("non-ui event: %v", err)
	}
	if len(resp) < 2<<20 {
		t.Fatalf("non-ui event answer truncated to %d bytes", len(resp))
	}
}
