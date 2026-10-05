package pages

import (
	"crypto/rand"
	"crypto/sha256"
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

// importCommitLock (ut-docs#1510) closes the one duplicate-import gap
// takeStagedCatalogUpload's exclusivity doesn't cover: a DIRECT commit —
// "Import" pressed without ever previewing first, so there is no staged_id
// for two requests to race over. A double-tap there sends two independent
// POST /api/import requests, each carrying its own byte-identical copy of
// the uploaded file. For a row with neither barcode nor SKU there is no DB
// UNIQUE constraint to catch the resulting duplicate (items.sku stores NULL,
// and SQLite treats every NULL as distinct under UNIQUE) — the only place
// left to stop it is before either request starts inserting rows.
//
// The guard is a content hash, not a client-supplied token: two requests
// racing to commit are recognised as "the same import" because they carry
// identical bytes, which is exactly what a double-tap (or a second browser
// tab still holding the same file selection) produces. A deliberate second
// import of a DIFFERENT file, or a re-import of the same file well after the
// first finished, both hash differently in time or content and proceed
// normally — this only rejects a genuine overlap.
var (
	importCommitMu   sync.Mutex
	importCommitLock = map[string]time.Time{}
)

// sampleDataMu serialises POST /api/import/sample-data's "already loaded?"
// check, seed and audit (ut-docs#3709 review N1). Unlike importCommitLock,
// which rejects a same-content duplicate, this one waits: the second of two
// concurrent taps then finds the data loaded and says so, rather than both
// seeding and both auditing sample_data_loaded. The seed takes well under a
// second, so a waiting request is never held long.
var sampleDataMu sync.Mutex

// sampleDataAfterCountSync is a test-only seam, nil in every real build:
// called while sampleDataMu is held, right after the "already loaded?" read,
// so TestImportSampleData_ConcurrentLoadsAuditOnce can park one request
// there and prove a concurrent one cannot get past the same read.
var sampleDataAfterCountSync func()

// importCommitLockTTL bounds how long a hash may be held: normally released
// by the handler's own defer the moment its commit finishes, this is only
// the backstop for a request that panics or whose goroutine is killed
// without unwinding — matching stagedCatalogTTL's role for the preview
// registry above.
const importCommitLockTTL = 2 * time.Minute

// importCommitReserveSync is a test-only synchronization seam (ut-docs#1725),
// nil in every real build (no-op — the nil check at the one call site in
// import_page.go is the only cost in production). The import commit handler
// calls it, if set, right after a successful reserveImportCommit (i.e.
// while still holding the reservation, before releaseImportCommit's defer
// fires) so a test can pause a request there and deterministically prove a
// second, concurrent same-content request is rejected while the first is in
// flight — see TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond for
// why that can't be left to two goroutines racing an OS scheduler, and for
// the full history. Unlike pluginTaxRateAsker's cacheMax (tax_hook.go), a
// per-instance field that can never leak between tests, this is a package
// global: safe today because nothing in this package calls t.Parallel(),
// but the first test that does needs its own seam, not this one shared.
var importCommitReserveSync func()

// reserveImportCommit claims exclusive rights to commit the exact bytes
// hashed as key. false means another commit with the same content is
// already in flight — the caller must reject the request, not proceed.
func reserveImportCommit(hash string) bool {
	importCommitMu.Lock()
	defer importCommitMu.Unlock()
	for k, t := range importCommitLock {
		if time.Since(t) > importCommitLockTTL {
			delete(importCommitLock, k)
		}
	}
	if _, inFlight := importCommitLock[hash]; inFlight {
		return false
	}
	importCommitLock[hash] = time.Now()
	return true
}

// releaseImportCommit hands the hash back once a reserved commit's request
// has finished, success or failure — always via defer, right after a
// successful reserveImportCommit.
func releaseImportCommit(hash string) {
	importCommitMu.Lock()
	defer importCommitMu.Unlock()
	delete(importCommitLock, hash)
}

// hashImportUpload hashes the upload's full bytes for reserveImportCommit,
// leaving file re-wound to the start for every downstream read (sniffing,
// staging, parsing) — the same seek-then-read contract stageCatalogUpload
// above already relies on.
func hashImportUpload(file io.ReadSeeker) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek upload to hash: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hash upload: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek upload after hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// forceableImportIssue is ut-docs#601's explicit allow-list: the issue types
// an operator may force-include from the preview's problem grid, each with
// the correction field(s) that make the row importable, in the order they
// must be supplied. An ALLOW-list on purpose, never a deny-list: any other
// issue code — the integrity-sensitive skips (duplicate/already-in-catalog),
// and any issue type catimport grows in the future — defaults to skip-only,
// no matter what the client submits.
//
// IssueMissingNameAndBadPrice (ut-docs#1713) needs BOTH corrections at
// once — a row missing only its name never silently ships a price of 0 by
// having just one of the two fields.
func forceableImportIssue(issue string) (fields []string, ok bool) {
	switch issue {
	case catimport.IssueMissingName:
		return []string{"name"}, true
	case catimport.IssueBadPrice:
		return []string{"price"}, true
	case catimport.IssueMissingNameAndBadPrice:
		return []string{"name", "price"}, true
	}
	return nil, false
}
