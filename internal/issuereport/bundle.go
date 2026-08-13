// Package issuereport is the till-side half of ADR-0022 / spec 012: a
// manager captures a description (typed and/or spoken as a voice note, plus
// an optional screen recording) and the till's recent logs into a bundle
// saved on local disk, queued for upload to Universal Till Cloud on the
// cloudsync retry cadence (internal/cloudsync).
//
// Saving a bundle never talks to the network and always succeeds regardless
// of connectivity (same offline-first bar as the rest of this till) — upload
// is a separate, best-effort step.
package issuereport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/logging"
)

// PendingDir is where unuploaded bundles live. Overridable in tests (same
// convention as internal/pages' pluginPagesDir); production sets it from
// paths.Data("issue-reports", "pending") during Init.
var PendingDir = "./data/issue-reports/pending"

// newBundleID is overridable in tests that need a predictable directory
// name (e.g. to pre-arrange a write failure and assert the cleanup path).
var newBundleID = uuid.NewString

// Meta is the non-blob part of a captured bundle, persisted as meta.json
// alongside the recording files.
type Meta struct {
	ID   string `json:"id"`
	Note string `json:"note"`
	// Locale is the till's UI locale (e.g. "fa") at capture time, so
	// cloud-side transcription can request it from Whisper (ut-docs#397).
	// Empty when it was never captured (bundles saved before this field
	// existed, or the JS-less path) — the cloud treats that as auto-detect.
	Locale    string            `json:"locale"`
	CreatedAt time.Time         `json:"created_at"`
	Logs      []logging.Problem `json:"logs"`
	// SentFailCount counts how many times this bundle uploaded to the cloud
	// successfully but its local issue_reports_sent retention record
	// (ut-docs#348) failed to save — e.g. disk full, DB briefly read-only.
	// Not necessarily N *consecutive* ticks: an intervening cloud-upload
	// failure (offline) never touches this counter, so it's a lifetime
	// total across however many SaveSent attempts this bundle has actually
	// had. Persisted here (not tracked purely in memory) so it survives a
	// till restart between cloudsync ticks; see RecordSentFailure and
	// MaxSentFailCount (ut-docs#446). Omitted from JSON — and so zero — for
	// every bundle saved before this field existed.
	SentFailCount int `json:"sent_fail_count,omitempty"`
	// UploadFailCount counts failed cloud-upload attempts (the
	// uploadIssueReport call itself failing — offline, misconfigured,
	// unregistered) since the last successful upload — distinct from
	// SentFailCount above, which only counts after a successful upload.
	// Never capped or acted on by cloudsync itself (the upload keeps
	// retrying unboundedly, per offline-first — ADR-0003); it exists purely
	// so /my-reports (ut-docs#637) can tell a bundle that's merely waiting
	// for connectivity from one that has been failing for a while. Reset to
	// zero by ClearUploadFailure once the upload succeeds — usually moot
	// because a successful upload immediately discards the bundle, but NOT
	// moot when the immediately following SaveSent step then fails and the
	// bundle survives for retry (see uploadPendingIssueReports): without
	// the explicit clear, a since-recovered bundle would keep showing its
	// stale failure reason. See RecordUploadFailure, ClearUploadFailure and
	// UploadFailingThreshold.
	UploadFailCount int `json:"upload_fail_count,omitempty"`
	// UploadFailReason is a coarse, translatable classification of the most
	// recent upload failure — one of UploadFailReasonNotRegistered or
	// UploadFailReasonOther, never the raw error text (which can contain a
	// URL/host, isn't translated, and isn't meant for a shop owner to read
	// verbatim).
	UploadFailReason string `json:"upload_fail_reason,omitempty"`
}

// UploadFailingThreshold is how many consecutive cloud-upload failures (of
// the kind that can plausibly self-resolve, e.g. the shop being briefly
// offline) a bundle tolerates before /my-reports (ut-docs#637) presents it
// as "failing" rather than merely "pending". A single failure is normal —
// this till is offline-first and a bundle just sits pending until
// connectivity returns; only a *run* of failures across several cloudsync
// ticks (2-minute cadence — see cloudsync's tickIntervalNS — so this is
// roughly 10 minutes) is worth flagging to the shop owner.
// UploadFailReasonNotRegistered is exempted from this threshold; see its
// own doc.
const UploadFailingThreshold = 5

// UploadFailReasonNotRegistered classifies an upload failure caused by this
// till not being enrolled in the marketplace yet (uploadIssueReport's "not
// registered" error). Unlike a network hiccup this cannot self-resolve by
// merely waiting — it needs the shop owner to finish enrolment — so a
// bundle failing for this reason is presented as failing immediately;
// UploadFailingThreshold does not apply to it.
const UploadFailReasonNotRegistered = "not_registered"

// UploadFailReasonOther is every other upload failure reason (network
// trouble, a non-2xx response, a locally-missing attachment) — expected to
// self-resolve once the shop reconnects, so it only counts toward
// UploadFailingThreshold rather than failing immediately.
const UploadFailReasonOther = "other"

// MaxSentFailCount caps how many SaveSent failures a bundle tolerates
// before the till gives up on local retention and discards it (ut-docs#446)
// — without this cap, a persistently-failing local write re-uploads the
// bundle's full multipart body (including any audio/video) on every single
// cloudsync tick, forever. 5 is a small, explicit cap chosen for this
// mechanism specifically — not a reuse of another cap's exact semantics
// elsewhere in this codebase (e.g. internal/plugins/download_manager.go's
// maxRetries permits maxRetries+1 total attempts; this permits exactly
// MaxSentFailCount). This does NOT apply to the cloud upload itself failing
// (offline/network-down) — that keeps retrying unboundedly, per
// offline-first (ADR-0003).
const MaxSentFailCount = 5

// Bundle is one saved, not-yet-uploaded capture on disk.
type Bundle struct {
	Meta       Meta
	Dir        string
	AudioPath  string   // "" when no voice note was captured
	VideoPath  string   // "" when no screen recording was captured
	ImagePaths []string // empty when no screenshots were captured, else sorted capture order
}

// Save writes a new bundle to the pending queue. A description is required —
// either typed (note) or spoken (audio) — but not both; video and images are
// always optional. locale is the operator's UI locale at capture time
// (ut-docs#397) — empty is valid (never captured / JS-less path). images is
// written as image-0.png, image-1.png, … in call order (screenshot capture
// order, ut-docs#347).
func Save(note, locale string, audio, video []byte, images [][]byte) (string, error) {
	if strings.TrimSpace(note) == "" && len(audio) == 0 {
		return "", fmt.Errorf("issuereport: a description (typed note or voice recording) is required")
	}
	id := newBundleID()
	dir := filepath.Join(PendingDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("issuereport: create bundle dir: %w", err)
	}
	// Any failure past this point leaves a directory with no meta.json —
	// invisible to Pending() and so never reachable by Discard() either.
	// Clean it up here instead of leaking it permanently.
	if err := saveBundleFiles(dir, id, note, locale, audio, video, images); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return id, nil
}

func saveBundleFiles(dir, id, note, locale string, audio, video []byte, images [][]byte) error {
	if len(audio) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "audio.webm"), audio, 0o644); err != nil {
			return fmt.Errorf("issuereport: write audio: %w", err)
		}
	}
	if len(video) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "video.webm"), video, 0o644); err != nil {
			return fmt.Errorf("issuereport: write video: %w", err)
		}
	}
	for i, img := range images {
		name := fmt.Sprintf("image-%d.png", i)
		if err := os.WriteFile(filepath.Join(dir, name), img, 0o644); err != nil {
			return fmt.Errorf("issuereport: write %s: %w", name, err)
		}
	}
	meta := Meta{ID: id, Note: note, Locale: locale, CreatedAt: time.Now().UTC(), Logs: logging.Recent()}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("issuereport: encode meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o644); err != nil {
		return fmt.Errorf("issuereport: write meta: %w", err)
	}
	return nil
}

// Pending lists bundles waiting for upload, oldest-captured first (so a
// backlog uploads in the order it happened once connectivity returns). A
// bundle whose meta.json is missing or unreadable is skipped, not fatal —
// one corrupt directory must not block every other pending report.
func Pending() ([]Bundle, error) {
	entries, err := os.ReadDir(PendingDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("issuereport: list pending: %w", err)
	}
	var bundles []Bundle
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(PendingDir, e.Name())
		mb, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		var meta Meta
		if err := json.Unmarshal(mb, &meta); err != nil {
			continue
		}
		b := Bundle{Meta: meta, Dir: dir}
		if _, err := os.Stat(filepath.Join(dir, "audio.webm")); err == nil {
			b.AudioPath = filepath.Join(dir, "audio.webm")
		}
		if _, err := os.Stat(filepath.Join(dir, "video.webm")); err == nil {
			b.VideoPath = filepath.Join(dir, "video.webm")
		}
		if paths, err := filepath.Glob(filepath.Join(dir, "image-*.png")); err == nil && len(paths) > 0 {
			sort.Slice(paths, func(i, j int) bool { return imageIndex(paths[i]) < imageIndex(paths[j]) })
			b.ImagePaths = paths
		}
		bundles = append(bundles, b)
	}
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].Meta.CreatedAt.Before(bundles[j].Meta.CreatedAt)
	})
	return bundles, nil
}

// imageIndex extracts the numeric N out of a ".../image-N.png" path, for
// sorting screenshots back into capture order. filepath.Glob's own result
// order is lexicographic (image-1.png, image-10.png, image-2.png, …), which
// misorders any bundle with 10+ screenshots — sort on the parsed integer
// instead. A path that doesn't parse (shouldn't happen — Save is the only
// writer of these files) sorts last rather than panicking.
func imageIndex(path string) int {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".png")
	base = strings.TrimPrefix(base, "image-")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 1<<31 - 1
	}
	return n
}

// Discard removes a bundle from the pending queue — called once the cloud
// has confirmed receipt, so the till never re-uploads it.
func Discard(id string) error {
	return os.RemoveAll(filepath.Join(PendingDir, id))
}

// sentFailFallback tracks SentFailCount in memory for bundles whose durable
// write (writeMeta, below) has failed — independent review (ut-docs#446)
// caught that the counter's own persistence shares a failure domain with
// the failure it exists to protect against: on a genuinely full disk, the
// meta.json write fails for exactly the same reason SaveSent does, so a
// pure on-disk counter never advances and the cap never engages in the
// ticket's own headline scenario. This fallback closes that gap: it does
// NOT survive a till restart (an accepted, explicitly-noted degradation —
// once disk pressure is bad enough to fail this write, the harm this cap
// exists to bound is limited to this process's remaining lifetime rather
// than eliminated outright), but it does mean the cap still engages within
// one process's run even while the disk stays unwritable.
var sentFailFallback = struct {
	mu     sync.Mutex
	counts map[string]int
}{counts: map[string]int{}}

// RecordSentFailure increments and persists a bundle's SentFailCount,
// returning the new count. Called each time a bundle uploaded successfully
// but its local issue_reports_sent record failed to save — the caller
// compares the returned count against MaxSentFailCount to decide whether to
// keep retrying or give up.
//
// An unknown/unreadable bundle id (already discarded, or its meta.json is
// corrupt) is a real error — distinct from "this bundle exists but its
// write just failed" — so the caller can tell "nothing to count" from "this
// failure needs counting."
//
// The write itself is durable (fsync'd, via writeMeta) so the count
// survives a till restart in the common case. If the write fails — the
// disk itself is the problem, i.e. exactly the scenario this counter
// exists to catch — falls back to sentFailFallback so the cap still
// engages rather than looping forever; see that var's own doc for the
// trade-off.
func RecordSentFailure(id string) (int, error) {
	path := filepath.Join(PendingDir, id, "meta.json")
	mb, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("issuereport: read meta for %s: %w", id, err)
	}
	var meta Meta
	if err := json.Unmarshal(mb, &meta); err != nil {
		return 0, fmt.Errorf("issuereport: decode meta for %s: %w", id, err)
	}

	sentFailFallback.mu.Lock()
	// Fold in any progress accumulated purely in memory on a prior call
	// whose write failed — otherwise a disk that fails intermittently
	// (fails, then briefly recovers, then fails again) would lose the
	// fallback's count the moment one write happens to succeed.
	if fb := sentFailFallback.counts[id]; fb > meta.SentFailCount {
		meta.SentFailCount = fb
	}
	meta.SentFailCount++
	sentFailFallback.mu.Unlock()

	if err := writeMeta(path, meta); err != nil {
		sentFailFallback.mu.Lock()
		sentFailFallback.counts[id] = meta.SentFailCount
		sentFailFallback.mu.Unlock()
		return meta.SentFailCount, nil
	}

	sentFailFallback.mu.Lock()
	delete(sentFailFallback.counts, id)
	sentFailFallback.mu.Unlock()
	return meta.SentFailCount, nil
}

// RecordUploadFailure increments and persists a bundle's UploadFailCount and
// records its most recent failure's reason (UploadFailReasonNotRegistered or
// UploadFailReasonOther). Called each time uploadIssueReport fails for a
// pending bundle. Unlike RecordSentFailure, nothing here is capped or gates
// a destructive action — the upload itself keeps retrying unboundedly per
// offline-first (ADR-0003); this count only decides how the bundle is
// *presented* on /my-reports. So, unlike RecordSentFailure, this
// deliberately has no in-memory fallback for a failing write: undercounting
// here only delays when a stuck report gets flagged as failing, it never
// loses data or leaves a retry loop unbounded.
//
// An unknown/unreadable bundle id (already discarded, or its meta.json is
// corrupt) is a real error, same distinction RecordSentFailure makes.
func RecordUploadFailure(id, reason string) (int, error) {
	path := filepath.Join(PendingDir, id, "meta.json")
	mb, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("issuereport: read meta for %s: %w", id, err)
	}
	var meta Meta
	if err := json.Unmarshal(mb, &meta); err != nil {
		return 0, fmt.Errorf("issuereport: decode meta for %s: %w", id, err)
	}
	meta.UploadFailCount++
	meta.UploadFailReason = reason
	if err := writeMeta(path, meta); err != nil {
		return 0, fmt.Errorf("issuereport: persist upload-fail count for %s: %w", id, err)
	}
	return meta.UploadFailCount, nil
}

// ClearUploadFailure resets a bundle's UploadFailCount/UploadFailReason back
// to zero. Called once a bundle's cloud upload succeeds (independent review,
// ut-docs#637): the bundle is usually discarded outright right after a
// successful upload, but if the immediately following SaveSent step then
// fails, the bundle survives on disk for the next tick's retry (see
// uploadPendingIssueReports) — without this, its LAST *failed* upload's
// stale reason (even UploadFailReasonNotRegistered, if this till was JUST
// enrolled) would keep rendering as failing on /my-reports despite having,
// in fact, already reached the cloud.
//
// A bundle that's already gone by the time this runs (discarded, or never
// existed) is not an error worth reporting — there's nothing left to clear,
// and the caller here always calls this immediately after its own
// successful upload of the very id it's about to either discard or retain.
func ClearUploadFailure(id string) error {
	path := filepath.Join(PendingDir, id, "meta.json")
	mb, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("issuereport: read meta for %s: %w", id, err)
	}
	var meta Meta
	if err := json.Unmarshal(mb, &meta); err != nil {
		return fmt.Errorf("issuereport: decode meta for %s: %w", id, err)
	}
	if meta.UploadFailCount == 0 && meta.UploadFailReason == "" {
		return nil // already clear — skip the write
	}
	meta.UploadFailCount = 0
	meta.UploadFailReason = ""
	if err := writeMeta(path, meta); err != nil {
		return fmt.Errorf("issuereport: clear upload-fail state for %s: %w", id, err)
	}
	return nil
}

// writeMeta is overridable in tests that need to simulate the durable write
// itself failing (e.g. disk full) without needing OS-level tricks — same
// convention as newBundleID above.
var writeMeta = writeMetaAtomic

// writeMetaAtomic durably persists meta to path: write to a sibling temp
// file, fsync it, then atomically rename over the target. A plain
// os.WriteFile has two gaps this closes (independent review, ut-docs#446):
//
//   - A write interrupted partway (disk full, power loss, SIGKILL) leaves a
//     truncated, unparsable meta.json. Pending() silently skips a bundle
//     whose meta.json doesn't parse (by design — one corrupt directory must
//     not block every other pending report), which would otherwise strand
//     that bundle's directory, and its media, unreachable forever: nothing
//     can ever learn its id again to Discard it. Rename is atomic on the
//     same filesystem — the target either ends up with the old content or
//     the new content, never a partial write.
//   - fsync makes the update durable across a power loss (plausible on a
//     till), rather than only surviving in the OS write-back cache.
func writeMetaAtomic(path string, meta Meta) error {
	out, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("issuereport: encode meta for %s: %w", meta.ID, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".meta-*.json.tmp")
	if err != nil {
		return fmt.Errorf("issuereport: create temp meta for %s: %w", meta.ID, err)
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("issuereport: write temp meta for %s: %w", meta.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("issuereport: fsync temp meta for %s: %w", meta.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("issuereport: close temp meta for %s: %w", meta.ID, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("issuereport: rename temp meta for %s: %w", meta.ID, err)
	}
	renamed = true
	return nil
}
