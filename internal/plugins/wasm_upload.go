package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/logging"
)

// Plugin view uploads (ADR-0121 §3 upload_open/upload_read/upload_close,
// §7 form field kind "file"; ut-docs#3793): a file the operator submitted
// through a plugin view is staged by the host to a temp file and handed to
// the plugin's ui.action.ask (and a job it starts) only as an opaque,
// unguessable token in upload_handles — never the bytes. The guest opens
// the token with upload_open and streams it with upload_read, exactly like
// the import_file_* transport this generalises (wasm_import_file.go).
//
// No dedicated permission gates these functions: a token only ever reaches
// a plugin inside a ui.action.ask (or the job it started) that passed the
// entry's ui:page check, and every token is bound to that plugin — the
// (pluginID, token) key means another plugin presenting the same token is
// refused exactly like an unknown one.
//
// The temp file never outlives: upload_close (which consumes the token),
// the end of the ui.action.ask that received it (or of the job it started)
// via ReleaseUploads, or the plugin's unload/reload (WasmRuntime.Sync →
// uploads.CloseAll).

const (
	// maxStagedUploadsPerPlugin caps staged (not yet released) uploads per
	// plugin: two posts' worth of files (a post carries at most 4).
	maxStagedUploadsPerPlugin = 8
	// maxUploadHandlesPerPlugin caps open upload handles per plugin.
	maxUploadHandlesPerPlugin = 8
	// uploadReadBufCap bounds the host-side read buffer whatever dstCap the
	// guest claims (importFileReadBufCap's role).
	uploadReadBufCap = 256 << 10
	// maxUploadTokenBytes: a token is 32 hex characters; anything longer is
	// refused before it is even read from guest memory.
	maxUploadTokenBytes = 64
)

var uploadTokenFormat = regexp.MustCompile(`^[0-9a-f]{32}$`)

// uploadHandle is one open fd on a staged upload.
type uploadHandle struct {
	f     *os.File
	token string
}

// uploadRegistry tracks staged uploads by (pluginID, token) and their open
// handles by (pluginID, handle). Handles are sequential per plugin and never
// reused within the registry's lifetime for that plugin.
type uploadRegistry struct {
	mu      sync.Mutex
	staged  map[string]map[string]string // plugin → token → temp path
	handles map[string]map[int32]uploadHandle
	next    map[string]int32
}

func newUploadRegistry() *uploadRegistry {
	return &uploadRegistry{
		staged:  map[string]map[string]string{},
		handles: map[string]map[int32]uploadHandle{},
		next:    map[string]int32{},
	}
}

// uploads is the process-wide registry: staged uploads outlive per-event
// module instances (a job event reads what ui.action.ask received), and
// Sync must be able to reap them.
var uploads = newUploadRegistry()

var errUploadsFull = errors.New("plugin already holds the maximum number of staged uploads")

// stage registers path for pluginID under a new random token.
func (r *uploadRegistry) stage(pluginID, path string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.staged[pluginID]) >= maxStagedUploadsPerPlugin {
		return "", errUploadsFull
	}
	if r.staged[pluginID] == nil {
		r.staged[pluginID] = map[string]string{}
	}
	r.staged[pluginID][tok] = path
	return tok, nil
}

// open opens a staged upload for pluginID: a fresh fd (cursor at 0) under
// a new handle, or a negative hostErr* code.
func (r *uploadRegistry) open(pluginID, token string) int32 {
	if !uploadTokenFormat.MatchString(token) {
		return hostErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path, ok := r.staged[pluginID][token]
	if !ok {
		return hostErrNotFound
	}
	if len(r.handles[pluginID]) >= maxUploadHandlesPerPlugin {
		return hostErrBusy
	}
	f, err := os.Open(path)
	if err != nil {
		logging.L().Infof("[wasm:%s] upload open failed: %v", pluginID, err)
		return hostErrInternal
	}
	if r.handles[pluginID] == nil {
		r.handles[pluginID] = map[int32]uploadHandle{}
	}
	h := r.next[pluginID]
	r.next[pluginID] = h + 1
	r.handles[pluginID][h] = uploadHandle{f: f, token: token}
	return h
}

func (r *uploadRegistry) get(pluginID string, handle int32) (*os.File, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	uh, ok := r.handles[pluginID][handle]
	return uh.f, ok
}

// releaseLocked removes a token's staged file and closes every handle on
// it; the caller removes/closes what it returns outside the lock.
func (r *uploadRegistry) releaseLocked(pluginID, token string) (path string, files []*os.File) {
	path, ok := r.staged[pluginID][token]
	if !ok {
		return "", nil
	}
	delete(r.staged[pluginID], token)
	for h, uh := range r.handles[pluginID] {
		if uh.token == token {
			files = append(files, uh.f)
			delete(r.handles[pluginID], h)
		}
	}
	return path, files
}

func closeAndRemove(path string, files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

// closeHandle closes the handle and consumes its token (the staged file is
// removed, other handles on it closed). Unknown handles are a no-op.
func (r *uploadRegistry) closeHandle(pluginID string, handle int32) {
	r.mu.Lock()
	uh, ok := r.handles[pluginID][handle]
	var path string
	var files []*os.File
	if ok {
		delete(r.handles[pluginID], handle)
		path, files = r.releaseLocked(pluginID, uh.token)
	}
	r.mu.Unlock()
	if ok {
		closeAndRemove(path, append(files, uh.f))
	}
}

// release consumes tokens staged for pluginID; unknown tokens (already
// closed, or another plugin's) are a no-op.
func (r *uploadRegistry) release(pluginID string, tokens []string) {
	for _, tok := range tokens {
		r.mu.Lock()
		path, files := r.releaseLocked(pluginID, tok)
		r.mu.Unlock()
		closeAndRemove(path, files)
	}
}

// CloseAll releases every upload staged for pluginID (WasmRuntime.Sync,
// when the plugin's module is dropped or reloaded).
func (r *uploadRegistry) CloseAll(pluginID string) {
	r.mu.Lock()
	staged := r.staged[pluginID]
	handles := r.handles[pluginID]
	delete(r.staged, pluginID)
	delete(r.handles, pluginID)
	r.mu.Unlock()
	for _, uh := range handles {
		_ = uh.f.Close()
	}
	for _, path := range staged {
		_ = os.Remove(path)
	}
}

// StageUpload hands the temp file at path, already written and closed, to
// pluginID's upload registry and returns the token its ui.action.ask
// payload carries. From a nil error on, the registry owns the file
// (ReleaseUploads, upload_close or the plugin's unload remove it); on an
// error the caller still owns — and must remove — it.
func StageUpload(pluginID, path string) (string, error) {
	tok, err := uploads.stage(pluginID, path)
	if err != nil {
		logging.L().Infof("[wasm:%s] upload staging refused: %v", pluginID, err)
	}
	return tok, err
}

// ReleaseUploads removes pluginID's staged uploads named by tokens (and
// closes any handle on them). Idempotent: the host calls it when the ask —
// or the job — that received them ends, whether or not the guest already
// called upload_close.
func ReleaseUploads(pluginID string, tokens []string) {
	uploads.release(pluginID, tokens)
}

// hostUploadOpen opens the staged upload named by the token in guest
// memory: a handle ≥ 0, or hostErrNotFound (unknown, already closed, or
// staged for another plugin), hostErrInvalid (malformed), hostErrBusy (the
// plugin holds its maximum of open handles).
func hostUploadOpen(ctx context.Context, m api.Module, tokPtr, tokLen uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if tokLen == 0 || tokLen > maxUploadTokenBytes {
		return hostErrInvalid
	}
	tok, ok := readGuest(m, tokPtr, tokLen)
	if !ok {
		return hostErrInvalid
	}
	return uploads.open(s.pluginID, string(tok))
}

// hostUploadRead is one sequential read from the handle's cursor, with
// import_file_read's plain read() semantics: at most dstCap bytes (and at
// most uploadReadBufCap) land in the guest buffer and the return value is
// how many; 0 at EOF; a negative hostErr* code otherwise. The destination
// is validated before the file is read, so a bad pointer loses nothing.
func hostUploadRead(ctx context.Context, m api.Module, handle int32, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	f, ok := uploads.get(s.pluginID, handle)
	if !ok {
		return hostErrNotFound
	}
	if dstCap == 0 {
		return hostErrInvalid
	}
	bufCap := min(dstCap, uploadReadBufCap)
	if _, ok := m.Memory().Read(dstPtr, bufCap); !ok {
		return hostErrInvalid
	}
	buf := make([]byte, bufCap)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		logging.L().Infof("[wasm:%s] upload read handle %d failed: %v", s.pluginID, handle, err)
		return hostErrInternal
	}
	if n == 0 {
		return 0
	}
	if !m.Memory().Write(dstPtr, buf[:n]) {
		return hostErrInvalid
	}
	return int32(n)
}

// hostUploadClose closes the handle and consumes its upload: the staged
// temp file is removed. Idempotent: an unknown or already-closed handle
// also returns 0.
func hostUploadClose(ctx context.Context, handle int32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	uploads.closeHandle(s.pluginID, handle)
	return 0
}
