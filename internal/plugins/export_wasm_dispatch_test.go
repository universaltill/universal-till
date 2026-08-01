package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildExportGuest compiles the export_guest wasip1 test fixture once per
// test run — same pattern as buildHostfnGuest (wasm_hostfns_test.go).
func buildExportGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "export_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/export_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// TestExportDispatch_RealWasmModule proves the export/report dispatcher
// (ut-docs#189) reaches an actual compiled WASM plugin through the real
// wazero runtime — not a fake in-process handler (that's what
// internal/pages/export_dispatch_test.go covers). Mirrors
// TestWasmRuntimeSyncLoadsRealModuleAndSubscribes (wasm_sync_test.go): a
// real module is loaded via WasmRuntime.Sync, subscribed on the shared bus,
// and driven through EventBus.Ask exactly like data_api.go's handler does.
func TestExportDispatch_RealWasmModule(t *testing.T) {
	guest := buildExportGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.exportwasm"
	seedInstalledPlugin(t, db, pluginID, "ExportWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`,
		"exw-perm", pluginID); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'export.requested.ask', 'export', 1)`,
		"exw-hook", pluginID); err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	// Lay the real module out where Sync expects it.
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}

	w.Sync(ctx, db)

	bus := SharedBus(db)
	defer bus.ResetSubscribers() // stop drainers before the test DB closes
	if !bus.HasSubscribers("export.requested.ask") {
		t.Fatalf("export.requested.ask not subscribed after Sync")
	}
	// ".ask" events run blocking (wasm_runtime.go's Sync sets this
	// automatically; asserted explicitly here too, matching
	// TestEventBus_Ask's pattern in ipc_test.go).
	bus.SetEventMode("export.requested.ask", Blocking)

	resp, ok, err := bus.Ask(ctx, "export.requested.ask", map[string]any{
		"from": "2026-01-01", "to": "2026-01-31", "entry_key": "csv",
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !ok {
		t.Fatalf("expected the real wasm module to answer")
	}

	var parsed struct {
		OK         bool   `json:"ok"`
		Filename   string `json:"filename"`
		ContentB64 string `json:"content_b64"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse response %s: %v", resp, err)
	}
	if !parsed.OK || parsed.Filename != "export.csv" {
		t.Fatalf("unexpected response: %+v", parsed)
	}
	content, err := base64.StdEncoding.DecodeString(parsed.ContentB64)
	if err != nil {
		t.Fatalf("decode content_b64: %v", err)
	}
	if string(content) != "id,total\n1,9.99\n" {
		t.Fatalf("content = %q, want the guest's fixed CSV bytes", content)
	}
}
