package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// Plugin view uploads (ADR-0121 §3/§7, ut-docs#3793): a file the operator
// submitted through a plugin view, staged by the host and handed to the
// plugin only as an opaque token it opens with upload_open.

var uploadTokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func stageUploadFile(t *testing.T, pluginID string, content []byte) (string, string) {
	t.Helper()
	path := stageTempFile(t, content)
	tok, err := StageUpload(pluginID, path)
	if err != nil {
		t.Fatalf("StageUpload: %v", err)
	}
	return tok, path
}

func assertPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged upload missing too early: %v", err)
	}
}

func TestStageUpload_TokensCapAndRelease_3793(t *testing.T) {
	const a, b = "com.test.upload.a", "com.test.upload.b"
	t.Cleanup(func() { uploads.CloseAll(a); uploads.CloseAll(b) })

	tok, path := stageUploadFile(t, a, []byte("hello"))
	if !uploadTokenRe.MatchString(tok) {
		t.Fatalf("token %q is not 32 hex characters", tok)
	}
	assertPresent(t, path)
	tok2, _ := stageUploadFile(t, a, []byte("x"))
	if tok2 == tok {
		t.Fatal("two staged uploads got the same token")
	}

	// Per-plugin cap: staging beyond it is refused (the caller keeps the
	// file); another plugin is unaffected.
	for i := 2; i < maxStagedUploadsPerPlugin; i++ {
		stageUploadFile(t, a, []byte("x"))
	}
	over := stageTempFile(t, []byte("over"))
	if _, err := StageUpload(a, over); err == nil {
		t.Fatal("staging beyond the per-plugin cap succeeded")
	}
	assertPresent(t, over) // still the caller's to remove
	tokB, pathB := stageUploadFile(t, b, []byte("b"))

	ReleaseUploads(a, []string{tok})
	assertGone(t, path)
	ReleaseUploads(a, []string{tok})  // idempotent
	ReleaseUploads(a, []string{tokB}) // another plugin's token: no-op
	assertPresent(t, pathB)

	uploads.CloseAll(a)
	if n := uploads.count(a); n != 0 {
		t.Fatalf("CloseAll left %d staged uploads", n)
	}
	assertPresent(t, pathB)
	uploads.CloseAll(b)
	assertGone(t, pathB)
}

// upload_open is scoped to the plugin the upload was staged for: another
// plugin presenting the same token is refused exactly like an unknown one.
func TestUploadOpen_ForeignAndMalformedTokens_3793(t *testing.T) {
	const owner, other = "com.test.upload.owner", "com.test.upload.other"
	t.Cleanup(func() { uploads.CloseAll(owner); uploads.CloseAll(other) })
	tok, path := stageUploadFile(t, owner, []byte("secret upload"))

	if got := uploads.open(other, tok); got != hostErrNotFound {
		t.Fatalf("foreign token open = %d, want %d", got, hostErrNotFound)
	}
	for _, bad := range []string{"", "nothex", tok + "00", "ABCDEF0123456789ABCDEF0123456789"} {
		if got := uploads.open(owner, bad); got != hostErrInvalid {
			t.Errorf("open(%q) = %d, want %d (malformed)", bad, got, hostErrInvalid)
		}
	}
	if got := uploads.open(owner, "0123456789abcdef0123456789abcdef"); got != hostErrNotFound {
		t.Errorf("unknown token open = %d, want %d", got, hostErrNotFound)
	}
	h := uploads.open(owner, tok)
	if h < 0 {
		t.Fatalf("owner open = %d", h)
	}
	// A handle is scoped like the token.
	if _, ok := uploads.get(other, h); ok {
		t.Fatal("another plugin reached the owner's handle")
	}
	uploads.closeHandle(other, h)
	assertPresent(t, path)
	// Close consumes the token and removes the staged file.
	uploads.closeHandle(owner, h)
	assertGone(t, path)
	if got := uploads.open(owner, tok); got != hostErrNotFound {
		t.Fatalf("open after close = %d, want %d", got, hostErrNotFound)
	}
}

func TestUploadOpen_HandleCapIsBusy_3793(t *testing.T) {
	const p = "com.test.upload.handles"
	t.Cleanup(func() { uploads.CloseAll(p) })
	tok, _ := stageUploadFile(t, p, []byte("x"))
	for i := 0; i < maxUploadHandlesPerPlugin; i++ {
		if h := uploads.open(p, tok); h < 0 {
			t.Fatalf("open %d = %d", i, h)
		}
	}
	if got := uploads.open(p, tok); got != hostErrBusy {
		t.Fatalf("open beyond the handle cap = %d, want %d", got, hostErrBusy)
	}
}

func buildUploadGuest(t *testing.T) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "upload_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/upload_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// uploadGuestRuntime installs the upload guest as pluginID@version
// subscribed to ui.action.ask and syncs it.
func uploadGuestRuntime(t *testing.T, guest []byte, pluginIDs ...string) (*WasmRuntime, func(), string) {
	t.Helper()
	db := managerTestDB(t)
	base := t.TempDir()
	w := NewWasmRuntime(base)
	for _, id := range pluginIDs {
		seedInstalledPlugin(t, db, id, id, "1.0.0", "wasm", true)
		if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`, id+"-perm", id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'ui.action.ask', 'view', 1)`, id+"-hook", id); err != nil {
			t.Fatal(err)
		}
		if err := writeFileWithParents(filepath.Join(base, id, "1.0.0", "plugin.wasm"), guest); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { uploads.CloseAll(id) })
	}
	w.Sync(context.Background(), db)
	bus := SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("ui.action.ask", Blocking)
	return w, func() { w.Sync(context.Background(), db) }, base
}

func askUploadGuest(t *testing.T, w *WasmRuntime, pluginID, mode, token string) map[string]any {
	t.Helper()
	bus := SharedBus(w.db)
	resp, ok, err := bus.AskPlugin(context.Background(), pluginID, "ui.action.ask", map[string]any{
		"mode":           mode,
		"upload_handles": []map[string]any{{"field": "photo", "handle": token}},
	})
	if err != nil || !ok {
		t.Fatalf("ask: ok=%v err=%v", ok, err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("answer %s: %v", resp, err)
	}
	return out
}

// TestUploadWasm_RoundTrip_3793: the real guest streams a staged upload
// byte-for-byte through upload_open/read/close in several reads; its own
// close removes the file and consumes the token.
func TestUploadWasm_RoundTrip_3793(t *testing.T) {
	const p = "com.test.uploadwasm"
	w, _, _ := uploadGuestRuntime(t, buildUploadGuest(t), p)

	content := make([]byte, 100<<10)
	for i := range content {
		content[i] = byte((i*13 + i/97) % 256)
	}
	tok, path := stageUploadFile(t, p, content)
	out := askUploadGuest(t, w, p, "", tok)
	if out["error"] != nil {
		t.Fatalf("guest error: %v", out)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(content)); out["sha256"] != want {
		t.Fatalf("sha256 = %v, want %s", out["sha256"], want)
	}
	if out["bytes"] != float64(len(content)) || out["reads"].(float64) < 2 {
		t.Fatalf("bytes/reads = %v/%v", out["bytes"], out["reads"])
	}
	if out["close"] != float64(0) || out["close_again"] != float64(0) {
		t.Fatalf("close = %v / %v, want idempotent 0", out["close"], out["close_again"])
	}
	if out["reopen"] != float64(hostErrNotFound) {
		t.Fatalf("reopen after close = %v, want %d", out["reopen"], hostErrNotFound)
	}
	assertGone(t, path)
}

// TestUploadWasm_ForeignTokenRefused_3793: a real guest of another plugin
// presenting the owner's token gets not-found; a malformed one invalid.
func TestUploadWasm_ForeignTokenRefused_3793(t *testing.T) {
	const owner, other = "com.test.uploadowner", "com.test.uploadother"
	w, _, _ := uploadGuestRuntime(t, buildUploadGuest(t), owner, other)
	tok, path := stageUploadFile(t, owner, []byte("owner's file"))

	if out := askUploadGuest(t, w, other, "probe", tok); out["open"] != float64(hostErrNotFound) {
		t.Fatalf("foreign upload_open = %v, want %d", out["open"], hostErrNotFound)
	}
	if out := askUploadGuest(t, w, owner, "probe", "../../etc/passwd"); out["open"] != float64(hostErrInvalid) {
		t.Fatalf("malformed upload_open = %v, want %d", out["open"], hostErrInvalid)
	}
	assertPresent(t, path)
}

// TestUploadWasm_UnloadRemovesStaged_3793: a guest that opens an upload and
// never closes it leaks nothing past the plugin's unload (Sync drop path).
func TestUploadWasm_UnloadRemovesStaged_3793(t *testing.T) {
	const p = "com.test.uploadunload"
	w, sync, _ := uploadGuestRuntime(t, buildUploadGuest(t), p)
	tok, path := stageUploadFile(t, p, []byte("left open"))
	if out := askUploadGuest(t, w, p, "leave_open", tok); out["open_ok"] != true || out["read"] != float64(4) {
		t.Fatalf("leave_open = %v", out)
	}
	assertPresent(t, path) // survives the event boundary ...
	if _, err := w.db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = ?`, p); err != nil {
		t.Fatal(err)
	}
	sync()
	assertGone(t, path) // ... but not the unload
	if n := uploads.count(p); n != 0 {
		t.Fatalf("%d uploads still registered after unload", n)
	}
}

// TestUploadWasm_ReloadRemovesStaged_3793: a version bump (reload, the
// other path that drops a module) also releases the plugin's uploads.
func TestUploadWasm_ReloadRemovesStaged_3793(t *testing.T) {
	const p = "com.test.uploadreload"
	guest := buildUploadGuest(t)
	w, sync, base := uploadGuestRuntime(t, guest, p)
	_, path := stageUploadFile(t, p, []byte("staged before the update"))
	if err := writeFileWithParents(filepath.Join(base, p, "1.0.1", "plugin.wasm"), guest); err != nil {
		t.Fatal(err)
	}
	seedCatalogRow(t, w.db, p, p, "1.0.1", "")
	if _, err := w.db.Exec(`UPDATE plugins SET version = '1.0.1' WHERE id = ?`, p); err != nil {
		t.Fatal(err)
	}
	sync()
	if w.versions[p] != "1.0.1" {
		t.Fatalf("plugin not reloaded: version %q", w.versions[p])
	}
	assertGone(t, path)
}

// count is how many uploads are staged for pluginID (test-only).
func (r *uploadRegistry) count(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.staged[pluginID]) + len(r.parked[pluginID])
}

// A kept upload (ut-docs#4006, ADR-0121 R2a): the sell screen's identify
// photo must survive the plugin's own upload_close so core can keep it for
// a pick. upload_close still consumes the token for the guest (no reopen),
// but parks the file; TakeUpload hands it to core once; ReleaseUploads and
// CloseAll still delete it, parked or not.
func TestKeptUpload_CloseParksTakeHandsOver_4006(t *testing.T) {
	const p = "com.test.upload.kept"
	t.Cleanup(func() { uploads.CloseAll(p) })

	path := stageTempFile(t, []byte("photo"))
	tok, err := StageUploadKept(p, path)
	if err != nil {
		t.Fatal(err)
	}
	h := uploads.open(p, tok)
	if h < 0 {
		t.Fatalf("open = %d", h)
	}
	uploads.closeHandle(p, h)
	assertPresent(t, path) // parked, not deleted
	if got := uploads.open(p, tok); got != hostErrNotFound {
		t.Fatalf("reopen after upload_close = %d, want not found", got)
	}
	if n := uploads.count(p); n != 1 {
		t.Fatalf("a parked upload still counts toward the cap: count = %d", n)
	}
	got, ok := TakeUpload(p, tok)
	if !ok || got != path {
		t.Fatalf("TakeUpload = %q, %v; want %q", got, ok, path)
	}
	if _, ok := TakeUpload(p, tok); ok {
		t.Fatal("TakeUpload handed the same upload out twice")
	}
	ReleaseUploads(p, []string{tok}) // the job's release after the take: a no-op
	assertPresent(t, path)           // core owns it now
	if n := uploads.count(p); n != 0 {
		t.Fatalf("count after take = %d", n)
	}
	_ = os.Remove(path)

	// Taken while still open: the handle is closed, the file handed over.
	path2 := stageTempFile(t, []byte("photo2"))
	tok2, _ := StageUploadKept(p, path2)
	h2 := uploads.open(p, tok2)
	if got, ok := TakeUpload(p, tok2); !ok || got != path2 {
		t.Fatalf("TakeUpload(open) = %q, %v", got, ok)
	}
	if _, ok := uploads.get(p, h2); ok {
		t.Fatal("TakeUpload left the guest's handle open")
	}
	_ = os.Remove(path2)

	// Released (the job failed): parked or staged, the file is deleted.
	path3 := stageTempFile(t, []byte("photo3"))
	tok3, _ := StageUploadKept(p, path3)
	uploads.closeHandle(p, uploads.open(p, tok3))
	ReleaseUploads(p, []string{tok3})
	assertGone(t, path3)
	if _, ok := TakeUpload(p, tok3); ok {
		t.Fatal("a released upload was handed out")
	}

	// Unload: parked files go too.
	path4 := stageTempFile(t, []byte("photo4"))
	tok4, _ := StageUploadKept(p, path4)
	uploads.closeHandle(p, uploads.open(p, tok4))
	uploads.CloseAll(p)
	assertGone(t, path4)

	// A plain (not kept) upload is still deleted by upload_close, and
	// TakeUpload of an unknown or foreign token is refused.
	tok5, path5 := stageUploadFile(t, p, []byte("plain"))
	uploads.closeHandle(p, uploads.open(p, tok5))
	assertGone(t, path5)
	tok6, path6 := stageUploadFile(t, p, []byte("mine"))
	if _, ok := TakeUpload("com.test.other", tok6); ok {
		t.Fatal("another plugin took the upload")
	}
	assertPresent(t, path6)
}
