package pos

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// SessionBasketManager builds independent *Service baskets with the kiosk's
// factory (ADR-0103, ut-docs#2261). Table-QR ordering moved to the cloud
// (ADR-0159, ut-docs#4338), so nothing in production registers a session any
// more: NewDetached (the guest_order directive's basket) is the live use. The
// registered-session half (Get/Remove/Sweep/Len and the pages-level session
// cookie) is inert and is removed by a follow-up card.
//
// Sessions are in-memory only. Locking: mu is a plain, NON-reentrant Mutex —
// every exported method takes it at most once and never calls a Service method
// that can run a blocking plugin ask while holding it (SetConfig snapshots the
// live *Service list under mu and calls each one's SetConfig after releasing
// it, ut-docs#2435; whole batches are serialized by setConfigMu, ut-docs#2488).
// Lock order is setConfigMu -> manager.mu -> Service.mu, and a Service never
// calls back into the manager, so it cannot invert.
type SessionBasketManager struct {
	mu       sync.Mutex
	sessions map[string]*sessionBasket
	factory  func() *Service
	// setConfigMu serializes whole SetConfig batches (snapshot + dispatch)
	// against each other (ut-docs#2488): without it two racing calls'
	// per-session writes could interleave and leave live sessions on
	// different final configs. Now the call that acquires it last wins on
	// every session. Only SetConfig takes it; lock order is
	// setConfigMu -> mu -> Service.mu, and mu is still released before any
	// Service call. A SetConfig stuck in a slow plugin ask (bounded by the
	// WASM call timeout) delays a concurrent SetConfig, never Get.
	setConfigMu sync.Mutex
	// clock is the time source for each session's last-seen stamp (Get). time.Now in production; same-package tests override it so
	// Sweep's idle math can be exercised without sleeping — the same
	// injectable-`now` idea OrderTrackingVisible uses, kept as a field here
	// because the stamping happens inside Get, not at the call site.
	clock func() time.Time
	// abandon is the handler every session's Service gets (ut-docs#6601). Set
	// at boot by SetAbandonedTenderHandler, applied by build to each new
	// session. Atomic so build never takes m.mu.
	abandon atomic.Pointer[AbandonedTenderHandler]
}

// sessionBasket is one live guest session: its basket engine and when a
// request last resolved it.
type sessionBasket struct {
	svc      *Service
	lastSeen time.Time
}

// NewSessionBasketManager returns an empty manager. factory builds each new
// session's *Service — the caller (pages.Init) constructs it identically to
// how KioskEngine itself is built, so a session basket behaves exactly like
// the kiosk basket in every respect but lifetime and visibility; this
// package deliberately doesn't know about the pages-level tax/charge askers.
func NewSessionBasketManager(factory func() *Service) *SessionBasketManager {
	return &SessionBasketManager{
		sessions: map[string]*sessionBasket{},
		factory:  factory,
		clock:    time.Now,
	}
}

// SetAbandonedTenderHandler registers h on every live session and on every
// session built afterwards (ut-docs#6601). A session minted concurrently with
// this call may miss h; wiring is boot-only.
func (m *SessionBasketManager) SetAbandonedTenderHandler(h AbandonedTenderHandler) {
	if m == nil {
		return
	}
	m.abandon.Store(&h)
	m.mu.Lock()
	svcs := make([]*Service, 0, len(m.sessions))
	for _, sb := range m.sessions {
		svcs = append(svcs, sb.svc)
	}
	m.mu.Unlock()
	for _, svc := range svcs {
		svc.SetAbandonedTenderHandler(h)
	}
}

// build is factory plus the abandoned-tender handler. It takes no manager
// lock, so it is safe inside and outside m.mu.
func (m *SessionBasketManager) build() *Service {
	svc := m.factory()
	if svc != nil {
		if h := m.abandon.Load(); h != nil {
			svc.SetAbandonedTenderHandler(*h)
		}
	}
	return svc
}

// NewDetached builds one basket with the session factory WITHOUT
// registering it (ut-docs#4337, ADR-0159 §5): the guest_order directive
// prices a cloud guest order on a fresh *Service built exactly like a
// table-QR session's — same resolver, askers and current kiosk Config —
// that no cookie can reach and no sweep or cap counts. ok=false on a nil
// manager (a bare-Deps test harness) or a nil factory.
func (m *SessionBasketManager) NewDetached() (*Service, bool) {
	if m == nil || m.factory == nil {
		return nil, false
	}
	svc := m.build()
	return svc, svc != nil
}

// newSessionToken mints the opaque session id: 16 bytes of crypto/rand as
// lowercase hex. crypto/rand.Read never returns an error (Go >= 1.24), so
// there is no error path.
func newSessionToken() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// Create registers a new session and returns its token and fresh *Service.
// No production path calls it since ut-docs#4338 (table ordering moved to the
// cloud; the cap and the busy-table binding went with BindTable); it remains
// only so the pages tests can exercise config/abandon propagation to
// registered sessions until the follow-up removes the registered-session
// half of this type. Already in the deadcode baseline.
func (m *SessionBasketManager) Create() (string, *Service, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc := m.build()
	token := newSessionToken()
	for _, taken := m.sessions[token]; taken; _, taken = m.sessions[token] {
		token = newSessionToken()
	}
	m.sessions[token] = &sessionBasket{svc: svc, lastSeen: m.clock()}
	return token, svc, true
}

// Get returns the live session for token, refreshing its idle clock on a
// hit so a session that is actively being used is never swept. A nil
// manager, an empty token, or an unknown/swept token all miss.
func (m *SessionBasketManager) Get(token string) (*Service, bool) {
	if m == nil || token == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sessions[token]
	if !ok {
		return nil, false
	}
	sb.lastSeen = m.clock()
	return sb.svc, true
}

// Remove evicts the session for token (a completed checkout, or a browser
// that just minted a replacement session). Unknown tokens are a no-op.
func (m *SessionBasketManager) Remove(token string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	sb := m.sessions[token]
	delete(m.sessions, token)
	m.mu.Unlock()
	if sb != nil {
		abandonDropped(sb.svc, AbandonSessionEnded) // after unlock (ut-docs#6601)
	}
}

// Sweep evicts every session whose last Create/Get is more than maxIdle
// before now, and returns how many it removed. now is a parameter (the same
// injectable-time shape as OrderTrackingVisible) so the background loop
// passes time.Now() and tests pass whatever they need.
func (m *SessionBasketManager) Sweep(maxIdle time.Duration, now time.Time) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	cutoff := now.Add(-maxIdle)
	var evicted []*Service
	for token, sb := range m.sessions {
		if sb.lastSeen.Before(cutoff) {
			delete(m.sessions, token)
			evicted = append(evicted, sb.svc)
		}
	}
	m.mu.Unlock()
	// Hand off dropped tenders after unlock: the handler calls plugins
	// (ut-docs#6601).
	for _, svc := range evicted {
		abandonDropped(svc, AbandonSessionExpired)
	}
	return len(evicted)
}

// abandonDropped hands a dropped session's tender to the handler, recovering
// a panic so one bad hand-off never skips the other dropped sessions or kills
// the sweep goroutine (ut-docs#6601 review L1).
func abandonDropped(svc *Service, reason AbandonReason) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("self-order session tender hand-off panicked (%s): %v (ut-docs#6601)", reason, r)
		}
	}()
	svc.AbandonTender(reason)
}

// SetConfig pushes a new tax/service-charge Config to every live session —
// the sessions' twin of the `d.KioskEngine.SetConfig(newCfg)` every
// settings handler already does after a store-settings save, so a guest
// mid-order sees the same rates a kiosk checkout would after a settings
// change, never a stale boot-time config.
//
// Snapshots the live *Service list under m.mu, then calls each snapshotted
// Service's SetConfig AFTER releasing the lock (ut-docs#2435, same
// lock-scope class as ut-docs#2443/#2449): Service.SetConfig runs
// recomputeTotals under Service.mu, which can invoke a blocking plugin
// tax/charge-policy ask — holding m.mu across that call would serialize
// every other live session's Get/BindTable/etc. (all of which need m.mu)
// behind however long that ask takes. A session created after the snapshot
// is taken simply doesn't receive this particular SetConfig call, same as
// any other snapshot-then-call race in this file. Runs under setConfigMu
// (ut-docs#2488).
func (m *SessionBasketManager) SetConfig(cfg Config) {
	if m == nil {
		return
	}
	m.setConfigMu.Lock()
	defer m.setConfigMu.Unlock()
	m.mu.Lock()
	svcs := make([]*Service, 0, len(m.sessions))
	for _, sb := range m.sessions {
		svcs = append(svcs, sb.svc)
	}
	m.mu.Unlock()
	for _, svc := range svcs {
		svc.SetConfig(cfg)
	}
}

// SetItemImage points every live session's lines of itemID at imageURL
// (Service.SetItemImage) so a photo changed in the item editor mid-order
// shows on each table guest's cart (ut-docs#4959). Same snapshot-then-call
// shape as SetConfig: Service.SetItemImage can recompute totals (a plugin
// ask), so mu is released before any Service call.
func (m *SessionBasketManager) SetItemImage(itemID, imageURL string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	svcs := make([]*Service, 0, len(m.sessions))
	for _, sb := range m.sessions {
		svcs = append(svcs, sb.svc)
	}
	m.mu.Unlock()
	for _, svc := range svcs {
		svc.SetItemImage(itemID, imageURL)
	}
}

// HasItems reports whether any live session's basket holds at least one
// item — the sessions' twin of the `d.KioskEngine.HasItemsOrByHand()` check
// (ut-docs#3596) the unattended-update scheduler uses to avoid restarting
// the till under a customer mid-order.
//
// Deliberately priced-lines-only, unlike Service.HasItemsOrByHand
// (ut-docs#3607): a session's own *Service is only ever freshly minted by
// this manager's factory or reused via BindTable's mover path, and neither
// calls Service.RestoreHeld — the only place AddByHand gets populated — so
// an "add by hand" entry cannot reach a session's basket today. If a future
// change ever restores a held sale into a session's Service, this needs
// revisiting.
func (m *SessionBasketManager) HasItems() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sb := range m.sessions {
		if len(sb.svc.Lines()) > 0 {
			return true
		}
	}
	return false
}

// Len returns the number of live sessions.
func (m *SessionBasketManager) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}
