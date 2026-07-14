package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

const (
	// CookieName carries the opaque session token. No Secure flag: tills
	// serve plain HTTP on localhost/LAN.
	CookieName = "ut_session"

	SessionTTL = 12 * time.Hour

	maxFailedAttempts = 5
	lockoutDuration   = 30 * time.Second
)

var (
	ErrInvalidPIN = errors.New("invalid pin")
	ErrLockedOut  = errors.New("too many failed attempts")
	ErrPINTaken   = errors.New("pin already in use")
)

// User is the authenticated operator attached to a request.
type User struct {
	ID          string
	Username    string
	DisplayName string
	Role        string
}

// IsManager reports whether the user can approve manager-gated actions.
func (u User) IsManager() bool { return u.Role == "manager" || u.Role == "admin" }

// Service owns login, session resolution and the failed-attempt lockout.
type Service struct {
	repo *data.AuthRepo

	// idleLockMinutes > 0 revokes sessions idle for longer than the window
	// (docs: pos-auth.md idle auto-lock). 0 = disabled.
	idleLockMinutes atomic.Int64
	// onIdleLock is the audit seam (set by pages.Init; auth stays SQL-free).
	onIdleLock atomic.Value // func(ctx context.Context, userID string)

	mu       sync.Mutex
	failures int
	lockedTo time.Time
}

func NewService(db *sql.DB) *Service {
	return &Service{repo: data.NewAuthRepo(db)}
}

func (s *Service) Repo() *data.AuthRepo { return s.repo }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// locked reports whether PIN checks are currently locked out.
func (s *Service) locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.lockedTo)
}

func (s *Service) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.failures >= maxFailedAttempts {
		s.lockedTo = time.Now().Add(lockoutDuration)
		s.failures = 0
	}
}

func (s *Service) recordSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
	s.lockedTo = time.Time{}
}

// FindUserByPIN identifies the operator a PIN belongs to (PIN-only login).
// It does NOT touch the lockout counters — Login does.
func (s *Service) FindUserByPIN(ctx context.Context, pin string) (data.UserRow, bool, error) {
	if err := ValidatePINFormat(pin); err != nil {
		return data.UserRow{}, false, nil
	}
	users, err := s.repo.ListActiveUsersWithPIN(ctx)
	if err != nil {
		return data.UserRow{}, false, err
	}
	for _, u := range users {
		if VerifyPIN(pin, u.PinHash) {
			return u, true, nil
		}
	}
	return data.UserRow{}, false, nil
}

// checkPIN verifies a PIN under the device-wide lockout: every failed check
// (keypad login or manager approval) counts against the same limiter, so no
// endpoint is a brute-force oracle.
func (s *Service) checkPIN(ctx context.Context, pin string) (data.UserRow, error) {
	if s.locked() {
		return data.UserRow{}, ErrLockedOut
	}
	u, ok, err := s.FindUserByPIN(ctx, pin)
	if err != nil {
		return data.UserRow{}, err
	}
	if !ok {
		s.recordFailure()
		return data.UserRow{}, ErrInvalidPIN
	}
	s.recordSuccess()
	return u, nil
}

// Login verifies the PIN and creates a session, enforcing the lockout.
// The returned token goes into the cookie; only its hash is stored.
func (s *Service) Login(ctx context.Context, pin string) (User, string, error) {
	u, err := s.checkPIN(ctx, pin)
	if err != nil {
		return User{}, "", err
	}
	token, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	return User{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role}, token, nil
}

// AuthorizeManager verifies a manager/admin PIN for a gated action (no
// session is created). Shares the login lockout; a wrong PIN of any role
// counts as a failure.
func (s *Service) AuthorizeManager(ctx context.Context, pin string) (User, error) {
	u, err := s.checkPIN(ctx, pin)
	if err != nil {
		return User{}, err
	}
	if u.Role != "manager" && u.Role != "admin" {
		s.recordFailure()
		return User{}, ErrInvalidPIN
	}
	return User{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role}, nil
}

// CreateSession issues a fresh session token for a user (login, first-boot setup).
func (s *Service) CreateSession(ctx context.Context, userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if _, err := s.repo.InsertSession(ctx, hashToken(token), userID, time.Now().Add(SessionTTL)); err != nil {
		return "", err
	}
	// Best-effort housekeeping: drop revoked/week-old-expired sessions.
	_ = s.repo.PurgeDeadSessions(ctx, time.Now().Add(-7*24*time.Hour))
	return token, nil
}

// SetIdleLockMinutes applies the auth.idle_lock_minutes setting (0 disables).
func (s *Service) SetIdleLockMinutes(minutes int) {
	if minutes < 0 {
		minutes = 0
	}
	s.idleLockMinutes.Store(int64(minutes))
}

// IdleLockMinutes returns the active idle window (0 = disabled).
func (s *Service) IdleLockMinutes() int { return int(s.idleLockMinutes.Load()) }

// SetIdleLockAudit installs the callback fired when a session is idle-locked.
func (s *Service) SetIdleLockAudit(fn func(ctx context.Context, userID string)) {
	if fn != nil {
		s.onIdleLock.Store(fn)
	}
}

// touchInterval keeps last_seen_at fresh enough for the idle window without
// a SQLite write per request: well under the window, at most once a minute.
func touchInterval(window time.Duration) time.Duration {
	interval := time.Minute
	if window > 0 && window/4 < interval {
		interval = window / 4
	}
	return interval
}

// Resolve returns the operator for a session token, if the session is live.
// A session idle for longer than the configured window is revoked here —
// the server is authoritative; the client-side timer is cosmetic.
func (s *Service) Resolve(ctx context.Context, token string) (User, bool) {
	if token == "" {
		return User{}, false
	}
	hash := hashToken(token)
	row, ok, err := s.repo.LookupSession(ctx, hash)
	if err != nil || !ok {
		return User{}, false
	}
	idle := time.Since(row.LastSeenAt)
	if window := time.Duration(s.idleLockMinutes.Load()) * time.Minute; window > 0 && idle > window {
		_ = s.repo.RevokeSession(ctx, hash)
		if fn, k := s.onIdleLock.Load().(func(context.Context, string)); k {
			fn(ctx, row.UserID)
		}
		return User{}, false
	}
	if idle >= touchInterval(time.Duration(s.idleLockMinutes.Load())*time.Minute) {
		_ = s.repo.TouchSession(ctx, hash)
	}
	return User{ID: row.UserID, Username: row.Username, DisplayName: row.DisplayName, Role: row.Role}, true
}

// Logout revokes the session behind the token (lock / sign out).
func (s *Service) Logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_ = s.repo.RevokeSession(ctx, hashToken(token))
}

// ChangeOwnPIN lets an operator change their own PIN: current PIN proves
// identity (wrong attempts count against the shared device lockout), the
// new PIN must be valid and unique (login is PIN-only), and all the user's
// sessions are revoked — a changed credential invalidates sessions, so the
// operator signs back in with the new PIN.
func (s *Service) ChangeOwnPIN(ctx context.Context, userID, currentPIN, newPIN string) error {
	if s.locked() {
		return ErrLockedOut
	}
	u, found, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if !found || !VerifyPIN(currentPIN, u.PinHash) {
		s.recordFailure()
		return ErrInvalidPIN
	}
	s.recordSuccess()
	if err := ValidatePINFormat(newPIN); err != nil {
		return err
	}
	if owner, taken, err := s.FindUserByPIN(ctx, newPIN); err != nil {
		return err
	} else if taken && owner.ID != userID {
		return ErrPINTaken
	}
	hash, err := HashPIN(newPIN)
	if err != nil {
		return err
	}
	if err := s.repo.SetUserPIN(ctx, userID, hash); err != nil {
		return err
	}
	return s.repo.RevokeUserSessions(ctx, userID)
}

// NeedsFirstBoot reports whether no operator can log in yet, which switches
// /login into the one-time "set admin PIN" flow.
func (s *Service) NeedsFirstBoot(ctx context.Context) (bool, error) {
	users, err := s.repo.ListActiveUsersWithPIN(ctx)
	if err != nil {
		return false, err
	}
	return len(users) == 0, nil
}
