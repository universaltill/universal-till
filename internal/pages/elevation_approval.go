package pages

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ut-docs#7085: a manager PIN entered on a sell-screen elevation prompt must
// approve only the exact request the prompt summarised, once. The prompt
// carries a single-use token (hidden field "approval") minted for a canonical
// binding of the endpoint, session user, actions, from/to values and target.
// The retry re-derives the binding from what it posts now; a missing, used,
// expired, other-user or edited approval is refused before any PIN lookup.
//
// In-memory and per process by design (offline-first, no DB, no network): a
// till restart invalidates every outstanding approval and the retry simply
// re-prompts (fail closed).

const (
	approvalTTL = 5 * time.Minute // a manager walks over; well under a shift
	approvalCap = 256             // global backstop, bounded even if many sessions spam prompts

	// approvalPerActorCap: one session cannot evict another's live prompt
	// token (ut-docs#7152). A cashier has one live prompt at a time (a wrong
	// PIN reuses its token); a few tabs/devices on one login fit in 8. With
	// 256 global slots it takes 32 actors at their cap before the global
	// eviction can touch anyone else, far beyond a shop's concurrent staff.
	approvalPerActorCap = 8
)

type approvalEntry struct {
	actor   string // session user id that was prompted ("" with auth off)
	binding string
	expires time.Time
}

type approvalStore struct {
	mu  sync.Mutex
	m   map[string]approvalEntry
	now func() time.Time // tests may swap it
}

var elevationApprovals = &approvalStore{m: map[string]approvalEntry{}, now: time.Now}

var errApprovalStale = errors.New("elevation: approval missing, used, expired or for other values")

// mint stores binding under a fresh random token and returns it. An actor
// already holding approvalPerActorCap live tokens loses its own earliest-
// expiring one; only then does the global cap evict. On a crypto/rand failure
// it returns "", which never validates (fail closed).
func (s *approvalStore) mint(actorID, binding string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	tok := base64.RawURLEncoding.EncodeToString(raw[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.m {
		if !now.Before(e.expires) {
			delete(s.m, k)
		}
	}
	own, ownOldest, ownAt := 0, "", time.Time{}
	oldest, oldestAt := "", time.Time{}
	for k, e := range s.m {
		if oldest == "" || e.expires.Before(oldestAt) {
			oldest, oldestAt = k, e.expires
		}
		if e.actor == actorID {
			own++
			if ownOldest == "" || e.expires.Before(ownAt) {
				ownOldest, ownAt = k, e.expires
			}
		}
	}
	if own >= approvalPerActorCap {
		delete(s.m, ownOldest)
	} else if len(s.m) >= approvalCap {
		delete(s.m, oldest)
	}
	s.m[tok] = approvalEntry{actor: actorID, binding: binding, expires: now.Add(approvalTTL)}
	return tok
}

// check reports whether tok is live and bound to binding. Caller holds mu.
func (s *approvalStore) check(tok, binding string) bool {
	if tok == "" {
		return false
	}
	e, ok := s.m[tok]
	if !ok || !s.now().Before(e.expires) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(e.binding), []byte(binding)) == 1
}

// valid is check without consuming the token.
func (s *approvalStore) valid(tok, binding string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.check(tok, binding)
}

// consume checks and deletes atomically: of two concurrent callers exactly
// one wins.
func (s *approvalStore) consume(tok, binding string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.check(tok, binding) {
		return false
	}
	delete(s.m, tok)
	return true
}

// approvalBinding is the canonical, length-prefixed join of everything the
// prompt summarised and the retry will apply. fields are the prompt's hidden
// fields (canonical computed values, never raw form text); extra carries
// server-side facts the form does not (unit price, matched-line snapshot).
func approvalBinding(endpoint, actorID string, actions []string, fields []elevationHiddenField, extra ...string) string {
	acts := append([]string(nil), actions...)
	sort.Strings(acts)
	var b strings.Builder
	put := func(p string) {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
	}
	put("v1")
	put(endpoint)
	put(actorID)
	put(strings.Join(acts, ","))
	for _, f := range fields {
		put(f.Name)
		put(f.Value)
	}
	for _, e := range extra {
		put(e)
	}
	return b.String()
}
