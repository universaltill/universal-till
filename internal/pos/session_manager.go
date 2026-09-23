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
// exported method except BindTable takes it exactly once at its own top
// and calls only unexported helpers that assume it is held. Lock order is
// manager.mu -> Service.mu (TableOwner/HasItems call Service methods while
// holding mu, but only ever TableID()/Lines() — cheap lock/copy/unlock reads
// with no recompute and no plugin call, so this is a narrow exception, not a
// violation, of "no Service call under m.mu"). SetConfig used to be the one
// exception that mattered: its Service.SetConfig call triggers
// recomputeTotals, which can itself invoke a blocking plugin tax/
// charge-policy ask — the same lock-scope class BindTable (ut-docs#2443) and
// HasItems (ut-docs#2449) were already fixed for. Fixed for SetConfig too
// (ut-docs#2435): it snapshots the live *Service list under mu, then calls
// each one's SetConfig after releasing it, so no manager method still holds
// mu across a plugin round-trip.
//
// BindTable is the one exception, and takes mu up to three times (ut-docs#2443,
// review finding S1 on ut-docs#2434, and the round-2 review of that first
// fix that found it didn't go far enough): both `factory()` (which installs
// tax/charge-policy askers — installing either synchronously recomputes
// totals, which can invoke a blocking plugin ask) and `SetTable` can block
// on that same kind of ask, and BindTable's own busy-check loop needs mu
// too — holding mu across either blocking call would serialize every OTHER
// table's concurrent BindTable/Get behind it for the ask's duration. So a
// fresh Service is built BEFORE the first mu acquisition wherever the call
// shape lets that be decided up front (a nil mover — see BindTable's own
// comment for the one rare fallback where it can't), the claim itself is
// recorded on the manager's own sessionBasket inside one lock/unlock,
// SetTable then runs unlocked, and a final short lock/unlock writes back
// whatever the Service actually ended up holding (never blindly the
// requested tableID — SetTable can silently no-op for an all-takeaway
// basket) so sessionBasket.tableID stays the authoritative record for
// BindTable's own busy-check even when two calls sharing one ownToken (a
// double-tap) complete their unlocked SetTable calls out of order. A
// Service never calls back into the manager, so lock order still can't
// invert.
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
	// tableID is BindTable's own record of which table this session holds
	// — read and written under m.mu alone, with no Service call involved,
	// so recording or checking a claim never needs to touch svc while
	// m.mu is held (ut-docs#2443, review finding S1 on ut-docs#2434). It
	// is kept eventually consistent with svc.TableID() by a write-back
	// BindTable does right after its own (unlocked) SetTable call — NOT
	// a live mirror: TableOwner/TableOwnerActive below still read
	// sb.svc.TableID() directly (a deliberate, pre-existing choice — see
	// their own comments), so the two can disagree for the brief unlocked
	// window while a BindTable call's SetTable is still in flight. That
	// window is what makes S1's fix possible at all; BindTable's own
	// busy-check is what actually enforces one-session-per-table, and it
	// always reads this field, never svc.TableID().
	//
	// The eventual-consistency guarantee holds only for changes BindTable
	// itself makes. Nothing here re-syncs if svc's table is cleared some
	// OTHER way (e.g. an order-type change reaching applyTablePolicyLocked)
	// — today that path is blocked from ever reaching a table-bound
	// self-order session only by a clamp in a different package
	// (self_order_shop.go's takeaway-toggle guard, pinned by
	// TestSelfOrderShop_TableCheckout_TakeawayToggleCannotUnbindTable), not
	// by anything in this file. If that clamp is ever relaxed, this field
	// can strand a phantom claim (a table sb.tableID calls busy that no
	// Service actually holds) — flagged in round 2 of ut-docs#2443's
	// review, not yet fixed or filed as its own follow-up.
	tableID string
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
// basket holds no items, freeing one slot for Create/BindTable at the cap.
// Reports whether it found one to evict; mu must already be held. O(n) over
// live sessions — n is capped at MaxLiveSelfOrderSessions, so this is
// bounded work, not unbounded scan growth.
//
// The empty check is len(sb.svc.Lines()) == 0, deliberately NOT
// sb.svc.Basket().ItemCount() == 0 — same reasoning as BindTable's own
// empty check (see its doc comment, ut-docs#2444 review finding S1):
// Basket() calls recomputeTotals(), which can perform a blocking plugin
// ask, and this runs under m.mu (BindTable's fresh-mint path calls this
// with its own lock already held) — the exact class of bug ut-docs#2443
// closed elsewhere in this file. Lines() is a lock/copy/unlock with no
// recompute, so this scan never blocks on a plugin round-trip.
func (m *SessionBasketManager) evictOldestEmptyLocked() bool {
	var oldestToken string
	var oldestSeen time.Time
	found := false
	for token, sb := range m.sessions {
		if len(sb.svc.Lines()) > 0 {
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
// review finding B1). Reads sb.svc.TableID() directly, unlike BindTable's
// own busy-check (sb.tableID) — the two can disagree for the brief window
// while a BindTable call's own SetTable is still in flight, unlocked
// (ut-docs#2443 N2); this method and TableOwnerActive are read-only
// diagnostic/test queries, never the enforcement path, so that window is
// harmless here. An empty tableID never matches (an unbound session has
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
// maxIdle of now — a single-window simplification of the same recency
// principle BindTable's busy check below uses (ut-docs#2261 review finding
// B1; BindTable itself has used two item-count-dependent windows since
// ut-docs#2444, not one — see its own doc comment). TableOwner alone made an
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
// and the claim (move an existing session onto tableID, or mint a fresh
// one) run under one m.mu critical section, so no other goroutine's own
// BindTable call can land between "is this table free" and "claim it" —
// closing the exact check-then-act race that previously let two phones
// scanning the same table, before either added an item, both bind. The
// actual SetTable call, and any blocking plugin ask it can trigger, always
// runs AFTER that critical section releases mu (ut-docs#2443) — see the
// type's own Locking comment for why, and sessionBasket.tableID's comment
// for how the claim stays correct across that unlocked window.
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
// already bound to tableID and was touched within its own recency window of
// now — no item-count EXCLUSION: an EMPTY session holds its table exactly
// like a non-empty one, which is what actually closes the N3 race (the old
// guard's len(owner.Lines())>0 requirement was the gap two staggered,
// still-empty scans slipped through). But the window itself DOES depend on
// item count (ut-docs#2444, review finding S3 of ut-docs#2434's own
// record): a still-empty session uses emptyMaxIdle, a session holding at
// least one line uses maxIdle. emptyMaxIdle is deliberately much shorter —
// the race this atomicity closes only needs a window of
// milliseconds-to-seconds, so a long window on an EMPTY session bought
// nothing but a guest self-locking their own abandoned-and-forgotten table
// (e.g. re-scanning from a second in-app-browser cookie jar) for the full
// maxIdle with no staff-facing override. A session idle past its own window
// still never counts, the same recency principle TableOwnerActive already
// documents, so the B1 idle-recovery guarantee is unaffected: an abandoned
// session — empty or not — frees its table once idle past whichever window
// applies to it. Because ownToken is always excluded from the scan, an
// UNCONTESTED resume (the normal case — nobody else has touched this
// table) is still never busy.
//
// The empty check is len(sb.svc.Lines()) == 0, deliberately NOT
// sb.svc.Basket().ItemCount() == 0 (ut-docs#2444 review finding S1):
// Basket() calls recomputeTotals(), which on a session with a tax/
// charge-policy asker installed can perform a blocking plugin ask — and
// this whole loop runs under m.mu, the exact class of bug ut-docs#2443
// (finding S1 of a DIFFERENT card) just closed on the bind path. Lines()
// is a lock/copy/unlock with no recompute, so this scan never blocks on a
// plugin round-trip just to classify a candidate session — unlike
// sb.tableID (read with no Service call at all, for the SetTable-in-
// flight reason its own comment explains), Lines() DOES take Service.mu,
// but only ever briefly: nothing here waits on a plugin ask the way
// Basket() can, so it's a different, narrower exception to "no Service
// call under m.mu" than sb.tableID's, not a violation of it.
//
// A nil manager or empty tableID both report not-busy with no service and
// no token — same "there is nothing to check" contract TableOwner(Active)
// already use, even though today's only caller (bindSelfOrderTableSession)
// never reaches here with either.
func (m *SessionBasketManager) BindTable(tableID, tableLabel, ownToken string, mover *Service, maxIdle, emptyMaxIdle time.Duration, now time.Time) (token string, svc *Service, busy bool) {
	if m == nil || tableID == "" {
		return "", nil, false
	}
	// A nil mover means this call can only possibly mint a fresh session
	// (the common first-ever-scan case) — build that Service now, BEFORE
	// taking m.mu: m.factory() installs the tax/charge-policy askers, and
	// installing either synchronously recomputes totals, which can invoke
	// the same kind of blocking plugin ask SetTable itself can trigger
	// (ut-docs#2443 M1 — building it inside the lock, as a first cut of
	// this fix did, defeated this card's whole point for the mint path,
	// its most common one). Discarded unused if the table turns out busy.
	// A non-nil mover skips this: the common resume/move path never needs
	// a new Service at all, so never pays for one it won't use.
	var freshSvc *Service
	if mover == nil {
		freshSvc = m.factory()
	}
	m.mu.Lock()
	cutoff := now.Add(-maxIdle)
	emptyCutoff := now.Add(-emptyMaxIdle)
	for tok, sb := range m.sessions {
		if tok == ownToken {
			continue
		}
		if sb.tableID != tableID {
			continue
		}
		c := cutoff
		if len(sb.svc.Lines()) == 0 {
			c = emptyCutoff
		}
		if !sb.lastSeen.Before(c) {
			m.mu.Unlock()
			return "", nil, true
		}
	}
	// mover is only honoured when its own session is still actually live in
	// this manager — a Get a moment ago doesn't guarantee it still is
	// (concurrent Sweep/Remove). Falling through to mint instead of
	// silently "succeeding" on an orphaned *Service avoids leaving the
	// guest bound to no cookie at all.
	if sb, ok := m.sessions[ownToken]; mover != nil && ok {
		// Record the claim on the manager's own sessionBasket, and release
		// mu, BEFORE calling SetTable (ut-docs#2443, review finding S1 on
		// ut-docs#2434): SetTable -> recomputeTotals can invoke a blocking
		// plugin tax/charge-policy ask, and every other table's own
		// BindTable/Get needs mu too — holding it across that ask would
		// serialize every other guest's request behind this one's ask.
		// The claim recorded here under mu is what makes a concurrent
		// BindTable see busy, so unlocking before the SetTable call does
		// not reopen the check-then-act race ut-docs#2434 closed.
		//
		// sb.svc, not the caller's own mover — always the same live
		// Service in valid production use (mover IS sb.svc, resolved by
		// the caller a moment before this call), but acting on the
		// manager's own record rather than trusting the caller's copy of
		// it keeps that an invariant this method enforces, not one it
		// merely assumes (ut-docs#2443 N4).
		sb.tableID = tableID
		sb.lastSeen = m.clock()
		m.mu.Unlock()
		sb.svc.SetTable(tableID, tableLabel)
		// Write back whatever the Service actually ended up holding, not
		// the tableID this call requested (ut-docs#2443 M2/M3): SetTable
		// silently no-ops for an all-takeaway basket (Service.SetTable's
		// own hasDineInLine guard), which would otherwise leave sb.tableID
		// claiming a table the Service never actually took, permanently
		// blocking every other guest from it. The same write-back is also
		// what makes two calls racing on the SAME ownToken (a double-tap)
		// converge correctly: whichever SetTable call actually finishes
		// last is the one whose result lands here, matching what
		// sb.svc.TableID() itself will report from then on — without it,
		// the two calls' m.mu-protected claim writes and their unlocked
		// SetTable calls could complete in opposite orders and leave
		// sb.tableID and sb.svc.TableID() permanently disagreeing, which
		// is exactly the double-bind ut-docs#2434 closed, reopened via a
		// different path. Re-checks the session is still the same one
		// (not evicted by a concurrent Remove/Sweep) before writing.
		m.mu.Lock()
		if m.sessions[ownToken] == sb {
			sb.tableID = sb.svc.TableID()
		}
		m.mu.Unlock()
		return ownToken, sb.svc, false
	}
	if freshSvc == nil {
		// Rare fallback: mover was non-nil but its session was evicted
		// (Sweep/Remove) between the caller's Get and this call, so the
		// branch above didn't run. Minting here still builds the Service
		// under mu, same as before this card — accepted as a genuinely
		// rare race (a Get a moment ago doesn't guarantee the session is
		// still live), not the common mint path ut-docs#2443's fix is
		// actually about.
		freshSvc = m.factory()
	}
	// ut-docs#2432: the session-count cap applies only to this fresh-mint
	// path — never the mover branch just above, which reuses an existing
	// session and never grows len(m.sessions). Reached under the SAME m.mu
	// held continuously since the busy-check loop at the top, so this is
	// one atomic "is there room, and if not can I make room" step, not a
	// separate lock/unlock. See Create's doc comment for why evicting the
	// oldest EMPTY session first (rather than refusing outright) is what
	// stops one source flooding the map with empty sessions from locking
	// out every real guest at every table.
	if len(m.sessions) >= MaxLiveSelfOrderSessions && !m.evictOldestEmptyLocked() {
		m.mu.Unlock()
		return "", nil, true
	}
	svc = freshSvc
	token = newSessionToken()
	for _, taken := m.sessions[token]; taken; _, taken = m.sessions[token] {
		token = newSessionToken() // 2^128 space — practically unreachable, but never overwrite a live session
	}
	m.sessions[token] = &sessionBasket{svc: svc, lastSeen: m.clock(), tableID: tableID}
	m.mu.Unlock()
	svc.SetTable(tableID, tableLabel)
	// Same write-back reasoning as the mover branch above (ut-docs#2443
	// M2/M3) — a fresh mint can no-op its own SetTable too (an all-takeaway
	// default basket), and while two calls can't share a freshly-minted
	// token's ownToken (it doesn't exist until this call creates it), the
	// principle that sb.tableID must reflect what the Service actually
	// holds, never merely what was requested, is the same either way.
	m.mu.Lock()
	if sb := m.sessions[token]; sb != nil {
		sb.tableID = svc.TableID()
	}
	m.mu.Unlock()
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
//
// Snapshots the live *Service list under m.mu, then calls each snapshotted
// Service's SetConfig AFTER releasing the lock (ut-docs#2435, same
// lock-scope class as ut-docs#2443/#2449): Service.SetConfig runs
// recomputeTotals under Service.mu, which can invoke a blocking plugin
// tax/charge-policy ask — holding m.mu across that call would serialize
// every other live session's Get/BindTable/etc. (all of which need m.mu)
// behind however long that ask takes. A session created after the snapshot
// is taken simply doesn't receive this particular SetConfig call, same as
// any other snapshot-then-call race in this file.
func (m *SessionBasketManager) SetConfig(cfg Config) {
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
		svc.SetConfig(cfg)
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
