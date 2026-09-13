package diagnostics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// memKV is an in-memory stand-in for the till's settings store (the same
// Get/Set shape *settings.Store and *data.SettingsRepo both expose).
type memKV struct {
	mu   sync.Mutex
	m    map[string]string
	fail bool
}

func newMemKV() *memKV { return &memKV{m: map[string]string{}} }

func (k *memKV) Get(_ context.Context, key string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}

func (k *memKV) Set(_ context.Context, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return errors.New("disk full")
	}
	k.m[key] = value
	return nil
}

// resetForTest clears the process-wide session/ring state and points the
// pending directory at a fresh temp dir, restoring everything on cleanup.
func resetForTest(t *testing.T) {
	t.Helper()
	origDir := PendingDir
	PendingDir = t.TempDir()
	current.Store(nil)
	ring.mu.Lock()
	ring.events, ring.dropped = nil, 0
	ring.mu.Unlock()
	t.Cleanup(func() {
		PendingDir = origDir
		current.Store(nil)
		ring.mu.Lock()
		ring.events, ring.dropped = nil, 0
		ring.mu.Unlock()
	})
}

// Activation persists the session as ordinary settings rows and a fresh
// process (LoadSession from the same rows) sees it active again — this is
// the "survives restart/reboot/update" property: no in-memory-only state.
func TestActivate_PersistsAndReloads(t *testing.T) {
	resetForTest(t)
	kv := newMemKV()
	at := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	if err := Activate(t.Context(), kv, "sess-1", at); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !Active() {
		t.Fatal("Active() = false right after Activate")
	}
	if v, _, _ := kv.Get(t.Context(), keySessionID); v != "sess-1" {
		t.Fatalf("%s = %q, want sess-1", keySessionID, v)
	}
	if v, _, _ := kv.Get(t.Context(), keyActive); v != "true" {
		t.Fatalf("%s = %q, want true", keyActive, v)
	}

	// "Restart": wipe the in-process flag, reload from the rows.
	current.Store(nil)
	if Active() {
		t.Fatal("Active() = true with no in-process state")
	}
	if err := LoadSession(t.Context(), kv); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	s, ok := Current()
	if !ok || s.ID != "sess-1" || !s.ActivatedAt.Equal(at) {
		t.Fatalf("Current() after reload = %+v ok=%v", s, ok)
	}
}

// A persisted row that says inactive (or is absent) must never come back
// as active after a reload.
func TestLoadSession_InactiveRowStaysInactive(t *testing.T) {
	resetForTest(t)
	kv := newMemKV()
	_ = kv.Set(t.Context(), keySessionID, "sess-old")
	_ = kv.Set(t.Context(), keyActive, "false")
	if err := LoadSession(t.Context(), kv); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if Active() {
		t.Fatal("an inactive persisted session was loaded as active")
	}
	if err := LoadSession(t.Context(), newMemKV()); err != nil || Active() {
		t.Fatalf("empty store: err=%v active=%v", err, Active())
	}
}

// Local stop: immediate, no network (the KV is the only dependency),
// clears the flag, discards the pending queue and reports what it
// discarded — and leaves a stop-report marker for the next cloudsync tick.
func TestStop_IsImmediateDiscardsPendingAndMarksReport(t *testing.T) {
	kv := withActiveSession(t)
	for i := 0; i < 3; i++ {
		Emit(Gap{DroppedCount: i + 1})
	}
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	Emit(Gap{DroppedCount: 99}) // still in the ring, not on disk
	batches, events := PendingSummary()
	if batches != 1 || events != 4 {
		t.Fatalf("PendingSummary = (%d, %d), want (1, 4)", batches, events)
	}

	res, err := Stop(t.Context(), kv, EndedStopped)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if Active() {
		t.Fatal("still active after Stop")
	}
	if res.DiscardedBatches != 1 || res.DiscardedEvents != 4 {
		t.Fatalf("Stop discarded (%d, %d), want (1, 4)", res.DiscardedBatches, res.DiscardedEvents)
	}
	if b, e := PendingSummary(); b != 0 || e != 0 {
		t.Fatalf("pending after Stop = (%d, %d), want empty", b, e)
	}
	if v, _, _ := kv.Get(t.Context(), keyActive); v != "false" {
		t.Fatalf("%s = %q after Stop, want false", keyActive, v)
	}
	if v, _, _ := kv.Get(t.Context(), keyEndedReason); v != EndedStopped {
		t.Fatalf("%s = %q, want %q", keyEndedReason, v, EndedStopped)
	}
	rep, ok := StopReport(t.Context(), kv)
	if !ok || rep["session_id"] != "11111111-2222-3333-4444-555555555555" || rep["status"] != "stopped" {
		t.Fatalf("StopReport = %v ok=%v", rep, ok)
	}
	if err := ClearStopReport(t.Context(), kv); err != nil {
		t.Fatalf("ClearStopReport: %v", err)
	}
	if _, ok := StopReport(t.Context(), kv); ok {
		t.Fatal("stop report still pending after ClearStopReport")
	}
	// Emit is a no-op again.
	Emit(Gap{DroppedCount: 1})
	if got := drainRing(t); len(got) != 0 {
		t.Fatal("Emit buffered an event after Stop")
	}
}

// Remote revoke: same disposition as a local stop for the matching session
// (flag off, whole on-disk queue for that session drained in one step) but
// NO stop report — the cloud is the one that told us. A revoke for some
// other session id still drains any leftover files for it and is a clean
// no-op otherwise, so a retried directive never fails.
func TestRevoke_MatchingSessionDrainsAndDoesNotReportStop(t *testing.T) {
	kv := withActiveSession(t)
	Emit(Gap{DroppedCount: 1})
	if err := Flush(t.Context(), kv); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	msg, err := Revoke(t.Context(), kv, "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if Active() {
		t.Fatal("still active after Revoke")
	}
	if b, _ := PendingSummary(); b != 0 {
		t.Fatalf("pending batches after Revoke = %d, want 0", b)
	}
	if msg == "" {
		t.Fatal("Revoke returned an empty result message")
	}
	if _, ok := StopReport(t.Context(), kv); ok {
		t.Fatal("a revoke must not queue a stop report")
	}
	if v, _, _ := kv.Get(t.Context(), keyEndedReason); v != EndedRevoked {
		t.Fatalf("%s = %q, want %q", keyEndedReason, v, EndedRevoked)
	}

	// Unknown session: no error, nothing changes.
	if _, err := Revoke(t.Context(), kv, "not-this-one"); err != nil {
		t.Fatalf("Revoke(unknown): %v", err)
	}
}

// A settings write failing during Activate must not leave the process
// believing it is active — a session the next boot cannot see is not a
// session.
func TestActivate_KVFailureDoesNotActivate(t *testing.T) {
	resetForTest(t)
	kv := newMemKV()
	kv.fail = true
	if err := Activate(t.Context(), kv, "sess-1", time.Now()); err == nil {
		t.Fatal("Activate succeeded against a failing store")
	}
	if Active() {
		t.Fatal("Active() = true after a failed Activate")
	}
}
