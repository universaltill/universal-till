package pages

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/catimport"
)

// Preview-time staging for POST /api/import (ut-docs#601): a preview
// persists the uploaded bytes to a temp file so the follow-up commit can
// re-read the byte-identical copy and apply the operator's per-row problem
// overrides against stable row indexes — instead of trusting the browser to
// re-send the same file. Same staged-file idiom as POST /api/data/import
// (import_dispatch.go): streamed copy, cap enforced on bytes actually
// written, cleaned up by whoever owns the copy. os.CreateTemp("") targets
// os.TempDir(), which always exists — mirroring import_dispatch.go's
// "ut-import-*.upload" staging, never a cwd-relative path.

// stagedCatalogEntry is one staged preview upload. The registry is
// in-process only (the till is a single process); the id is 128 bits of
// crypto/rand, so a client can only ever name a copy it was handed.
type stagedCatalogEntry struct {
	path   string
	staged time.Time
}

var (
	stagedCatalogMu      sync.Mutex
	stagedCatalogUploads = map[string]stagedCatalogEntry{}
)

// stagedCatalogTTL bounds how long an abandoned preview's staged copy may
// linger: a preview that is never committed (operator closed the page) has
// no other cleanup hook, so stale entries are pruned whenever a new preview
// stages a file.
const stagedCatalogTTL = time.Hour

// stageCatalogUpload copies the already-parsed upload into a staged temp
// file and registers it under a fresh random id. The caller keeps ownership
// of `file` (it is only read, from offset 0); the registry owns the staged
// copy until takeStagedCatalogUpload/discardStagedCatalogUpload consumes it
// or the TTL prune removes it.
func stageCatalogUpload(file io.ReadSeeker) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek upload for staging: %w", err)
	}
	tmp, err := os.CreateTemp("", "ut-import-stage-*.upload")
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	// Streamed copy, size-capped on bytes actually written — the same
	// shape import_dispatch.go stages with (and the same cap const).
	written, copyErr := io.Copy(tmp, io.LimitReader(file, maxImportFileSize+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("stage upload: copy=%v close=%v", copyErr, closeErr)
	}
	if written > maxImportFileSize {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("upload exceeds the %d-byte staging cap", maxImportFileSize)
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("generate staged id: %w", err)
	}
	id := hex.EncodeToString(raw[:])

	stagedCatalogMu.Lock()
	for k, e := range stagedCatalogUploads {
		if time.Since(e.staged) > stagedCatalogTTL {
			_ = os.Remove(e.path)
			delete(stagedCatalogUploads, k)
		}
	}
	stagedCatalogUploads[id] = stagedCatalogEntry{path: tmp.Name(), staged: time.Now()}
	stagedCatalogMu.Unlock()
	return id, nil
}

// takeStagedCatalogUpload removes id from the registry and hands the staged
// file's path — and its ownership, including removal — to the caller.
func takeStagedCatalogUpload(id string) (string, bool) {
	stagedCatalogMu.Lock()
	defer stagedCatalogMu.Unlock()
	e, ok := stagedCatalogUploads[id]
	if ok {
		delete(stagedCatalogUploads, id)
	}
	return e.path, ok
}

// restageCatalogUpload hands a taken staged copy back to the registry under
// its original id — for a commit that took the copy and then decided NOT to
// consume it after all (the ut-docs#970 currency-confirm detour: the
// operator's confirmed resubmit must re-read the same copy so their per-row
// overrides still apply, ut-docs#601). Take-then-restage rather than a
// non-removing peek keeps takeStagedCatalogUpload's exclusivity: between
// take and restage no other request can name the copy, so two concurrent
// commits can never both consume it. The id was just removed by take and is
// 128 bits of crypto/rand, so re-registering it can never double-register a
// live entry. The staged time restarts — the operator gets the full TTL to
// answer the confirm prompt, with the prune still the backstop if they
// abandon it.
func restageCatalogUpload(id, path string) {
	stagedCatalogMu.Lock()
	defer stagedCatalogMu.Unlock()
	stagedCatalogUploads[id] = stagedCatalogEntry{path: path, staged: time.Now()}
}

// discardStagedCatalogUpload consumes and deletes a staged copy that will
// never be committed (e.g. a re-preview superseding the previous one).
func discardStagedCatalogUpload(id string) {
	if path, ok := takeStagedCatalogUpload(id); ok {
		_ = os.Remove(path)
	}
}

// forceableImportIssue is ut-docs#601's explicit allow-list: the only two
// issue types an operator may force-include from the preview's problem
// grid, each with the correction field that makes the row importable. An
// ALLOW-list on purpose, never a deny-list: any other issue code — the
// integrity-sensitive skips (duplicate/already-in-catalog), and any issue
// type catimport grows in the future — defaults to skip-only, no matter
// what the client submits.
func forceableImportIssue(issue string) (field string, ok bool) {
	switch issue {
	case catimport.IssueMissingName:
		return "name", true
	case catimport.IssueBadPrice:
		return "price", true
	}
	return "", false
}
