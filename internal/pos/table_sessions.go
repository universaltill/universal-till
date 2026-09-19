package pos

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// TableSessions is the per-table basket store behind table-QR self-ordering
// (ut-docs#2261). ut-docs#815 shipped /self-order?table=<id> on top of the
// ONE till-process-global kiosk engine (common.Deps.KioskEngine), so only
// one table's order could be in progress process-wide at a time — a second
// table's guest was fail-safed onto a "till busy" screen. This gives every
// table its own *Service instance, keyed by a random session token the
// guest's browser carries in a cookie, so concurrent tables never share
// state at all.
//
// Same-table rule (resolves the open question BA flagged on the card): any
// device that visits ?table=X while X has a live session JOINS that one
// session — the same browser revisiting, or a second guest at the same
// physical table scanning the same printed QR. One shared cart per table is
// how Toast/Square/Lightspeed's table ordering works, and it is the only
// reading under which a table can split a bill sensibly. Different tables
// are structurally isolated regardless (separate map entries, zero shared
// state).
//
// In-memory only, deliberately: KioskEngine's own basket has never been
// persisted (a till restart drops it), so a per-table store keeps the
// identical durability contract — no migration, no repo.
//
// Locking: *Service already self-serializes every exported method (see
// Service.mu's own doc comment), so nothing here wraps an engine call. The
// store's own mu guards only its two maps. A nil *TableSessions is an inert
// no-op for every method (bare-Deps test harnesses never wire one — same
// convention as the existing `d.KioskEngine != nil` guards).
type TableSessions struct {
	mu sync.Mutex
	// newEngine is the factory that mints one engine per table — captures the
	// resolver/taxAsker/chargeAsker/Config exactly the way pages.Init builds
	// kioskEngine itself, just reusable.
	newEngine func() *Service
	idleTTL   time.Duration
	byToken   map[string]*tableSession
	byTable   map[string]string // tableID -> token, secondary index
	// now is a test seam for Lookup/SessionForTable's activity stamps; Sweep
	// takes its `now` explicitly so callers/tests control the cutoff.
	now func() time.Time
}

type tableSession struct {
	engine       *Service
	tableID      string
	lastActivity time.Time
}

// NewTableSessions builds an empty store. idleTTL is how long a session may
// go untouched (no SessionForTable/Lookup hit) before Sweep evicts it and
// Lookup stops resolving it.
func NewTableSessions(newEngine func() *Service, idleTTL time.Duration) *TableSessions {
	return &TableSessions{
		newEngine: newEngine,
		idleTTL:   idleTTL,
		byToken:   map[string]*tableSession{},
		byTable:   map[string]string{},
		now:       time.Now,
	}
}

// newSessionToken mints 16 bytes of crypto/rand as lowercase hex — the same
// convention as order-tracking tokens (data.newTrackingToken) and the sync
// enrolment tokens: never uuid, never math/rand. The token is the ONLY thing
// tying an anonymous browser to a table's basket, so it must be unguessable.
func newSessionToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("table session token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// SessionForTable returns the live session for tableID, creating one if
// none exists (or the existing one has idle-expired). existed reports
// whether the caller JOINED an already-live session (a later guest/device
// at the same table) rather than starting it — the /self-order handler uses
// it to decide whether to show the "joined" banner. Touches lastActivity on
// a join.
func (s *TableSessions) SessionForTable(tableID string) (token string, engine *Service, existed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if tok, ok := s.byTable[tableID]; ok {
		if sess := s.byToken[tok]; sess != nil && !s.expiredLocked(sess, now) {
			sess.lastActivity = now
			return tok, sess.engine, true, nil
		}
		// Stale index entry (idle-expired but not yet swept): drop it and
		// mint a fresh session below, rather than resurrect an abandoned
		// basket for a new sitting.
		s.evictLocked(tok)
	}
	token, err = newSessionToken()
	if err != nil {
		return "", nil, false, err
	}
	engine = s.newEngine()
	s.byToken[token] = &tableSession{engine: engine, tableID: tableID, lastActivity: now}
	s.byTable[tableID] = token
	return token, engine, false, nil
}

// Lookup resolves a session cookie's token to its engine and table. ok=false
// means unknown, or idle-expired (whether or not Sweep has run yet) — the
// caller treats both as "no session", the same graceful degradation the
// bare kiosk's idle-reset already has. Touches lastActivity on a hit.
func (s *TableSessions) Lookup(token string) (engine *Service, tableID string, ok bool) {
	if s == nil || token == "" {
		return nil, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byToken[token]
	if sess == nil {
		return nil, "", false
	}
	now := s.now()
	if s.expiredLocked(sess, now) {
		s.evictLocked(token)
		return nil, "", false
	}
	sess.lastActivity = now
	return sess.engine, sess.tableID, true
}

// SetConfigAll pushes cfg to every live session's engine — the per-table
// twin of the `d.KioskEngine.SetConfig(newCfg)` broadcast the settings and
// setup handlers already do, so a tax-rate change reaches an in-progress
// table order too instead of it silently checking out at stale rates.
func (s *TableSessions) SetConfigAll(cfg Config) {
	for _, e := range s.All() {
		e.SetConfig(cfg)
	}
}

// AnyHasItems reports whether any live session's basket holds a line — the
// auto-update scheduler's mid-order guard (update_api.go) consults it beside
// the cashier and bare-kiosk baskets.
func (s *TableSessions) AnyHasItems() bool {
	for _, e := range s.All() {
		if e.Basket().ItemCount() > 0 {
			return true
		}
	}
	return false
}

// All returns a snapshot of every live (non-idle-expired) session's engine —
// for scans like data_api.go's cleanupInLiveBasket. nil on a nil store.
func (s *TableSessions) All() []*Service {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]*Service, 0, len(s.byToken))
	for _, sess := range s.byToken {
		if !s.expiredLocked(sess, now) {
			out = append(out, sess.engine)
		}
	}
	return out
}

// Sweep evicts every session whose lastActivity is older than now-idleTTL
// from both maps and returns how many it removed. The background loop in
// pages.Init calls it periodically; Lookup/SessionForTable also refuse an
// expired session on their own, so Sweep is purely memory hygiene, never
// the correctness gate.
func (s *TableSessions) Sweep(now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for tok, sess := range s.byToken {
		if s.expiredLocked(sess, now) {
			s.evictLocked(tok)
			n++
		}
	}
	return n
}

func (s *TableSessions) expiredLocked(sess *tableSession, now time.Time) bool {
	return now.Sub(sess.lastActivity) > s.idleTTL
}

// evictLocked removes one session from both maps. The byTable entry is only
// dropped when it still points at THIS token — a fresh session for the same
// table may already have replaced it.
func (s *TableSessions) evictLocked(token string) {
	sess := s.byToken[token]
	if sess == nil {
		return
	}
	delete(s.byToken, token)
	if s.byTable[sess.tableID] == token {
		delete(s.byTable, sess.tableID)
	}
}
