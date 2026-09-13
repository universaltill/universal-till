package issuereport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/diagnostics"
)

type memKV map[string]string

func (m memKV) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}
func (m memKV) Set(_ context.Context, k, v string) error { m[k] = v; return nil }

func withDiagnosticsSession(t *testing.T, active bool) {
	t.Helper()
	orig := diagnostics.PendingDir
	diagnostics.PendingDir = t.TempDir()
	kv := memKV{}
	_, _ = diagnostics.Stop(context.Background(), kv, diagnostics.EndedStopped)
	if active {
		if err := diagnostics.Activate(context.Background(), kv, "sess-report", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = diagnostics.Stop(context.Background(), kv, diagnostics.EndedStopped)
		diagnostics.PendingDir = orig
	})
}

func readMeta(t *testing.T, id string) Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(PendingDir, id, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ADR-0092 §6 (till side): while a diagnostic session is active, a saved
// bundle carries a reference to it plus its most recent locally-buffered
// events — over the SAME bundle/meta.json shape the existing upload path
// already sends, no new channel.
func TestSaveAttachesDiagnosticSnapshotWhenActive(t *testing.T) {
	withTempPendingDir(t)
	withDiagnosticsSession(t, true)
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 3})

	id, err := Save("printer jammed", "en", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := readMeta(t, id)
	if m.DiagnosticSessionID != "sess-report" {
		t.Fatalf("DiagnosticSessionID = %q", m.DiagnosticSessionID)
	}
	if len(m.DiagnosticEvents) != 1 || !strings.Contains(string(m.DiagnosticEvents[0]), `"diagnostic_gap"`) {
		t.Fatalf("DiagnosticEvents = %v, want the one buffered gap event", m.DiagnosticEvents)
	}
}

// Without a session the fields are absent from meta.json entirely
// (omitempty) — bundles look exactly as they did before this field existed.
func TestSaveOmitsDiagnosticSnapshotWhenInactive(t *testing.T) {
	withTempPendingDir(t)
	withDiagnosticsSession(t, false)
	id, err := Save("printer jammed", "en", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(PendingDir, id, "meta.json"))
	if strings.Contains(string(raw), "diagnostic") {
		t.Fatalf("inactive bundle carries diagnostic fields: %s", raw)
	}
}
