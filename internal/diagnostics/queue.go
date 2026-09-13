package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// PendingDir is where flushed-but-unacknowledged batches live, one
// subdirectory per session, one JSON file per batch. Overridable in tests
// (same convention as issuereport.PendingDir); production sets it from
// paths.Data("diagnostics", "pending") in pages.Init. Flush creates it
// (os.MkdirAll) before the first write — ADR-0092 §3 spells this out
// because this pipeline has shipped the "paths.Data without MkdirAll" bug
// twice.
var PendingDir = "./data/diagnostics/pending"

// Capacity knobs (ADR-0092 §2/§3). Package vars, not consts, so the tests
// can shrink them; production never changes them.
var (
	// ringCap bounds the in-memory ring. At capacity the OLDEST event is
	// dropped and counted toward the next diagnostic_gap marker.
	ringCap = 2000
	// ringMaxAge bounds how long an event may sit in the ring before a
	// flush; anything older at flush time is dropped (counted) rather than
	// uploaded stale. The cloudsync tick (2 min) flushes far sooner in
	// normal operation — this only bites when the tick has stalled.
	ringMaxAge = 15 * time.Minute
	// maxPendingBatches bounds the on-disk queue across ALL sessions; at
	// capacity the oldest batch files are removed and their events counted
	// toward the next gap marker. 200 × ≤256 KiB is ≤ 50 MB worst case.
	maxPendingBatches = 200
)

// Cloud ingestion bounds — mirror ut-cloud/internal/diagnostics'
// MaxBatchEvents / MaxBatchBytes so a batch this till writes is never one
// the cloud refuses as oversized.
const (
	maxBatchEvents = 500
	// maxBatchBytes is the encoded events-array budget per batch, with
	// headroom under the cloud's 256 KiB whole-body cap for the envelope.
	maxBatchBytes = 200 << 10
)

type ringEntry struct {
	raw []byte
	at  time.Time
}

// ringBuffer is the bounded in-memory buffer Emit appends to; dropped
// counts evictions not yet reported by a gap marker.
type ringBuffer struct {
	mu      sync.Mutex
	events  []ringEntry
	dropped int
}

// ring is the process-wide instance.
var ring ringBuffer

// add appends one encoded event, evicting the oldest at capacity.
func (r *ringBuffer) add(raw []byte, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) >= ringCap {
		over := len(r.events) - ringCap + 1
		r.events = append(r.events[:0], r.events[over:]...)
		r.dropped += over
	}
	r.events = append(r.events, ringEntry{raw: raw, at: at})
}

func (r *ringBuffer) reset() {
	r.mu.Lock()
	r.events, r.dropped = nil, 0
	r.mu.Unlock()
}

// Batch is one on-disk pending batch.
type Batch struct {
	SessionID string
	Seq       int
	Path      string
	Events    []json.RawMessage
}

// batchFile is the on-disk JSON shape.
type batchFile struct {
	SessionID string            `json:"session_id"`
	Seq       int               `json:"seq"`
	CreatedAt time.Time         `json:"created_at"`
	Events    []json.RawMessage `json:"events"`
}

// Flush moves the ring's contents into on-disk batches for the active
// session: stale events are dropped (counted), any pending drop count
// becomes ONE leading diagnostic_gap marker, and the rest is chunked into
// cloud-sized batches with monotonic sequence numbers persisted through kv
// BEFORE each file is written (a crash between the two wastes a seq — the
// cloud tolerates gaps — but never reuses one). Called from the cloudsync
// tick only; never from a request path. A disk failure drops the affected
// events (counted toward the next gap) rather than growing memory without
// bound, and is returned for the tick to log.
func Flush(ctx context.Context, kv KV) error {
	s := current.Load()
	if s == nil {
		return nil
	}
	now := time.Now()
	ring.mu.Lock()
	var kept []json.RawMessage
	for _, e := range ring.events {
		if now.Sub(e.at) > ringMaxAge {
			ring.dropped++
			continue
		}
		kept = append(kept, e.raw)
	}
	dropped := ring.dropped
	ring.events, ring.dropped = nil, 0
	ring.mu.Unlock()

	if dropped > 0 {
		marker, err := marshal(Gap{DroppedCount: dropped}, now)
		if err == nil {
			kept = append([]json.RawMessage{marker}, kept...)
		}
	}
	if len(kept) == 0 {
		return nil
	}

	dir := filepath.Join(PendingDir, s.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		ring.mu.Lock()
		ring.dropped += len(kept)
		ring.mu.Unlock()
		return fmt.Errorf("diagnostics: create pending dir: %w", err)
	}

	for len(kept) > 0 {
		chunk, rest := takeChunk(kept)
		kept = rest
		seq, err := nextSeq(ctx, kv)
		if err != nil {
			ring.mu.Lock()
			ring.dropped += len(chunk) + len(kept)
			ring.mu.Unlock()
			return err
		}
		if err := writeBatch(dir, s.ID, seq, chunk, now); err != nil {
			ring.mu.Lock()
			ring.dropped += len(chunk) + len(kept)
			ring.mu.Unlock()
			return err
		}
	}
	if n := evictOverflow(); n > 0 {
		ring.mu.Lock()
		ring.dropped += n
		ring.mu.Unlock()
	}
	return nil
}

// takeChunk splits off the next cloud-sized batch: at most maxBatchEvents
// events and about maxBatchBytes encoded.
func takeChunk(events []json.RawMessage) (chunk, rest []json.RawMessage) {
	size := 0
	for i, e := range events {
		size += len(e) + 1
		if i > 0 && (i >= maxBatchEvents || size > maxBatchBytes) {
			return events[:i], events[i:]
		}
	}
	return events, nil
}

// nextSeq reads, increments and persists the per-session counter. The
// write lands before the caller writes the batch file, so a crash can only
// ever skip a number, never repeat one.
func nextSeq(ctx context.Context, kv KV) (int, error) {
	v, _, err := kv.Get(ctx, keyNextSeq)
	if err != nil {
		return 0, fmt.Errorf("diagnostics: read %s: %w", keyNextSeq, err)
	}
	seq, _ := strconv.Atoi(strings.TrimSpace(v))
	if err := kv.Set(ctx, keyNextSeq, strconv.Itoa(seq+1)); err != nil {
		return 0, fmt.Errorf("diagnostics: persist %s: %w", keyNextSeq, err)
	}
	return seq, nil
}

func batchFileName(seq int) string { return fmt.Sprintf("%012d.json", seq) }

// writeBatch persists one batch atomically (temp + rename, same shape as
// issuereport.writeMetaAtomic) so a crash mid-write never leaves a
// half-batch that Pending would try to upload.
func writeBatch(dir, sessionID string, seq int, events []json.RawMessage, now time.Time) error {
	out, err := json.Marshal(batchFile{SessionID: sessionID, Seq: seq, CreatedAt: now.UTC(), Events: events})
	if err != nil {
		return fmt.Errorf("diagnostics: encode batch %d: %w", seq, err)
	}
	tmp, err := os.CreateTemp(dir, ".batch-*.tmp")
	if err != nil {
		return fmt.Errorf("diagnostics: create temp batch: %w", err)
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
		return fmt.Errorf("diagnostics: write batch %d: %w", seq, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("diagnostics: fsync batch %d: %w", seq, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("diagnostics: close batch %d: %w", seq, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, batchFileName(seq))); err != nil {
		return fmt.Errorf("diagnostics: rename batch %d: %w", seq, err)
	}
	renamed = true
	return nil
}

// evictOverflow enforces maxPendingBatches across the whole pending tree,
// removing the oldest batches first, and returns how many EVENTS were
// discarded so the caller can fold them into the next gap marker.
func evictOverflow() int {
	batches, err := Pending()
	if err != nil || len(batches) <= maxPendingBatches {
		return 0
	}
	dropped := 0
	for _, b := range batches[:len(batches)-maxPendingBatches] {
		if err := os.Remove(b.Path); err == nil {
			dropped += len(b.Events)
		}
	}
	return dropped
}

// Pending lists every on-disk batch, oldest first (by session directory
// then sequence), so a backlog drains FIFO once online. A file that
// doesn't parse is skipped, never fatal — one corrupt batch must not block
// the rest (same stance as issuereport.Pending).
func Pending() ([]Batch, error) {
	sessions, err := os.ReadDir(PendingDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("diagnostics: list pending: %w", err)
	}
	var out []Batch
	for _, sd := range sessions {
		if !sd.IsDir() {
			continue
		}
		dir := filepath.Join(PendingDir, sd.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, f.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var bf batchFile
			if err := json.Unmarshal(raw, &bf); err != nil || bf.SessionID == "" {
				continue
			}
			out = append(out, Batch{SessionID: bf.SessionID, Seq: bf.Seq, Path: path, Events: bf.Events})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// Discard removes one batch — called once the cloud acknowledged it, or on
// the terminal 409 rejection.
func Discard(b Batch) error {
	if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Drop the now-empty session directory so Pending stays tidy; a
	// failure here is cosmetic.
	_ = os.Remove(filepath.Dir(b.Path))
	return nil
}

// drainSessionDir deletes every pending batch of one session in one step
// and reports how many batches/events went. Used by Stop/Revoke and the
// terminal-409 path.
func drainSessionDir(sessionID string) (batches, events int) {
	if strings.ContainsAny(sessionID, `/\`) || strings.Contains(sessionID, "..") {
		return 0, 0
	}
	dir := filepath.Join(PendingDir, sessionID)
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err == nil {
			var bf batchFile
			if json.Unmarshal(raw, &bf) == nil {
				events += len(bf.Events)
			}
		}
		batches++
	}
	if err := os.RemoveAll(dir); err != nil {
		logging.L().Warnf("diagnostics: drain session %s: %v", sessionID, err)
	}
	return batches, events
}

// DrainSession is drainSessionDir for callers outside this package (the
// terminal-409 upload path).
func DrainSession(sessionID string) (batches, events int) { return drainSessionDir(sessionID) }

// PendingSummary counts what a local stop would discard right now: on-disk
// batches for the active session, and events both on disk and still in the
// ring — for the Settings stop confirmation ("N unsent batches will be
// discarded").
func PendingSummary() (batches, events int) {
	s := current.Load()
	if s == nil {
		return 0, 0
	}
	all, _ := Pending()
	for _, b := range all {
		if b.SessionID == s.ID {
			batches++
			events += len(b.Events)
		}
	}
	ring.mu.Lock()
	events += len(ring.events)
	ring.mu.Unlock()
	return batches, events
}

// recentForReportMax caps the snapshot attached to a "Report an issue"
// bundle (ADR-0092 §6, till side): the ring plus the newest on-disk batch,
// newest-last, trimmed to this many events.
const recentForReportMax = 200

// RecentForIssueReport returns the active session's id and its most recent
// locally-buffered events for the issue-report attachment — nothing when
// no session is active. Read-only: nothing is consumed.
func RecentForIssueReport() (sessionID string, events []json.RawMessage) {
	s := current.Load()
	if s == nil {
		return "", nil
	}
	all, _ := Pending()
	var newest *Batch
	for i := range all {
		if all[i].SessionID == s.ID && (newest == nil || all[i].Seq > newest.Seq) {
			newest = &all[i]
		}
	}
	if newest != nil {
		events = append(events, newest.Events...)
	}
	ring.mu.Lock()
	for _, e := range ring.events {
		events = append(events, e.raw)
	}
	ring.mu.Unlock()
	if len(events) > recentForReportMax {
		events = events[len(events)-recentForReportMax:]
	}
	return s.ID, events
}
