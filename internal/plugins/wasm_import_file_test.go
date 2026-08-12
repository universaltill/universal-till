package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stageTempFile creates a real on-disk temp file with content and returns
// its path — the same thing the /api/data/import dispatcher stages before
// registering it (import_dispatch.go).
func stageTempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "staged.upload")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	return path
}

func mustOpenStaged(t *testing.T, r *importFileRegistry, pluginID string, content []byte) (int32, string) {
	t.Helper()
	path := stageTempFile(t, content)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open staged file: %v", err)
	}
	h, ok := r.Open(pluginID, f, path)
	if !ok {
		f.Close()
		t.Fatalf("registry refused a handle below the cap")
	}
	return h, path
}

// assertGone fails unless path no longer exists on disk — the
// review-sensitive guarantee of ut-docs#599: a staged import temp file
// must never survive its handle being closed or reaped.
func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged temp file still on disk after close: %s (stat err=%v)", path, err)
	}
}

// Registry unit semantics: per-plugin sequential handles, isolation between
// plugins, idempotent Close, CloseAll clears everything for one plugin only
// — mirrors TestTCPConnRegistry, plus the file-specific guarantee that
// Close/CloseAll REMOVE the staged temp file from disk.
func TestImportFileRegistry(t *testing.T) {
	r := newImportFileRegistry()

	hA0, pA0 := mustOpenStaged(t, r, "plugin.a", []byte("aaa"))
	if hA0 != 0 {
		t.Fatalf("first handle for plugin.a = %d, want 0", hA0)
	}
	hA1, pA1 := mustOpenStaged(t, r, "plugin.a", []byte("bbb"))
	if hA1 != 1 {
		t.Fatalf("second handle = %d, want sequential 1", hA1)
	}
	hB0, pB0 := mustOpenStaged(t, r, "plugin.b", []byte("ccc"))
	if hB0 != 0 {
		t.Fatalf("plugin.b first handle = %d, want its own sequence starting 0", hB0)
	}

	if _, ok := r.Get("plugin.a", hA0); !ok {
		t.Error("Get lost an open handle")
	}
	if _, ok := r.Get("plugin.a", 99); ok {
		t.Error("Get returned an unknown handle")
	}
	if _, ok := r.Get("plugin.b", hA1); ok {
		t.Error("plugin.b sees plugin.a's handle")
	}

	// Reading through the registry's file works with plain read semantics.
	f, _ := r.Get("plugin.a", hA0)
	chunk := make([]byte, 2)
	if n, err := f.Read(chunk); err != nil || n != 2 || string(chunk) != "aa" {
		t.Fatalf("read via registry handle = %d %q %v", n, chunk, err)
	}

	r.Close("plugin.a", hA0)
	if _, ok := r.Get("plugin.a", hA0); ok {
		t.Error("handle still present after Close")
	}
	assertGone(t, pA0)       // Close must remove the temp file, not just the fd
	r.Close("plugin.a", hA0) // idempotent: closing again must not panic

	// Handles are never reused within a plugin's lifetime.
	hA2, pA2 := mustOpenStaged(t, r, "plugin.a", []byte("ddd"))
	if hA2 != 2 {
		t.Errorf("handle after close = %d, want 2 (no reuse)", hA2)
	}

	r.CloseAll("plugin.a")
	if _, ok := r.Get("plugin.a", hA1); ok {
		t.Error("CloseAll left plugin.a handles behind")
	}
	// CloseAll must remove every one of plugin.a's staged temp files ...
	assertGone(t, pA1)
	assertGone(t, pA2)
	// ... while plugin.b's handle AND its temp file survive untouched.
	if _, ok := r.Get("plugin.b", hB0); !ok {
		t.Error("CloseAll(plugin.a) closed plugin.b's handle")
	}
	if _, err := os.Stat(pB0); err != nil {
		t.Errorf("CloseAll(plugin.a) removed plugin.b's temp file: %v", err)
	}
	r.CloseAll("plugin.b")
	assertGone(t, pB0)
}

// Max concurrent staged files per plugin is capped at the registry level.
func TestImportFileRegistryMaxHandles(t *testing.T) {
	r := newImportFileRegistry()
	for i := 0; i < maxImportFileHandlesPerPlugin; i++ {
		mustOpenStaged(t, r, "plugin.max", []byte("x")) // fatals if refused below the cap
	}
	path := stageTempFile(t, []byte("over"))
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, ok := r.Open("plugin.max", f, path); ok {
		t.Fatal("open beyond the per-plugin cap succeeded")
	}
	f.Close()
	r.CloseAll("plugin.max")
}

// OpenImportFile/CloseImportFile are the host-side wrappers the dispatcher
// uses: OpenImportFile registers the staged path in the process-wide
// registry, CloseImportFile is the always-run cleanup that removes the
// temp file even when the guest never called import_file_close.
func TestOpenCloseImportFile(t *testing.T) {
	const pluginID = "com.test.importfile.hostside"
	t.Cleanup(func() { importFiles.CloseAll(pluginID) })

	path := stageTempFile(t, []byte("staged content"))
	handle, err := OpenImportFile(pluginID, path)
	if err != nil {
		t.Fatalf("OpenImportFile: %v", err)
	}
	if _, ok := importFiles.Get(pluginID, handle); !ok {
		t.Fatal("OpenImportFile did not register the handle")
	}
	CloseImportFile(pluginID, handle)
	assertGone(t, path)
	CloseImportFile(pluginID, handle) // idempotent
	if _, err := OpenImportFile(pluginID, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("OpenImportFile on a missing path must error")
	}
}

// hostImportFileSize/hostImportFileClose don't touch guest memory, so their
// handle-scoping semantics are testable without a module instance: an
// unknown handle (or another plugin's handle) is hostErrNotFound, and a
// reaped plugin's handle is gone.
func TestHostImportFileSizeScoping(t *testing.T) {
	db := managerTestDB(t)
	const owner = "com.test.importfile.owner"
	const other = "com.test.importfile.other"
	t.Cleanup(func() {
		importFiles.CloseAll(owner)
		importFiles.CloseAll(other)
	})

	content := []byte("0123456789")
	path := stageTempFile(t, content)
	handle, err := OpenImportFile(owner, path)
	if err != nil {
		t.Fatalf("OpenImportFile: %v", err)
	}

	ownerCtx := withHostState(context.Background(), &hostState{pluginID: owner, db: db})
	otherCtx := withHostState(context.Background(), &hostState{pluginID: other, db: db})

	if got := hostImportFileSize(ownerCtx, handle); got != int64(len(content)) {
		t.Fatalf("size = %d, want %d", got, len(content))
	}
	// Another plugin guessing the same small integer must not reach the
	// owner's staged file.
	if got := hostImportFileSize(otherCtx, handle); got != int64(hostErrNotFound) {
		t.Fatalf("cross-plugin size = %d, want %d (not found)", got, hostErrNotFound)
	}
	if got := hostImportFileClose(otherCtx, handle); got != 0 {
		t.Fatalf("cross-plugin close = %d, want idempotent 0", got)
	}
	// ... and that cross-plugin close must not have reaped the owner's file.
	if got := hostImportFileSize(ownerCtx, handle); got != int64(len(content)) {
		t.Fatalf("size after cross-plugin close = %d, want %d", got, len(content))
	}

	if got := hostImportFileClose(ownerCtx, handle); got != 0 {
		t.Fatalf("close = %d, want 0", got)
	}
	assertGone(t, path)
	if got := hostImportFileSize(ownerCtx, handle); got != int64(hostErrNotFound) {
		t.Fatalf("size after close = %d, want %d (not found)", got, hostErrNotFound)
	}
}
