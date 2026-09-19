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
// Service.mu (TableOwner/HasItems/SetConfig/BindTable call Service methods
// while holding mu — BindTable's own SetTable call is the first MUTATING
// one, and per ut-docs#2434 review finding S1 it can itself trigger a
// blocking plugin tax/charge-policy ask, serializing every OTHER table's
// concurrent request behind it for that ask's duration; tracked as a
// follow-up rather than fixed here, see that card); a Service never calls
// back into the manager, so that order can't invert.
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

// Create mints a new session and returns its token and fresh *Service.
func (m *SessionBasketManager) Create() (string, *Service) {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc := m.factory()
	token := newSessionToken()
	for _, taken := m.sessions[token]; taken; _, taken = m.sessions[token] {
		token = newSessionToken() // 2^128 space — practically unreachable, but never overwrite a live session
	}
	m.sessions[token] = &sessionBasket{svc: svc, lastSeen: m.clock()}
	return token, svc
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
// maxIdle of now — the same recency window BindTable's busy check below
// uses (ut-docs#2261 review finding B1). TableOwner alone made an
// ABANDONED session hold its table hostage for the full Sweep threshold
// (originally 2h, chosen only to bound memory): a guest who adds one item
// then orders at the counter instead left that table's QR unscannable by
// anyone else for up to two hours, with no staff-facing way to clear it. A
// session untouched longer than maxIdle no longer counts as "holding" the
// table for this check — a fresh scan is let through — even though it
// stays in memory, keeps its basket, and can still be resumed by its OWN
// cookie (selfOrderSession's normal lookup is unaffected by this method)
// until Sweep actually evicts it. Same (token, *Service, bool) shape and
// empty-tableID/nil-manager handling as TableOwner. Kept as a read-only
// query for callers that just want to know who holds a table (tests,
// diagnostics) — the page's own busy guard uses BindTable below instead,
// since a separate check-then-act pair of calls is exactly the race
// ut-docs#2434 closed.
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

// BindTable atomically resolves one guest's scan of tableID (ut-docs#2434,
// ADR-0103 review finding N3, corrected in the ADR itself): the busy check
// and the bind (move an existing session onto tableID, or mint a fresh
// one) run under one m.mu critical section, so no other goroutine's own
// BindTable call can land between "is this table free" and "claim it" —
// closing the exact check-then-act race that previously let two phones
// scanning the same table, before either added an item, both bind.
//
// mover, if non-nil, is the CALLER'S OWN already-resolved live session
// (ownToken is its token) to move onto tableID rather than replace — the
// existing-cookie "guest scanned a different table, or is re-scanning
// their own current one (a resume)" case; a resume is simply a move where
// tableID already equals mover.TableID(), a no-op SetTable. mover == nil
// (ownToken == "") is the fresh-browser mint case. **The resume case is
// NOT special-cased by the caller ahead of this method** (fixed
// ut-docs#2434 review finding S4): routing every path — mint, move, AND
// resume — through this same critical section is what makes the guard
// atomic against a stale-then-resumed session racing a different phone's
// fresh bind, not just against two fresh binds.
//
// busy=true when a DIFFERENT live session (any token != ownToken) is
// already bound to tableID and was touched within maxIdle of now — no
// item-count exception: an EMPTY session holds its table exactly like a
// non-empty one now, which is what actually closes the N3 race (the old
// guard's len(owner.Lines())>0 requirement was the gap two staggered,
// still-empty scans slipped through). A session idle past maxIdle still
// never counts, the same recency window TableOwnerActive already
// documents, so the B1 idle-recovery guarantee is unaffected by dropping
// the item-count check: an abandoned EMPTY session frees its table after
// maxIdle exactly as an abandoned non-empty one already did. Because
// ownToken is always excluded from the scan, an UNCONTESTED resume (the
// normal case — nobody else has touched this table) is still never busy.
//
// A nil manager or empty tableID both report not-busy with no service and
// no token — same "there is nothing to check" contract TableOwner(Active)
// already use, even though today's only caller (bindSelfOrderTableSession)
// never reaches here with either.
func (m *SessionBasketManager) BindTable(tableID, tableLabel, ownToken string, mover *Service, maxIdle time.Duration, now time.Time) (token string, svc *Service, busy bool) {
	if m == nil || tableID == "" {
		return "", nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := now.Add(-maxIdle)
	for tok, sb := range m.sessions {
		if tok == ownToken {
			continue
		}
		if sb.svc.TableID() == tableID && !sb.lastSeen.Before(cutoff) {
			return "", nil, true
		}
	}
	// mover is only honoured when its own session is still actually live in
	// this manager — a Get a moment ago doesn't guarantee it still is
	// (concurrent Sweep/Remove). Falling through to mint instead of
	// silently "succeeding" on an orphaned *Service avoids leaving the
	// guest bound to no cookie at all.
	if sb, ok := m.sessions[ownToken]; mover != nil && ok {
		mover.SetTable(tableID, tableLabel)
		sb.lastSeen = m.clock()
		return ownToken, mover, false
	}
	svc = m.factory()
	token = newSessionToken()
	for _, taken := m.sessions[token]; taken; _, taken = m.sessions[token] {
		token = newSessionToken() // 2^128 space — practically unreachable, but never overwrite a live session
	}
	svc.SetTable(tableID, tableLabel)
	m.sessions[token] = &sessionBasket{svc: svc, lastSeen: m.clock()}
	return token, svc, false
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
