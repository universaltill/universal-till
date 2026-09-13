package diagnostics

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// KV is the till's settings store as this package needs it — the exact
// Get/Set shape *settings.Store and *data.SettingsRepo already expose, so
// the session rows are ordinary till-local settings (ADR-0092 §1: they
// survive restart/reboot/update the same way every other setting does,
// with no new persistence mechanism).
type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

// Settings keys. All under one prefix so the All-settings card groups them.
const (
	keySessionID   = "diagnostics.session_id"
	keyActive      = "diagnostics.active"
	keyActivatedAt = "diagnostics.activated_at"
	keyNextSeq     = "diagnostics.next_seq"
	keyEndedReason = "diagnostics.ended_reason"
	keyEndedAt     = "diagnostics.ended_at"
	// keyStopReport holds the session id of a LOCAL stop the cloud has not
	// been told about yet — cleared once a sync push carried it (§1's
	// best-effort "reports the stop on the next cloudsync tick").
	keyStopReport = "diagnostics.stop_report"
)

// Why the last session ended — shown on the Settings card and persisted
// in keyEndedReason.
const (
	EndedStopped  = "stopped"  // a manager turned it off locally
	EndedRevoked  = "revoked"  // the diagnostic_mode_revoke directive
	EndedRejected = "rejected" // the cloud answered 409 session_not_active on upload
)

// Session is the locally active diagnostic session.
type Session struct {
	ID          string
	ActivatedAt time.Time
}

// current is the in-process flag Emit checks (nil = inactive). Loaded from
// the settings rows at boot (LoadSession) and flipped by Activate/Stop/
// Revoke — never read from the DB on the hot path.
var current atomic.Pointer[Session]

// Active reports whether a diagnostic session is locally active.
func Active() bool { return current.Load() != nil }

// Current returns the active session, if any.
func Current() (Session, bool) {
	s := current.Load()
	if s == nil {
		return Session{}, false
	}
	return *s, true
}

// LoadSession restores the in-process flag from the persisted rows. Call
// once at boot. An absent or inactive row leaves the till inactive; a row
// that is active but unreadable in some other way is logged and treated as
// inactive (fail safe: no capture rather than capture nobody can stop).
func LoadSession(ctx context.Context, kv KV) error {
	active, _, err := kv.Get(ctx, keyActive)
	if err != nil {
		return fmt.Errorf("diagnostics: read %s: %w", keyActive, err)
	}
	id, _, err := kv.Get(ctx, keySessionID)
	if err != nil {
		return fmt.Errorf("diagnostics: read %s: %w", keySessionID, err)
	}
	id = strings.TrimSpace(id)
	if active != "true" || id == "" {
		current.Store(nil)
		return nil
	}
	at, _, _ := kv.Get(ctx, keyActivatedAt)
	activatedAt, perr := time.Parse(time.RFC3339, strings.TrimSpace(at))
	if perr != nil {
		activatedAt = time.Now().UTC()
	}
	current.Store(&Session{ID: id, ActivatedAt: activatedAt})
	logging.L().Infof("diagnostics: session %s active since %s (restored at boot)", id, activatedAt.Format(time.RFC3339))
	return nil
}

// Activate records a freshly redeemed session (ADR-0092 §1) as settings
// rows and turns capture on. The rows are written BEFORE the in-process
// flag flips, so a write failure leaves the till inactive — a session the
// next boot could not see is not a session. The per-session sequence
// counter restarts at 0 and any leftover ring content from a previous
// session is discarded.
func Activate(ctx context.Context, kv KV, sessionID string, now time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("diagnostics: empty session id")
	}
	rows := []struct{ k, v string }{
		{keySessionID, sessionID},
		{keyActivatedAt, now.UTC().Format(time.RFC3339)},
		{keyNextSeq, "0"},
		{keyEndedReason, ""},
		{keyEndedAt, ""},
		{keyStopReport, ""},
		{keyActive, "true"}, // last, so a partial write never reads as active
	}
	for _, r := range rows {
		if err := kv.Set(ctx, r.k, r.v); err != nil {
			// Don't leave a half-written session that a later boot could
			// read as active with a stale id: force the flag off.
			_ = kv.Set(ctx, keyActive, "false")
			return fmt.Errorf("diagnostics: persist %s: %w", r.k, err)
		}
	}
	ring.reset()
	current.Store(&Session{ID: sessionID, ActivatedAt: now.UTC()})
	logging.L().Infof("diagnostics: session %s activated", sessionID)
	return nil
}

// StopResult reports what a Stop discarded, for the operator-facing
// confirmation and the audit row.
type StopResult struct {
	SessionID        string
	DiscardedBatches int
	DiscardedEvents  int
}

// Stop ends the local session immediately (ADR-0092 §1: works fully
// offline — this touches only the settings store and local disk), stops
// capture, and DISCARDS every not-yet-uploaded batch plus the ring
// (the §1/§3 local-stop exception to "delete only after ack"). reason is
// one of EndedStopped/EndedRevoked/EndedRejected; only a local stop
// (EndedStopped) queues a best-effort stop report for the next cloudsync
// tick — for the other two the cloud already knows.
//
// The in-process flag flips off FIRST so no further Emit lands while the
// rows and files are being cleared; a settings-write failure afterwards is
// returned but the till still stays stopped in-process (a stop the
// operator asked for must take effect now, not after the disk recovers —
// the next boot re-reads whatever the rows say, which is the accepted
// degradation).
func Stop(ctx context.Context, kv KV, reason string) (StopResult, error) {
	s := current.Swap(nil)
	res := StopResult{}
	if s == nil {
		return res, nil
	}
	res.SessionID = s.ID
	ring.mu.Lock()
	res.DiscardedEvents = len(ring.events)
	ring.events, ring.dropped = nil, 0
	ring.mu.Unlock()
	batches, events := drainSessionDir(s.ID)
	res.DiscardedBatches += batches
	res.DiscardedEvents += events

	var firstErr error
	set := func(k, v string) {
		if err := kv.Set(ctx, k, v); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("diagnostics: persist %s: %w", k, err)
		}
	}
	set(keyActive, "false")
	set(keyEndedReason, reason)
	set(keyEndedAt, time.Now().UTC().Format(time.RFC3339))
	if reason == EndedStopped {
		set(keyStopReport, s.ID)
	}
	logging.L().Infof("diagnostics: session %s ended (%s); discarded %d pending batches / %d events", s.ID, reason, res.DiscardedBatches, res.DiscardedEvents)
	return res, firstErr
}

// Revoke applies the cloud's diagnostic_mode_revoke directive (ADR-0092
// §1/§4): if sessionID is the locally active session it is stopped with
// the same disposition as a local stop (flag off, whole on-disk queue for
// that session drained in one step, so the till doesn't spend N more
// ticks rediscovering "not active" one 409 at a time). Any other session
// id still has its leftover files (if any) drained and is otherwise a
// clean no-op — directives are at-least-once, so a retry must not fail.
// Returns the short human message the cloud's result column shows.
func Revoke(ctx context.Context, kv KV, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("missing session_id")
	}
	if s, ok := Current(); ok && s.ID == sessionID {
		res, err := Stop(ctx, kv, EndedRevoked)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("diagnostic session %s revoked; discarded %d pending batches", sessionID, res.DiscardedBatches), nil
	}
	batches, _ := drainSessionDir(sessionID)
	if batches > 0 {
		return fmt.Sprintf("diagnostic session %s was not active locally; discarded %d leftover batches", sessionID, batches), nil
	}
	return fmt.Sprintf("diagnostic session %s was not active locally (already stopped)", sessionID), nil
}

// StopReport returns the device-report fragment describing a local stop
// the cloud has not been told about yet — {session_id, status, ended_at}
// — or ok=false when there is nothing to report. cloudsync attaches it to
// the heartbeat's device record on every tick until ClearStopReport.
func StopReport(ctx context.Context, kv KV) (map[string]any, bool) {
	id, _, err := kv.Get(ctx, keyStopReport)
	if err != nil || strings.TrimSpace(id) == "" {
		return nil, false
	}
	endedAt, _, _ := kv.Get(ctx, keyEndedAt)
	return map[string]any{
		"session_id": strings.TrimSpace(id),
		"status":     EndedStopped,
		"ended_at":   strings.TrimSpace(endedAt),
	}, true
}

// ClearStopReport marks the pending stop as delivered.
func ClearStopReport(ctx context.Context, kv KV) error {
	return kv.Set(ctx, keyStopReport, "")
}

// EndedInfo returns how and when the last session ended (both empty when
// no session ever ended, or one is active) — for the Settings card copy.
func EndedInfo(ctx context.Context, kv KV) (reason string, endedAt time.Time) {
	reason, _, _ = kv.Get(ctx, keyEndedReason)
	reason = strings.TrimSpace(reason)
	if at, _, _ := kv.Get(ctx, keyEndedAt); strings.TrimSpace(at) != "" {
		endedAt, _ = time.Parse(time.RFC3339, strings.TrimSpace(at))
	}
	return reason, endedAt
}
