package agentaction

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Ceilings (ADR-0153 Decisions 3 and 6). Over any of them Put returns
// ErrQuota (host code -5).
const (
	ProposalTTL         = 5 * time.Minute
	MaxOpenPerRequest   = 3
	MaxPerSessionPerHr  = 20
	MaxOpenProposals    = 256
	maxTrackedSessions  = 1024
	sessionWindow       = time.Hour
	proposalIDByteCount = 16
)

// ErrQuota: a proposal ceiling was hit.
var ErrQuota = errors.New("agentaction: proposal ceiling reached")

// Operator is who asked: core stamps it from the signed-in session of the
// ui ask, never from the plugin's payload.
type Operator struct {
	UserID  string `json:"user_id"`
	Role    string `json:"role"`
	Session string `json:"-"`
	// Request identifies the one ui ask (a chat turn).
	Request string `json:"-"`
}

// Proposal is one pending change, bound to plugin + user + session.
type Proposal struct {
	ID       string
	PluginID string
	Operator Operator
	Action   *Action
	Rows     []Row
	Created  time.Time
	Expires  time.Time
	args     any
	state    any
	timer    stopper
}

// stopper is what Store needs of an expiry timer.
type stopper interface{ Stop() bool }

// Args and State are what Apply needs.
func (p *Proposal) Args() any  { return p.args }
func (p *Proposal) State() any { return p.state }

// Store is the bounded, in-memory, single-use proposal store. A proposal
// never survives a restart: it dies with the process, like its session's
// open chat.
type Store struct {
	mu       sync.Mutex
	open     map[string]*Proposal
	sessions map[string][]time.Time
	now      func() time.Time
	// afterFunc arms an expiry timer (time.AfterFunc; a test fakes it).
	afterFunc func(d time.Duration, f func()) stopper
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{open: map[string]*Proposal{}, sessions: map[string][]time.Time{}, now: time.Now,
		afterFunc: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }}
}

// Proposals is the till's one store.
var Proposals = NewStore()

// NewProposal builds a proposal (no id yet) from a prepared action.
func NewProposal(pluginID string, op Operator, a *Action, args any, rows []Row, state any) *Proposal {
	return &Proposal{PluginID: pluginID, Operator: op, Action: a, Rows: rows, args: args, state: state}
}

// Put stores p under a new crypto-random id, enforcing the ceilings.
// Expired proposals are swept (and audited) first.
func (s *Store) Put(ctx context.Context, db *sql.DB, p *Proposal) (string, error) {
	var b [proposalIDByteCount]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	s.mu.Lock()
	now := s.now()
	expired := s.sweepLocked(now)
	id, err := s.putLocked(now, hex.EncodeToString(b[:]), p)
	if err == nil {
		// Audit the expiry on time even on an idle till; the sweep
		// deletes before returning, so a later one can't audit it again.
		p.timer = s.afterFunc(p.Expires.Sub(now), func() {
			s.mu.Lock()
			expired := s.sweepLocked(s.now())
			s.mu.Unlock()
			AuditExpired(context.Background(), db, expired)
		})
	}
	s.mu.Unlock()
	AuditExpired(ctx, db, expired)
	return id, err
}

func (s *Store) putLocked(now time.Time, id string, p *Proposal) (string, error) {
	if len(s.open) >= MaxOpenProposals {
		return "", ErrQuota
	}
	inReq := 0
	for _, o := range s.open {
		if o.Operator.Request == p.Operator.Request && o.Operator.Session == p.Operator.Session {
			inReq++
		}
	}
	if inReq >= MaxOpenPerRequest {
		return "", ErrQuota
	}
	recent := s.sessions[p.Operator.Session]
	if len(recent) >= MaxPerSessionPerHr {
		return "", ErrQuota
	}
	if recent == nil && len(s.sessions) >= maxTrackedSessions {
		return "", ErrQuota
	}
	s.sessions[p.Operator.Session] = append(recent, now)
	p.ID, p.Created, p.Expires = id, now, now.Add(ProposalTTL)
	s.open[id] = p
	return id, nil
}

// sweepLocked drops expired proposals (returned for auditing) and session
// counts older than the window.
func (s *Store) sweepLocked(now time.Time) []*Proposal {
	var out []*Proposal
	for id, p := range s.open {
		if !now.Before(p.Expires) {
			delete(s.open, id)
			if p.timer != nil {
				p.timer.Stop()
			}
			out = append(out, p)
		}
	}
	for k, ts := range s.sessions {
		i := 0
		for i < len(ts) && now.Sub(ts[i]) >= sessionWindow {
			i++
		}
		if i == len(ts) {
			delete(s.sessions, k)
		} else if i > 0 {
			s.sessions[k] = append([]time.Time(nil), ts[i:]...)
		}
	}
	return out
}

// Peek returns the live proposal id for this user and session, or nil.
func (s *Store) Peek(ctx context.Context, db *sql.DB, id, userID, session string) *Proposal {
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	p := s.open[id]
	s.mu.Unlock()
	AuditExpired(ctx, db, expired)
	if p == nil || !p.boundTo(userID, session) {
		return nil
	}
	return p
}

// Take removes and returns the live proposal id for this user and
// session (single use), or nil. Another user's id is left in place.
func (s *Store) Take(ctx context.Context, db *sql.DB, id, userID, session string) *Proposal {
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	p := s.open[id]
	if p != nil && p.boundTo(userID, session) {
		delete(s.open, id)
		if p.timer != nil {
			p.timer.Stop()
		}
	} else {
		p = nil
	}
	s.mu.Unlock()
	AuditExpired(ctx, db, expired)
	return p
}

func (p *Proposal) boundTo(userID, session string) bool {
	return userID != "" && session != "" && p.Operator.UserID == userID && p.Operator.Session == session
}

// Audit writes one agent.action.<event> row: plugin, operator, action,
// rows and result, with the operator as actor.
func Audit(ctx context.Context, db *sql.DB, tx *sql.Tx, event string, p *Proposal, result string) error {
	return data.NewPOSRepo(db).InsertAudit(ctx, tx, p.Operator.UserID, "plugin", p.PluginID, "agent.action."+event, map[string]any{
		"plugin":   p.PluginID,
		"operator": p.Operator,
		"action":   p.Action.Name,
		"proposal": p.ID,
		"rows":     p.Rows,
		"result":   result,
	}, time.Now().UTC().Format(time.RFC3339), "")
}

// AuditExpired audits each swept proposal as agent.action.expired.
func AuditExpired(ctx context.Context, db *sql.DB, ps []*Proposal) {
	if db == nil {
		return
	}
	for _, p := range ps {
		if err := Audit(ctx, db, nil, "expired", p, "expired"); err != nil {
			logging.L().Warnf("agent action: audit expiry of %s: %v", p.ID, err)
		}
	}
}
