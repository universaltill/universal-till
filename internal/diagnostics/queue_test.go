package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func countType(t *testing.T, events []json.RawMessage, typ string) (n, dropped int) {
	t.Helper()
	for _, raw := range events {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("bad event %s: %v", raw, err)
		}
		if obj["type"] == typ {
			n++
			if d, ok := obj["dropped_count"].(float64); ok {
				dropped += int(d)
			}
		}
	}
	return n, dropped
}

// Ring at capacity: the OLDEST events go, and exactly ONE diagnostic_gap
// marker carrying the total dropped count is emitted per drop episode —
// never one per dropped event, never silent thinning.
func TestRing_CapacityDropsOldestAndEmitsOneGapMarker(t *testing.T) {
	kv := withActiveSession(t)
	origCap := ringCap
	ringCap = 5
	t.Cleanup(func() { ringCap = origCap })

	for i := 1; i <= 9; i++ {
		Emit(OrderStatus{OrderID: "o-" + strconv.Itoa(i), Status: "ready", Applied: true, Via: ViaLocal})
	}
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	gaps, dropped := countType(t, batches[0].Events, "diagnostic_gap")
	if gaps != 1 || dropped != 4 {
		t.Fatalf("gap markers = %d (dropped %d), want exactly 1 marker with dropped_count 4", gaps, dropped)
	}
	// The gap marker leads, then the 5 NEWEST survivors (o-5 … o-9).
	var first map[string]any
	_ = json.Unmarshal(batches[0].Events[0], &first)
	if first["type"] != "diagnostic_gap" {
		t.Fatalf("first event = %v, want the gap marker", first["type"])
	}
	var last map[string]any
	_ = json.Unmarshal(batches[0].Events[len(batches[0].Events)-1], &last)
	if last["order_id"] != "o-9" {
		t.Fatalf("last survivor = %v, want o-9 (newest kept, oldest dropped)", last["order_id"])
	}
	var second map[string]any
	_ = json.Unmarshal(batches[0].Events[1], &second)
	if second["order_id"] != "o-5" {
		t.Fatalf("oldest survivor = %v, want o-5", second["order_id"])
	}
}

// Events older than the ring's age cap at flush time are dropped (with a
// gap marker) rather than uploaded stale — the "time-capped" half.
func TestRing_AgeCapDropsStaleEvents(t *testing.T) {
	kv := withActiveSession(t)
	origAge := ringMaxAge
	ringMaxAge = time.Minute
	t.Cleanup(func() { ringMaxAge = origAge })

	Emit(Gap{DroppedCount: 1})
	ring.mu.Lock()
	ring.events[0].at = ring.events[0].at.Add(-2 * time.Minute) // age it past the cap
	ring.mu.Unlock()
	Emit(OrderStatus{OrderID: "fresh", Status: "ready", Applied: true, Via: ViaLocal})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches, _ := Pending()
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	gaps, dropped := countType(t, batches[0].Events, "diagnostic_gap")
	if gaps != 1 || dropped != 1 {
		t.Fatalf("gap markers = %d (dropped %d), want 1 marker for the 1 stale event", gaps, dropped)
	}
	if n, _ := countType(t, batches[0].Events, "order_status"); n != 1 {
		t.Fatalf("fresh event count = %d, want 1", n)
	}
}

// Flush writes under PendingDir/<session>/<seq>.json, creating the
// directory first (the paths.Data-without-MkdirAll bug class this pipeline
// has shipped twice), with monotonic sequence numbers persisted in the KV
// so a "restart" (fresh next-seq read from the same rows) never reuses one.
func TestFlush_CreatesDirAndPersistsMonotonicSeq(t *testing.T) {
	kv := withActiveSession(t)
	PendingDir = filepath.Join(t.TempDir(), "nested", "not", "yet", "created")

	Emit(Gap{DroppedCount: 1})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	Emit(Gap{DroppedCount: 2})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	// Nothing buffered → no empty batch written.
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("empty Flush: %v", err)
	}
	batches, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(batches) != 2 || batches[0].Seq != 0 || batches[1].Seq != 1 {
		t.Fatalf("batches = %+v, want seq 0 then 1", batches)
	}
	if batches[0].SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("SessionID = %q", batches[0].SessionID)
	}
	if v, _, _ := kv.Get(t.Context(), keyNextSeq); v != "2" {
		t.Fatalf("%s = %q, want 2", keyNextSeq, v)
	}

	// Restart: in-process state gone, only the rows + files remain.
	current.Store(nil)
	if err := LoadSession(t.Context(), kv); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	Emit(Gap{DroppedCount: 3})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("post-restart Flush: %v", err)
	}
	batches, _ = Pending()
	if len(batches) != 3 || batches[2].Seq != 2 {
		t.Fatalf("after restart: %+v, want a third batch with seq 2", batches)
	}

	// Ack → local copy deleted; the others stay.
	if err := Discard(batches[0]); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	batches, _ = Pending()
	if len(batches) != 2 || batches[0].Seq != 1 {
		t.Fatalf("after Discard: %+v", batches)
	}
}

// A new session restarts the sequence at 0 — seq is per session.
func TestActivate_ResetsSeqPerSession(t *testing.T) {
	kv := withActiveSession(t)
	Emit(Gap{DroppedCount: 1})
	_ = Flush(t.Context(), kv)
	_, _ = Stop(t.Context(), kv, EndedStopped)
	if err := Activate(t.Context(), kv, "sess-2", time.Now()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	Emit(Gap{DroppedCount: 1})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches, _ := Pending()
	if len(batches) != 1 || batches[0].Seq != 0 || batches[0].SessionID != "sess-2" {
		t.Fatalf("batches = %+v, want one seq-0 batch for sess-2", batches)
	}
}

// Large backlogs split into cloud-sized batches (MaxBatchEvents each).
func TestFlush_SplitsIntoBoundedBatches(t *testing.T) {
	kv := withActiveSession(t)
	origCap := ringCap
	ringCap = maxBatchEvents*2 + 10
	t.Cleanup(func() { ringCap = origCap })
	for i := 0; i < maxBatchEvents+1; i++ {
		Emit(Gap{DroppedCount: 1})
	}
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches, _ := Pending()
	if len(batches) != 2 || len(batches[0].Events) != maxBatchEvents || len(batches[1].Events) != 1 {
		t.Fatalf("got %d batches (sizes %v), want 500 + 1", len(batches), func() []int {
			var s []int
			for _, b := range batches {
				s = append(s, len(b.Events))
			}
			return s
		}())
	}
}

// On-disk queue at capacity: the OLDEST pending batches are removed and
// their event count folds into the next gap marker.
func TestDiskQueue_CapacityDropsOldestBatches(t *testing.T) {
	kv := withActiveSession(t)
	origMax := maxPendingBatches
	maxPendingBatches = 2
	t.Cleanup(func() { maxPendingBatches = origMax })
	for i := 0; i < 3; i++ {
		Emit(Gap{DroppedCount: 1})
		if err := Flush(t.Context(), kv); err != nil {
			t.Fatalf("Flush %d: %v", i, err)
		}
	}
	batches, _ := Pending()
	if len(batches) != 2 || batches[0].Seq != 1 {
		t.Fatalf("batches = %+v, want seq 1 and 2 (seq 0 evicted)", batches)
	}
	// The eviction is reported on the next flush as a gap.
	Emit(OrderStatus{OrderID: "o", Status: "ready", Applied: true, Via: ViaLocal})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	batches, _ = Pending()
	last := batches[len(batches)-1]
	if gaps, dropped := countType(t, last.Events, "diagnostic_gap"); gaps != 1 || dropped != 1 {
		t.Fatalf("eviction gap = %d markers / %d dropped, want 1/1", gaps, dropped)
	}
}

// Disk failure never surfaces as anything but a logged, returned error from
// Flush — Emit itself has no error path and never blocks; the events are
// counted as dropped (gap) rather than growing memory without bound.
func TestFlush_DiskFailureIsContainedAndEmitNeverFails(t *testing.T) {
	kv := withActiveSession(t)
	blocker := filepath.Join(t.TempDir(), "file-not-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	PendingDir = filepath.Join(blocker, "pending") // MkdirAll must fail here
	for i := 0; i < 10; i++ {
		Emit(Gap{DroppedCount: 1}) // must not panic, block or return
	}
	if err := Flush(t.Context(), kv); err == nil {
		t.Fatal("Flush against an unwritable directory reported success")
	}
	if !Active() {
		t.Fatal("a disk failure must not deactivate the session")
	}
	// The failed events were dropped, the ring is empty, and the NEXT
	// flush (disk healthy again) reports them as a gap.
	PendingDir = t.TempDir()
	Emit(Gap{DroppedCount: 1})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}
	batches, _ := Pending()
	if len(batches) != 1 {
		t.Fatalf("got %d batches after recovery, want 1", len(batches))
	}
	if gaps, dropped := countType(t, batches[0].Events, "diagnostic_gap"); gaps != 2 || dropped != 11 {
		// 1 marker for the 10 lost + the explicit Gap{1} event itself.
		t.Fatalf("gap markers = %d / dropped %d, want 2 / 11", gaps, dropped)
	}
}

// RecentForIssueReport hands the "Report an issue" bundle the newest
// buffered events (ring + last on-disk batch), capped, and nothing when no
// session is active.
func TestRecentForIssueReport(t *testing.T) {
	resetForTest(t)
	if id, evs := RecentForIssueReport(); id != "" || evs != nil {
		t.Fatalf("inactive: (%q, %v), want empty", id, evs)
	}
	kv := withActiveSession(t)
	for i := 0; i < 3; i++ {
		Emit(Gap{DroppedCount: 1})
	}
	_ = Flush(t.Context(), kv)
	Emit(Gap{DroppedCount: 2})
	id, evs := RecentForIssueReport()
	if id != "11111111-2222-3333-4444-555555555555" || len(evs) != 4 {
		t.Fatalf("(%q, %d events), want the session id and 4 events", id, len(evs))
	}
	// Reading for a report must not consume the ring.
	if b, e := PendingSummary(); b != 1 || e != 4 {
		t.Fatalf("PendingSummary after snapshot = (%d, %d), want (1, 4)", b, e)
	}
}
