package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// event_publish (ADR-0121 §3, build card 7c, ut-docs#3871): a plugin raises
// an event in its OWN namespace (`<manifest id>.<name>`, §2 + the
// 2026-10-03 amendment) so it can feed its own slots/ticks — never a core
// event, never another plugin's. Subscribers receive it through their
// existing entries' trigger_events (plugin_hooks); no new permission.
//
// Re-entrancy bound (the §3 row):
//   - Delivery is asynchronous: an accepted event is buffered on the
//     publishing instance's hostState and enqueued onto subscriber channels
//     only after that instance has exited (WasmRuntime.flushPublished), so
//     no subscriber ever runs inside the publishing call, and the flush
//     never calls a Blocking handler (EventBus.EnqueuePublished).
//   - Per-plugin rate: a token bucket, 20 tokens/s, burst 40, shared by every
//     instance of the plugin; over it → hostErrQuota.
//   - Hop count: core events are hop 0, a published event is the handled
//     event's hop + 1. A publish while handling a hop-3 event is dropped
//     with an audit row and a warning → hostErrQuota.
//   - A published event (hop > 0) is ordinary work: never the reserved
//     sale-path call slot, never the payment-gate/export/import deadline
//     floors (isSalePathCall, timeoutForEvent).
const (
	// maxPublishPayload is the payload cap (64 KiB); over it → hostErrInvalid.
	maxPublishPayload = 64 << 10
	// maxPublishTypeLen caps the event type, checked before it is read: an
	// id is ≤ 128 bytes, so 256 leaves room for the name. Without it a
	// guest could queue a type as large as its linear memory, and the
	// type is copied into audit rows and log lines.
	maxPublishTypeLen = 256
	// publishDropAuditInterval coalesces the hop-limit drop audit/log per
	// plugin, like channelFullWarnInterval (ipc.go): a handler looping at
	// the depth limit gets one row per second, not one per token.
	publishDropAuditInterval = time.Second
	// publishRatePerSec / publishBurst: the per-plugin token bucket.
	publishRatePerSec = 20
	publishBurst      = 40
	// maxPublishHop is the deepest hop an event can be published at: a
	// publish while handling an event already at this hop is dropped.
	maxPublishHop = 3
	// maxPendingPublishes bounds one instance's buffer (a long export or
	// import call keeps refilling the bucket until it exits) — the
	// subscriber channel's own capacity, so a full buffer can still land.
	maxPendingPublishes = 100
)

var (
	errEventNameMalformed = errors.New("not dot-separated lower-case segments")
	errEventNameCoreRoot  = errors.New("in a core event namespace")
	errEventNameForeign   = errors.New("outside the plugin's own namespace")
)

// checkPluginEventName applies ADR-0121 §2's plugin event namespace rule —
// shared by schedules[].event (manifest_abi3.go) and event_publish: the
// name is lower-case dot-separated segments, its first segment is not a
// core event root, and it starts with `<pluginID>.`. The own-id prefix is
// the real guard; the core-root check is the second, clearer one.
func checkPluginEventName(pluginID, event string) error {
	if !scheduleEventRe.MatchString(event) {
		return errEventNameMalformed
	}
	if root, _, _ := strings.Cut(event, "."); coreEventRoots[root] {
		return errEventNameCoreRoot
	}
	if !strings.HasPrefix(event, pluginID+".") {
		return errEventNameForeign
	}
	return nil
}

// publishRateLimiter is event_publish's per-plugin token bucket (20/s,
// burst 40). Hand-rolled — the module has no golang.org/x/time — with an
// injectable clock for tests. One bucket per plugin id, process-wide on the
// WasmRuntime, so parallel instances of one plugin share it.
type publishRateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*tokenBucket
	// dropAudited is when each plugin's last hop-limit drop was audited
	// (publishDropAuditInterval).
	dropAudited map[string]time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newPublishRateLimiter(now func() time.Time) *publishRateLimiter {
	if now == nil {
		now = time.Now
	}
	return &publishRateLimiter{now: now, buckets: map[string]*tokenBucket{}, dropAudited: map[string]time.Time{}}
}

// shouldAuditDrop reports whether pluginID's hop-limit drop is due an audit
// row and warning (the first, then at most one per
// publishDropAuditInterval), recording this one if so.
func (l *publishRateLimiter) shouldAuditDrop(pluginID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.dropAudited[pluginID]; ok && now.Sub(last) < publishDropAuditInterval {
		return false
	}
	l.dropAudited[pluginID] = now
	return true
}

// allow takes one token from pluginID's bucket, reporting false when empty.
func (l *publishRateLimiter) allow(pluginID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[pluginID]
	if !ok {
		b = &tokenBucket{tokens: publishBurst, last: now}
		l.buckets[pluginID] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * publishRatePerSec
		if b.tokens > publishBurst {
			b.tokens = publishBurst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// forget drops pluginID's bucket (the plugin was disabled or removed).
func (l *publishRateLimiter) forget(pluginID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.buckets, pluginID)
	delete(l.dropAudited, pluginID)
	l.mu.Unlock()
}

// hostEventPublish is `event_publish(type, payload) -> i32`: 0 when the
// event is queued for delivery after this instance exits; hostErrDenied
// outside the plugin's own namespace; hostErrInvalid for a type over 256
// bytes, a payload over 64 KiB or not JSON (empty → null); hostErrQuota over
// the rate limit, the instance's pending cap, or when dropped at the hop
// limit.
//
// Order: type length, rate, namespace, payload, hop, pending cap. Every call
// past the length check costs a token, refused or not, so a guest looping
// on a denied or dropped publish is held to 20/s — log lines and drop
// audits included (drop audits are further coalesced to 1/s).
func hostEventPublish(ctx context.Context, m api.Module, typePtr, typeLen, payloadPtr, payloadLen uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok || s.publishRate == nil {
		return hostErrInternal
	}
	if typeLen > maxPublishTypeLen {
		return hostErrInvalid
	}
	if !s.publishRate.allow(s.pluginID) {
		return hostErrQuota
	}
	rawType, ok := readGuest(m, typePtr, typeLen)
	if !ok {
		return hostErrInvalid
	}
	eventType := string(rawType) // string() copies out of guest memory
	if err := checkPluginEventName(s.pluginID, eventType); err != nil {
		logging.L().Infof("[wasm:%s] event_publish denied: %q is %v (ADR-0121 §2)", s.pluginID, eventType, err)
		return hostErrDenied
	}
	if payloadLen > maxPublishPayload {
		return hostErrInvalid
	}
	raw, ok := readGuest(m, payloadPtr, payloadLen)
	if !ok {
		return hostErrInvalid
	}
	payload := []byte("null")
	if len(raw) > 0 {
		if !json.Valid(raw) {
			return hostErrInvalid
		}
		payload = bytes.Clone(raw) // raw is a view into guest memory
	}
	if s.hop >= maxPublishHop {
		if s.publishRate.shouldAuditDrop(s.pluginID) {
			auditPublishDropped(ctx, s, eventType)
		}
		return hostErrQuota
	}
	if len(s.published) >= maxPendingPublishes {
		return hostErrQuota
	}
	s.published = append(s.published, Event{
		ID:        uuid.NewString(),
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
		Hop:       s.hop + 1,
	})
	return 0
}

// auditPublishDropped records a publish refused at the hop limit: plugin id,
// event type and depth — never the payload. The row stands for every drop
// by this plugin in the next publishDropAuditInterval, and says so.
func auditPublishDropped(ctx context.Context, s *hostState, eventType string) {
	logging.L().Warnf("[wasm:%s] event_publish %s dropped: raised while handling a published event at depth %d (limit %d, ADR-0121 §3; further drops within %s suppressed)", s.pluginID, eventType, s.hop, maxPublishHop, publishDropAuditInterval)
	details := map[string]any{"plugin_id": s.pluginID, "event_type": eventType, "depth": s.hop, "limit": maxPublishHop,
		"note": "further drops by this plugin within " + publishDropAuditInterval.String() + " are coalesced into this entry"}
	if err := data.NewPluginRepo(s.db).InsertAuditRaw(context.WithoutCancel(ctx), nil, "event_publish_dropped", "event", uuid.NewString(), details, time.Now()); err != nil {
		logging.L().Warnf("[wasm:%s] audit event_publish drop: %v", s.pluginID, err)
	}
}

// eventBus is the bus published events are delivered through: the test
// override, else the process-wide SharedBus (as Sync subscribes on).
func (w *WasmRuntime) eventBus(db *sql.DB) *EventBus {
	if w.bus != nil {
		return w.bus
	}
	return SharedBus(db)
}

// flushPublished hands the events an instance published to the bus. Called
// by handleEvent only after the instance has exited, whatever its outcome —
// the host already answered 0 for each — and only ever enqueues.
func (w *WasmRuntime) flushPublished(ctx context.Context, db *sql.DB, hs *hostState) {
	if len(hs.published) == 0 {
		return
	}
	if db == nil {
		logging.L().Warnf("[wasm:%s] event_publish: %d published events discarded (no database)", hs.pluginID, len(hs.published))
		hs.published = nil
		return
	}
	bus := w.eventBus(db)
	ctx = context.WithoutCancel(ctx) // the run deadline may be spent; the audit rows still land
	for _, ev := range hs.published {
		bus.EnqueuePublished(ctx, ev)
	}
	hs.published = nil
}

// isSalePathCall reports whether ev may take a reserved sale-path slot: a
// sale-path type raised by core (hop 0). A plugin-published event is
// ordinary work, whatever its name.
func isSalePathCall(ev Event) bool {
	return ev.Hop == 0 && isSalePathEvent(ev.Type)
}
