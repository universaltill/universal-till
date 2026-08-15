package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildImportGuest compiles the import_guest wasip1 test fixture once per
// test run — same pattern as buildExportGuest (export_wasm_dispatch_test.go).
func buildImportGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "import_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/import_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// TestImportDispatch_RealWasmModule proves the staged-file import path
// (ut-docs#599, ADR-0001 amendment 2026-08-12) end-to-end through the real
// wazero runtime and the real import_file_size/import_file_read/
// import_file_close host functions: a 500KB staged file — deliberately
// much larger than the guest's fixed 64KB read buffer — reaches a real
// compiled WASM module byte-for-byte (asserted by SHA256), the guest
// necessarily loops (reads > 1) rather than doing one big read, the event
// payload itself carries only a handle (never the file bytes), and the
// guest's own import_file_close removes the staged temp file from disk.
// Mirrors TestExportDispatch_RealWasmModule's structure.
func TestImportDispatch_RealWasmModule(t *testing.T) {
	guest := buildImportGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.importwasm"
	seedInstalledPlugin(t, db, pluginID, "ImportWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`,
		"imw-perm", pluginID); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'import.requested.ask', 'import', 1)`,
		"imw-hook", pluginID); err != nil {
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
	t.Cleanup(func() { importFiles.CloseAll(pluginID) })

	bus := SharedBus(db)
	defer bus.ResetSubscribers() // stop drainers before the test DB closes
	if !bus.HasSubscribers("import.requested.ask") {
		t.Fatalf("import.requested.ask not subscribed after Sync")
	}
	bus.SetEventMode("import.requested.ask", Blocking)

	// 500KB of deterministic, non-repeating-per-chunk content, staged the
	// way the dispatcher stages an upload: a temp file the registry then
	// owns. 500KB / 64KB buffer = at least 8 read calls if (and only if)
	// the guest genuinely chunks.
	const fileSize = 500 << 10
	content := make([]byte, fileSize)
	for i := range content {
		content[i] = byte((i*7 + i/251) % 256)
	}
	wantSum := sha256.Sum256(content)
	stagedPath := filepath.Join(t.TempDir(), "staged.bkp")
	if err := os.WriteFile(stagedPath, content, 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	handle, err := OpenImportFile(pluginID, stagedPath)
	if err != nil {
		t.Fatalf("OpenImportFile: %v", err)
	}

	// The exact payload shape import_dispatch.go's importRequestPayload
	// marshals to: entry key, resolved entities, the handle — and no file
	// content field of any kind.
	payload := map[string]any{
		"entry_key":   "bkp",
		"entities":    []string{"items"},
		"file_handle": handle,
		"file_name":   "staged.bkp",
		"file_size":   fileSize,
	}
	// Structural guard: the marshaled event payload must be a tiny
	// handle-carrying envelope, not a vehicle for the file itself — the
	// whole point of the ADR-0001 amendment. A 500KB file's bytes cannot
	// hide in <1KB of JSON.
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !bytes.Contains(rawPayload, []byte(`"file_handle"`)) {
		t.Fatalf("payload lost its file_handle field: %s", rawPayload)
	}
	if len(rawPayload) > 1024 {
		t.Fatalf("payload is %d bytes — file content is leaking into the event envelope", len(rawPayload))
	}

	resp, ok, err := bus.AskPlugin(ctx, pluginID, "import.requested.ask", payload)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !ok {
		t.Fatalf("expected the real wasm module to answer")
	}

	var parsed struct {
		OK      bool           `json:"ok"`
		Message string         `json:"message"`
		Error   string         `json:"error"`
		Counts  map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse response %s: %v", resp, err)
	}
	if !parsed.OK {
		t.Fatalf("guest reported failure: %+v", parsed)
	}
	// The guest's message carries everything the host functions produced:
	// the hash of the bytes it actually read, import_file_size's answer,
	// the total byte count and how many chunked reads it took.
	wantHash := fmt.Sprintf("sha256=%x", wantSum)
	if !strings.Contains(parsed.Message, wantHash) {
		t.Fatalf("message %q lacks %q — staged bytes did not reach the guest intact", parsed.Message, wantHash)
	}
	if !strings.Contains(parsed.Message, fmt.Sprintf("size=%d", fileSize)) {
		t.Fatalf("message %q lacks size=%d (import_file_size)", parsed.Message, fileSize)
	}
	if !strings.Contains(parsed.Message, fmt.Sprintf("bytes=%d", fileSize)) {
		t.Fatalf("message %q lacks bytes=%d — the guest did not read the whole file", parsed.Message, fileSize)
	}
	// 500KB against a 64KB buffer: the exact full byte count is only
	// reachable by looping, and the guest's own read counter must agree.
	reads := parsed.Counts["items"]
	if reads <= 1 {
		t.Fatalf("reads = %d, want > 1 (chunked reading must actually chunk)", reads)
	}
	if !strings.Contains(parsed.Message, "close=0 close_again=0") {
		t.Fatalf("message %q — import_file_close must be idempotent (both closes 0)", parsed.Message)
	}
	// The guest closed the handle, which must have removed the staged temp
	// file from disk — the no-leak guarantee, from the guest side.
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("staged temp file survived the guest's import_file_close: stat err=%v", err)
	}
}

// TestImportDispatch_HostBufCapClampsOversizedDstCap proves
// hostImportFileRead clamps a guest-claimed dstCap to importFileReadBufCap
// (256KB) on the HOST side, regardless of what the guest claims it can
// hold (ut-docs#615, follow-up from #599's review finding F5).
// TestImportDispatch_RealWasmModule's chunking proof is driven entirely by
// the GUEST's own 64KB buffer choice — it says nothing about what happens
// if a guest claims a much larger dstCap. This test drives the guest's
// dedicated "oversized_read" mode (testdata/import_guest/main.go), which
// allocates a 2MB buffer and issues exactly ONE import_file_read call
// claiming the full 2MB as dstCap, against a staged file with MORE than
// importFileReadBufCap bytes remaining. A real os.File.Read on a regular
// file with that much data remaining fills the whole target buffer in one
// syscall, so an unclamped host would return far more than 256KB for that
// one call; the clamp must cap it to exactly importFileReadBufCap.
func TestImportDispatch_HostBufCapClampsOversizedDstCap(t *testing.T) {
	guest := buildImportGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.importbufcap"
	seedInstalledPlugin(t, db, pluginID, "ImportBufCap", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`,
		"imwbc-perm", pluginID); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'import.requested.ask', 'import', 1)`,
		"imwbc-hook", pluginID); err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}
	w.Sync(ctx, db)
	t.Cleanup(func() { importFiles.CloseAll(pluginID) })

	bus := SharedBus(db)
	defer bus.ResetSubscribers()
	if !bus.HasSubscribers("import.requested.ask") {
		t.Fatalf("import.requested.ask not subscribed after Sync")
	}
	bus.SetEventMode("import.requested.ask", Blocking)

	// More than importFileReadBufCap bytes remaining, so an unclamped
	// single read really could satisfy the guest's oversized 2MB request.
	const fileSize = importFileReadBufCap + (100 << 10) // 356KB
	content := make([]byte, fileSize)
	for i := range content {
		content[i] = byte(i % 256)
	}
	stagedPath := filepath.Join(t.TempDir(), "staged.bkp")
	if err := os.WriteFile(stagedPath, content, 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	handle, err := OpenImportFile(pluginID, stagedPath)
	if err != nil {
		t.Fatalf("OpenImportFile: %v", err)
	}

	payload := map[string]any{
		"entry_key":   "bkp",
		"entities":    []string{"items"},
		"file_handle": handle,
		"file_name":   "staged.bkp",
		"file_size":   fileSize,
		"mode":        "oversized_read",
	}
	resp, ok, err := bus.AskPlugin(ctx, pluginID, "import.requested.ask", payload)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !ok {
		t.Fatalf("expected the real wasm module to answer")
	}

	var parsed struct {
		OK      bool           `json:"ok"`
		Message string         `json:"message"`
		Error   string         `json:"error"`
		Counts  map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse response %s: %v", resp, err)
	}
	if !parsed.OK {
		t.Fatalf("guest reported failure: %+v", parsed)
	}

	got, hasKey := parsed.Counts["first_read_bytes"]
	if !hasKey {
		t.Fatalf("response %+v missing first_read_bytes — guest's oversized_read mode not exercised", parsed)
	}
	if got != importFileReadBufCap {
		t.Fatalf("first read returned %d bytes, want exactly importFileReadBufCap (%d) — host-side dstCap clamp not applied", got, importFileReadBufCap)
	}
}

// TestImportDispatch_InvalidPtrDoesNotConsumeStreamBytes proves the
// ut-docs#614 fix: hostImportFileRead ('internal/plugins/wasm_import_file.go')
// validates the guest-supplied destination region is addressable BEFORE
// reading from the staged file, so a guest-supplied out-of-bounds dstPtr
// returns hostErrInvalid without silently consuming (and losing) bytes
// from the file's read cursor. Drives the guest's dedicated
// "invalid_ptr_read" mode (testdata/import_guest/main.go): one call with a
// bad pointer, then a normal chunked read of the WHOLE file — the
// resulting hash must match the full, untouched content. Before the fix,
// the bad-pointer call would already have pulled the first chunk off the
// file via f.Read before discovering the write would fail, permanently
// losing those bytes; the chunked read afterward would then hash a file
// short its first chunk and never match.
func TestImportDispatch_InvalidPtrDoesNotConsumeStreamBytes(t *testing.T) {
	guest := buildImportGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.importinvalidptr"
	seedInstalledPlugin(t, db, pluginID, "ImportInvalidPtr", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`,
		"imip-perm", pluginID); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'import.requested.ask', 'import', 1)`,
		"imip-hook", pluginID); err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}
	w.Sync(ctx, db)
	t.Cleanup(func() { importFiles.CloseAll(pluginID) })

	bus := SharedBus(db)
	defer bus.ResetSubscribers()
	if !bus.HasSubscribers("import.requested.ask") {
		t.Fatalf("import.requested.ask not subscribed after Sync")
	}
	bus.SetEventMode("import.requested.ask", Blocking)

	// Small enough to read in one chunked-loop iteration or a few — the
	// point is exactness of the byte count and hash, not chunking depth.
	const fileSize = 10 << 10
	content := make([]byte, fileSize)
	for i := range content {
		content[i] = byte((i*13 + 1) % 256)
	}
	wantSum := sha256.Sum256(content)
	stagedPath := filepath.Join(t.TempDir(), "staged.bkp")
	if err := os.WriteFile(stagedPath, content, 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	handle, err := OpenImportFile(pluginID, stagedPath)
	if err != nil {
		t.Fatalf("OpenImportFile: %v", err)
	}

	payload := map[string]any{
		"entry_key":   "bkp",
		"entities":    []string{"items"},
		"file_handle": handle,
		"file_name":   "staged.bkp",
		"file_size":   fileSize,
		"mode":        "invalid_ptr_read",
	}
	resp, ok, err := bus.AskPlugin(ctx, pluginID, "import.requested.ask", payload)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !ok {
		t.Fatalf("expected the real wasm module to answer")
	}

	var parsed struct {
		OK      bool           `json:"ok"`
		Message string         `json:"message"`
		Error   string         `json:"error"`
		Counts  map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse response %s: %v", resp, err)
	}
	if !parsed.OK {
		t.Fatalf("guest reported failure: %+v", parsed)
	}

	if parsed.Counts["invalid_code"] != hostErrInvalid {
		t.Fatalf("invalid_code = %v, want %d (hostErrInvalid) for an out-of-bounds dstPtr", parsed.Counts["invalid_code"], hostErrInvalid)
	}
	if got := parsed.Counts["bytes"]; got != fileSize {
		t.Fatalf("bytes read after the invalid-pointer probe = %d, want %d (the full file) — the invalid-pointer call consumed/lost bytes from the stream", got, fileSize)
	}
	wantHash := fmt.Sprintf("sha256=%x", wantSum)
	if !strings.Contains(parsed.Message, wantHash) {
		t.Fatalf("message %q lacks %q — file content read after the invalid-pointer probe does not match the untouched original (bytes were lost)", parsed.Message, wantHash)
	}
}

// TestImportFileCloseAllOnPluginUnload mirrors TestHostTCPCloseAllOnPluginUnload
// for the staged-file registry: a handle left open by a plugin (a guest
// that never called import_file_close, host-side cleanup skipped) is
// force-closed — and its temp file REMOVED from disk — when
// WasmRuntime.Sync drops the plugin's module on disable/remove/reload.
func TestImportFileCloseAllOnPluginUnload(t *testing.T) {
	guest := buildImportGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.importunload"
	seedInstalledPlugin(t, db, pluginID, "ImportUnload", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	if err := writeFileWithParents(filepath.Join(base, pluginID, "1.0.0", "plugin.wasm"), raw); err != nil {
		t.Fatalf("write module: %v", err)
	}
	w.Sync(ctx, db)
	defer SharedBus(db).ResetSubscribers()
	t.Cleanup(func() { importFiles.CloseAll(pluginID) })

	stagedPath := filepath.Join(t.TempDir(), "leaked.upload")
	if err := os.WriteFile(stagedPath, []byte("leaked staged content"), 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	handle, err := OpenImportFile(pluginID, stagedPath)
	if err != nil {
		t.Fatalf("OpenImportFile: %v", err)
	}
	// The handle survives the event boundary (the registry outlives module
	// instances) ...
	if _, ok := importFiles.Get(pluginID, handle); !ok {
		t.Fatal("open handle not in the registry")
	}

	// ... until the plugin is dropped: deactivate and Sync, the same point
	// tcpConns.CloseAll fires.
	if _, err := db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	w.Sync(ctx, db)

	if _, ok := importFiles.Get(pluginID, handle); ok {
		t.Error("registry still holds the handle after unload")
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Errorf("staged temp file survived plugin unload: stat err=%v", err)
	}
}
