package plugins

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/logging"
)

// Staged-file transport for import-type plugins (ut-docs#599, ADR-0001
// amendment 2026-08-12): the host stages an uploaded import file to a temp
// file on disk and hands the "import.requested.ask" event payload only a
// plugin-scoped HANDLE — never the file bytes. The guest pulls the content
// itself, in bounded chunks, through the import_file_size /
// import_file_read / import_file_close host functions, so neither side
// ever holds a hundreds-of-MB upload fully in memory just to cross the
// host↔guest boundary once.
//
// No dedicated permission gates these three functions: the dispatcher
// resolved and permission-checked the owning plugin before staging the
// file (entry resolution + per-entity "<entity>:write" grants +
// AskPlugin's own events:receive check), and a handle is scoped to the
// plugin that owns it via the (pluginID, handle) registry key — a plugin
// cannot address another plugin's staged file even by guessing a small
// integer, exactly like tcpConnRegistry.
//
// Staged files are tracked in a per-plugin handle registry that OUTLIVES
// the per-event module instance (mirroring wasm_tcp.go's tcpConnRegistry),
// and are force-closed — the underlying temp file REMOVED from disk — by
// WasmRuntime.Sync when the plugin is disabled, removed or reloaded, so a
// dropped plugin never leaks an fd or a stale temp file.

const (
	// maxImportFileHandlesPerPlugin caps concurrent staged files per plugin
	// — importing one file at a time per plugin is the normal case, a
	// handful at most.
	maxImportFileHandlesPerPlugin = 4
	// importFileReadBufCap bounds the host-side read buffer regardless of
	// the dstCap the guest claims (mirrors tcpReadBufCap's role).
	importFileReadBufCap = 256 << 10
)

// stagedImportFile is one registry entry: the read-only fd plus the temp
// path it was opened from, kept so Close/CloseAll can remove the file from
// disk after closing the fd — a staged import temp file must never survive
// on disk once the plugin drops it or is disabled/reloaded.
type stagedImportFile struct {
	f    *os.File
	path string
}

// importFileRegistry tracks staged import files by (pluginID, handle).
// Handles are sequential per plugin starting at 0 and never reused within a
// plugin's registry lifetime, so a stale handle can't alias a new file.
type importFileRegistry struct {
	mu       sync.Mutex
	byPlugin map[string]map[int32]stagedImportFile
	next     map[string]int32
}

func newImportFileRegistry() *importFileRegistry {
	return &importFileRegistry{
		byPlugin: map[string]map[int32]stagedImportFile{},
		next:     map[string]int32{},
	}
}

// importFiles is the process-wide registry: like tcpConns, staged files
// must survive per-event module instances, and Sync must be able to reap
// them.
var importFiles = newImportFileRegistry()

// Open registers f (opened from path) under the plugin's next handle.
// Returns (handle, true) or (0, false) when the plugin is already at
// maxImportFileHandlesPerPlugin — the caller still owns (and must close
// and remove) the file in that case.
func (r *importFileRegistry) Open(pluginID string, f *os.File, path string) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byPlugin[pluginID]) >= maxImportFileHandlesPerPlugin {
		return 0, false
	}
	if r.byPlugin[pluginID] == nil {
		r.byPlugin[pluginID] = map[int32]stagedImportFile{}
	}
	h := r.next[pluginID]
	r.next[pluginID] = h + 1
	r.byPlugin[pluginID][h] = stagedImportFile{f: f, path: path}
	return h, true
}

// Get looks up a staged file by (pluginID, handle).
func (r *importFileRegistry) Get(pluginID string, handle int32) (*os.File, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sf, ok := r.byPlugin[pluginID][handle]
	return sf.f, ok
}

// Close closes and removes one handle, deleting its temp file from disk;
// unknown handles are a no-op (import_file_close is idempotent, so
// host-side cleanup and a well-behaved guest's own close can both run).
func (r *importFileRegistry) Close(pluginID string, handle int32) {
	r.mu.Lock()
	sf, ok := r.byPlugin[pluginID][handle]
	if ok {
		delete(r.byPlugin[pluginID], handle)
	}
	r.mu.Unlock()
	if ok {
		_ = sf.f.Close()
		_ = os.Remove(sf.path)
	}
}

// CloseAll force-closes every handle a plugin holds — removing each staged
// temp file from disk — and clears its entry. Called from WasmRuntime.Sync
// when a plugin's module is dropped, so a disabled/removed/reloaded plugin
// never leaks an fd or a stale temp file.
func (r *importFileRegistry) CloseAll(pluginID string) {
	r.mu.Lock()
	files := r.byPlugin[pluginID]
	delete(r.byPlugin, pluginID)
	delete(r.next, pluginID)
	r.mu.Unlock()
	for _, sf := range files {
		_ = sf.f.Close()
		_ = os.Remove(sf.path)
	}
}

// OpenImportFile is the host-side entry point: it opens the staged temp
// file at path read-only and registers it for pluginID, returning the
// handle the "import.requested.ask" payload carries. The registry owns the
// fd AND the path from here on — Close/CloseAll delete the temp file, so
// the caller must not remove it separately once this returns nil error.
func OpenImportFile(pluginID, path string) (int32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	handle, ok := importFiles.Open(pluginID, f, path)
	if !ok {
		_ = f.Close()
		logging.L().Infof("[wasm:%s] import file open refused: max %d handles", pluginID, maxImportFileHandlesPerPlugin)
		return 0, errors.New("plugin already holds the maximum number of staged import files")
	}
	return handle, nil
}

// CloseImportFile is the host-side cleanup counterpart to OpenImportFile:
// the dispatcher calls it after AskPlugin returns (success or failure) so
// a plugin that never calls import_file_close — or errors, or times out —
// still can't leak the temp file. Idempotent, like the guest-facing close.
func CloseImportFile(pluginID string, handle int32) {
	importFiles.Close(pluginID, handle)
}

// hostImportFileSize returns the staged file's total size in bytes, or a
// negative hostErr* code when the handle is unknown to this plugin.
func hostImportFileSize(ctx context.Context, handle int32) int64 {
	s, ok := stateFrom(ctx)
	if !ok {
		return int64(hostErrInternal)
	}
	f, ok := importFiles.Get(s.pluginID, handle)
	if !ok {
		return int64(hostErrNotFound)
	}
	info, err := f.Stat()
	if err != nil {
		logging.L().Infof("[wasm:%s] import file stat handle %d failed: %v", s.pluginID, handle, err)
		return int64(hostErrInternal)
	}
	return info.Size()
}

// hostImportFileRead performs one sequential read from the staged file's
// implicit cursor (standard file-descriptor semantics — no explicit seek;
// sequential streaming is the point) and writes what was read into the
// guest buffer. Returns bytes ACTUALLY READ this call, 0 at EOF, or a
// negative hostErr* code.
//
// NOTE — deliberate departure from the module's usual buffer ABI: the
// "write min(len, dstCap), return the FULL length, guest retries with a
// bigger buffer" convention (writeGuest) fits single-shot lookups, not a
// streaming read whose whole purpose is that the guest never needs a
// buffer the size of the data. import_file_read has plain read()
// semantics instead: at most dstCap bytes are written and the return
// value is exactly how many landed in the guest buffer.
func hostImportFileRead(ctx context.Context, m api.Module, handle int32, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	f, ok := importFiles.Get(s.pluginID, handle)
	if !ok {
		return hostErrNotFound
	}
	if dstCap == 0 {
		return hostErrInvalid
	}
	bufCap := dstCap
	if bufCap > importFileReadBufCap {
		bufCap = importFileReadBufCap
	}
	// Validate the destination region is addressable BEFORE touching the
	// file (ut-docs#614): the staged file's cursor advances on every
	// f.Read, with no seek-back host function, so a bad dstPtr discovered
	// only after the read would silently and permanently lose that chunk
	// of the stream. Catching it here means an invalid pointer costs the
	// guest nothing — the bytes are still there for a retry with a good one.
	if _, ok := m.Memory().Read(dstPtr, bufCap); !ok {
		return hostErrInvalid
	}
	buf := make([]byte, bufCap)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		logging.L().Infof("[wasm:%s] import file read handle %d failed: %v", s.pluginID, handle, err)
		return hostErrInternal
	}
	if n == 0 {
		return 0 // EOF
	}
	if !m.Memory().Write(dstPtr, buf[:n]) {
		return hostErrInvalid
	}
	return int32(n)
}

// hostImportFileClose closes and forgets the handle, removing the staged
// temp file from disk. Idempotent: closing an unknown or already-closed
// handle also returns 0 (mirrors tcp_close — the host-side dispatcher
// cleanup and the guest's own close may both run).
func hostImportFileClose(ctx context.Context, handle int32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	importFiles.Close(s.pluginID, handle)
	return 0
}
