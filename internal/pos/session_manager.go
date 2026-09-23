package pos

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// SessionBasketManager holds one independent *Service per table-bound
// self-order guest session (ADR-0103, ut-docs#2261). ADR-0020's
// common.Deps.KioskEngine is ONE till-process-global basket — right for a
// single physical kiosk, but table-QR ordering (ut-docs#815) needs several
// tables' guests ordering from their own phones at the same time, each with
// their own basket. This manager is the "N instances instead of 1" half of
// that: Service itself is unchanged, there are just more of them, each
// reachable only by an opaque, unguessable token the guest's browser carries
// in an HttpOnly cookie (internal/pages/self_order_page.go's
// selfOrderEngine).
//
// Sessions are in-memory only — never persisted rows — exactly like
// KioskEngine already is (a session does not survive a process restart, same
// as today's single kiosk basket does not). Because the map is keyed by
// anonymous, LAN-reachable requests, it is bounded two ways: a session is
// removed on completed checkout, and Sweep evicts anything idle past a
// threshold (internal/pages/self_order_session_sweep.go drives it).
//
// Locking: mu is a plain, NON-reentrant Mutex following Service's own
// single-lock convention (see the Service struct's doc comment) — every
// exported method takes it exactly once at its own top and calls only
// unexported helpers that assume it is held. Lock order is manager.mu ->
// Service.mu (TableOwner/HasItems/SetConfig call Service methods while
// holding mu); a Service never calls back into the manager, so that order
// can't invert.
type SessionBasketManager struct {
	mu       sync.Mutex
	sessions map[string]*sessionBasket
	factory  func() *Service
	// clock is the time source for each session's last-seen stamp (Create
	// and Get). time.Now in production; same-package tests override it so
	// Sweep's idle math can be exercised without sleeping — the same
	// injectable-`now` idea OrderTrackingVisible uses, kept as a field here
	// because the stamping happens inside Create/Get, not at the call site.
	clock func() time.Time
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

// newSessionToken mints the opaque session id: 16 bytes of crypto/rand as
// lowercase hex, the identical shape internal/data's newTrackingToken
// established for this codebase's other anonymous-customer-facing token
// (the /o/{token} order-tracking link). Generated here directly rather than
// reused from internal/data because these sessions never touch the DB.
// crypto/rand.Read never returns an error (Go >= 1.24: it blocks or crashes
// the process rather than hand back weak bytes), so there is no error path.
func newSessionToken() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// MaxLiveSelfOrderSessions bounds the manager's total live-session count
// (ut-docs#2432). The map is keyed by anonymous, LAN-reachable requests —
// GET /self-order?table=<id> mints a new session on every cookieless hit
// that isn't blocked by the busy guard — so nothing but this cap and the
// idle Sweep (up to 2h away, see self_order_session_sweep.go) bounds its
// size; a tight request loop against one table's QR can exhaust memory on
// a Pi-class till long before Sweep ever runs. A real shop's simultaneous
// table/guest-device count realistically tops out in the dozens to low
// hundreds; 500 is generous headroom above that while still bounding
// worst-case memory to a small, fixed number of lightweight *Service
// instances (no DB connections, no goroutines per session).
const MaxLiveSelfOrderSessions = 500

// Create mints a new session and returns its token and fresh *Service, or
// ok=false once the manager is at MaxLiveSelfOrderSessions live sessions AND
// every one of them already holds real order items — the caller must treat
// a false ok as "nothing was created" and never use the zero-value
// token/Service returned alongside it.
//
// At the cap, Create first evicts the least-recently-seen EMPTY session
// (independent review of ut-docs#2432's first fix: capping the map alone
// let one source hold the whole shop's ordering hostage — mint 20
// cookieless, itemless sessions a minute, well under the 2h idle-sweep
// window, and every table's guests see "busy" forever). An empty session
// has never had an item added to it, so evicting one loses nothing a guest
// would notice — the same "nothing to lose" logic the busy guard already
// applies to an abandoned cart (bindSelfOrderTableSession's
// TableOwnerActive check). Only when EVERY live session actually holds
// items — the shop is genuinely at MaxLiveSelfOrderSessions real
// concurrent orders — does Create refuse, which is the true memory bound
// this cap exists to enforce.
func (m *SessionBasketManager) Create() (string, *Service, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= MaxLiveSelfOrderSessions && !m.evictOldestEmptyLocked() {
		return "", nil, false
	}
	svc := m.factory()
	token := newSessionToken()
	for _, taken := m.sessions[token]; taken; _, taken = m.sessions[token] {
		token = newSessionToken() // 2^128 space — practically unreachable, but never overwrite a live session
	}
	m.sessions[token] = &sessionBasket{svc: svc, lastSeen: m.clock()}
	return token, svc, true
}

// evictOldestEmptyLocked removes the least-recently-seen session whose
// basket holds no items, freeing one slot for Create at the cap. Reports
// whether it found one to evict; mu must already be held. O(n) over live
// sessions — n is capped at MaxLiveSelfOrderSessions, so this is bounded
// work, not unbounded scan growth.
func (m *SessionBasketManager) evictOldestEmptyLocked() bool {
	var oldestToken string
	var oldestSeen time.Time
	found := false
	for token, sb := range m.sessions {
		if sb.svc.Basket().ItemCount() > 0 {
			continue
		}
		if !found || sb.lastSeen.Before(oldestSeen) {
			oldestToken, oldestSeen, found = token, sb.lastSeen, true
		}
	}
	if !found {
		return false
	}
	delete(m.sessions, oldestToken)
	return true
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
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

// TableOwner finds a live session currently bound to tableID (any session
// whose Service.TableID() matches) — the unfiltered lookup behind
// TableOwnerActive below (ADR-0103 Decision 4, narrowed by ut-docs#2261
// review finding B1): TableOwnerActive is what the page's busy guard
// actually calls. An empty tableID never matches (an unbound session has
// TableID "", and "which session owns no table" is not a meaningful
// question). If two sessions were ever bound to one table — reachable in
// practice since B1: an old session that went idle past
// selfOrderTableBusyMaxIdle stays bound but stops blocking a new scan,
// which can bind a second session to the same table — either may be
// returned; when both are non-empty the busy guard's own caller treats
// either as busy, so which one TableOwner(Active) happens to return is not
// observable through that path.
func (m *SessionBasketManager) TableOwner(tableID string) (string, *Service, bool) {
	if m == nil || tableID == "" {
		return "", nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, sb := range m.sessions {
		if sb.svc.TableID() == tableID {
			return token, sb.svc, true
		}
	}
	return "", nil, false
}

// TableOwnerActive is TableOwner narrowed to a session touched within
// maxIdle of now — the busy guard's own recency window (ut-docs#2261 review
// finding B1). TableOwner alone made an ABANDONED session hold its table
// hostage for the full Sweep threshold (originally 2h, chosen only to bound
// memory): a guest who adds one item then orders at the counter instead
// left that table's QR unscannable by anyone else for up to two hours, with
// no staff-facing way to clear it. A session untouched longer than maxIdle
// no longer counts as "holding" the table for this check — a fresh scan is
// let through — even though it stays in memory, keeps its basket, and can
// still be resumed by its OWN cookie (selfOrderSession's normal lookup is
// unaffected by this method) until Sweep actually evicts it. Same
// (token, *Service, bool) shape and empty-tableID/nil-manager handling as
// TableOwner.
func (m *SessionBasketManager) TableOwnerActive(tableID string, maxIdle time.Duration, now time.Time) (string, *Service, bool) {
	if m == nil || tableID == "" {
		return "", nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := now.Add(-maxIdle)
	for token, sb := range m.sessions {
		if sb.svc.TableID() == tableID && !sb.lastSeen.Before(cutoff) {
			return token, sb.svc, true
		}
	}
	return "", nil, false
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
	defer m.mu.Unlock()
	cutoff := now.Add(-maxIdle)
	n := 0
	for token, sb := range m.sessions {
		if sb.lastSeen.Before(cutoff) {
			delete(m.sessions, token)
			n++
		}
	}
	return n
}

// SetConfig pushes a new tax/service-charge Config to every live session —
// the sessions' twin of the `d.KioskEngine.SetConfig(newCfg)` every
// settings handler already does after a store-settings save, so a guest
// mid-order sees the same rates a kiosk checkout would after a settings
// change, never a stale boot-time config.
func (m *SessionBasketManager) SetConfig(cfg Config) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sb := range m.sessions {
		sb.svc.SetConfig(cfg)
	}
}

// HasItems reports whether any live session's basket holds at least one
// item — the sessions' twin of the `d.KioskEngine.Basket().ItemCount() > 0`
// check the unattended-update scheduler uses to avoid restarting the till
// under a customer mid-order.
func (m *SessionBasketManager) HasItems() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sb := range m.sessions {
		if sb.svc.Basket().ItemCount() > 0 {
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
