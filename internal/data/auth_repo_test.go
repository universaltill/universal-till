package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// The whole operator-auth surface (PIN login, sessions, the users admin
// page) sits on this repo, and none of it had coverage until this batch.
// These tests run against a real migrated schema — the users table's
// UNIQUE(username), the sessions FK, and migration 010's last_seen_at
// backfill are all part of the behavior under test.

func newAuthTestRepo(t *testing.T) (*AuthRepo, *db.DB) {
	t.Helper()
	d := openMigratedDB(t, "auth.db")
	return NewAuthRepo(d.DB), d
}

func TestAuthRepo_CreateGetListUsers(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	id, err := repo.CreateUser(ctx, "zara", "Zara", "manager")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if id == "" {
		t.Fatal("CreateUser returned empty id")
	}

	// A brand-new user is active, has no PIN yet (NULL in the DB — the
	// COALESCE in userCols must surface it as ""), and round-trips fields.
	u, ok, err := repo.GetUser(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if u.Username != "zara" || u.DisplayName != "Zara" || u.Role != "manager" {
		t.Fatalf("unexpected user: %+v", u)
	}
	if !u.IsActive {
		t.Fatal("new user must default to active")
	}
	if u.PinHash != "" {
		t.Fatalf("new user must have empty PinHash, got %q", u.PinHash)
	}

	// Unknown id: not found, not an error.
	if _, ok, err := repo.GetUser(ctx, "nope"); err != nil || ok {
		t.Fatalf("GetUser(unknown): ok=%v err=%v", ok, err)
	}

	// usernames are UNIQUE in the schema — a duplicate must fail loudly.
	if _, err := repo.CreateUser(ctx, "zara", "Other Zara", "cashier"); err == nil {
		t.Fatal("duplicate username must error")
	}

	// ListUsers shows inactive users too (the admin page needs them),
	// ordered by username.
	id2, err := repo.CreateUser(ctx, "amir", "Amir", "cashier")
	if err != nil {
		t.Fatalf("CreateUser amir: %v", err)
	}
	if err := repo.SetUserActive(ctx, id2, false); err != nil {
		t.Fatalf("SetUserActive: %v", err)
	}
	users, err := repo.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	// The migrated schema seeds its own users (admin, kiosk, …) — assert
	// on ours relative to each other rather than an absolute count.
	posAmir, posZara := -1, -1
	for i, u := range users {
		switch u.Username {
		case "amir":
			posAmir = i
			if u.IsActive {
				t.Fatal("amir was deactivated; ListUsers must still return him, inactive")
			}
		case "zara":
			posZara = i
		}
	}
	if posAmir == -1 || posZara == -1 {
		t.Fatalf("ListUsers missing created users: %+v", users)
	}
	if posAmir > posZara {
		t.Fatal("ListUsers must order by username (amir before zara)")
	}
	_ = d
}

func TestAuthRepo_ListActiveUsersWithPIN(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	// Login candidates are exactly: active AND a non-empty pin_hash.
	mk := func(username string, active bool, pin string) string {
		t.Helper()
		id, err := repo.CreateUser(ctx, username, username, "cashier")
		if err != nil {
			t.Fatalf("CreateUser %s: %v", username, err)
		}
		if pin != "" {
			if err := repo.SetUserPIN(ctx, id, pin); err != nil {
				t.Fatalf("SetUserPIN %s: %v", username, err)
			}
		}
		if !active {
			if err := repo.SetUserActive(ctx, id, false); err != nil {
				t.Fatalf("SetUserActive %s: %v", username, err)
			}
		}
		return id
	}
	inID := mk("can-login", true, "hash-1")
	mk("no-pin", true, "")
	mk("inactive", false, "hash-2")
	// Empty-string pin_hash (as opposed to NULL) must also be excluded.
	emptyID := mk("empty-pin", true, "x")
	mustExec(t, d, `UPDATE users SET pin_hash = '' WHERE id = ?`, emptyID)

	got, err := repo.ListActiveUsersWithPIN(ctx)
	if err != nil {
		t.Fatalf("ListActiveUsersWithPIN: %v", err)
	}
	found := map[string]bool{}
	for _, u := range got {
		found[u.Username] = true
	}
	if !found["can-login"] {
		t.Fatal("active user with PIN missing from login candidates")
	}
	for _, bad := range []string{"no-pin", "inactive", "empty-pin"} {
		if found[bad] {
			t.Fatalf("%s must not be a login candidate", bad)
		}
	}
	// And the returned row carries the hash PIN login verifies against.
	for _, u := range got {
		if u.ID == inID && u.PinHash != "hash-1" {
			t.Fatalf("login candidate lost its pin hash: %+v", u)
		}
	}
}

func TestAuthRepo_SetUserPIN(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	id, err := repo.CreateUser(ctx, "pat", "Pat", "cashier")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := repo.SetUserPIN(ctx, id, "new-hash"); err != nil {
		t.Fatalf("SetUserPIN: %v", err)
	}
	var stored string
	if err := d.DB.QueryRow(`SELECT pin_hash FROM users WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "new-hash" {
		t.Fatalf("pin_hash not persisted: %q", stored)
	}

	// Unknown user: loud error, not a silent no-op — the admin UI relies
	// on this to report a failed reset.
	if err := repo.SetUserPIN(ctx, "ghost", "h"); err == nil {
		t.Fatal("SetUserPIN(unknown) must error")
	}
}

func TestAuthRepo_SetUserActive(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	id, err := repo.CreateUser(ctx, "sam", "Sam", "cashier")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := repo.SetUserActive(ctx, id, false); err != nil {
		t.Fatalf("SetUserActive(false): %v", err)
	}
	var active int
	if err := d.DB.QueryRow(`SELECT is_active FROM users WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("user not deactivated in DB")
	}
	if err := repo.SetUserActive(ctx, id, true); err != nil {
		t.Fatalf("SetUserActive(true): %v", err)
	}
	if err := d.DB.QueryRow(`SELECT is_active FROM users WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatal("user not reactivated in DB")
	}
	if err := repo.SetUserActive(ctx, "ghost", true); err == nil {
		t.Fatal("SetUserActive(unknown) must error")
	}
}

func TestAuthRepo_CountOtherActiveAdminsWithPIN(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	// The migrated schema may seed its own admin — measure the baseline
	// first so this test asserts deltas, not absolute counts.
	baseline, err := repo.CountOtherActiveAdminsWithPIN(ctx, "none")
	if err != nil {
		t.Fatalf("baseline count: %v", err)
	}

	mkAdmin := func(username string, active bool, pin string) string {
		t.Helper()
		id, err := repo.CreateUser(ctx, username, username, "admin")
		if err != nil {
			t.Fatalf("CreateUser %s: %v", username, err)
		}
		if pin != "" {
			if err := repo.SetUserPIN(ctx, id, pin); err != nil {
				t.Fatal(err)
			}
		}
		if !active {
			if err := repo.SetUserActive(ctx, id, false); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	a1 := mkAdmin("admin-a", true, "h1")
	mkAdmin("admin-b", true, "h2")
	mkAdmin("admin-inactive", false, "h3") // must not count
	mkAdmin("admin-no-pin", true, "")      // must not count
	// A non-admin with a PIN must not count either.
	cid, err := repo.CreateUser(ctx, "cashier-c", "C", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUserPIN(ctx, cid, "h4"); err != nil {
		t.Fatal(err)
	}

	// From admin-a's perspective: only admin-b (plus any seeded baseline).
	n, err := repo.CountOtherActiveAdminsWithPIN(ctx, a1)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != baseline+1 {
		t.Fatalf("expected %d other admins, got %d", baseline+1, n)
	}
	// From an outsider's perspective both count.
	n, err = repo.CountOtherActiveAdminsWithPIN(ctx, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if n != baseline+2 {
		t.Fatalf("expected %d admins, got %d", baseline+2, n)
	}
	_ = d
}

func TestAuthRepo_SessionLifecycle(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	uid, err := repo.CreateUser(ctx, "ops", "Ops", "manager")
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(1 * time.Hour)
	sid, err := repo.InsertSession(ctx, "tok-live", uid, expiry)
	if err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if sid == "" {
		t.Fatal("InsertSession returned empty id")
	}

	s, ok, err := repo.LookupSession(ctx, "tok-live")
	if err != nil || !ok {
		t.Fatalf("LookupSession: ok=%v err=%v", ok, err)
	}
	if s.SessionID != sid || s.UserID != uid || s.Username != "ops" || s.Role != "manager" {
		t.Fatalf("unexpected session row: %+v", s)
	}
	if s.ExpiresAt.Unix() != expiry.UTC().Truncate(time.Second).Unix() {
		t.Fatalf("expiry mismatch: got %v want %v", s.ExpiresAt, expiry)
	}
	// last_seen_at is NULL right after insert; the COALESCE falls back to
	// created_at (sqlite datetime('now') format) — the second accepted
	// layout. It must parse, not stay zero.
	if s.LastSeenAt.IsZero() {
		t.Fatal("LastSeenAt must fall back to created_at, not zero")
	}

	// Wrong token: no session.
	if _, ok, err := repo.LookupSession(ctx, "tok-wrong"); err != nil || ok {
		t.Fatalf("LookupSession(wrong): ok=%v err=%v", ok, err)
	}

	// An expired session must not resolve, even though the row exists.
	if _, err := repo.InsertSession(ctx, "tok-expired", uid, time.Now().Add(-1*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.LookupSession(ctx, "tok-expired"); ok {
		t.Fatal("expired session must not resolve")
	}

	// A revoked session must not resolve.
	if err := repo.RevokeSession(ctx, "tok-live"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, ok, _ := repo.LookupSession(ctx, "tok-live"); ok {
		t.Fatal("revoked session must not resolve")
	}
	var revokedAt *string
	if err := d.DB.QueryRow(`SELECT revoked_at FROM sessions WHERE id = ?`, sid).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil {
		t.Fatal("revoked_at not written")
	}

	// A deactivated user's (still-live) session must not resolve — this is
	// the "deactivate locks them out NOW" guarantee the admin page promises.
	if _, err := repo.InsertSession(ctx, "tok-deact", uid, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUserActive(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.LookupSession(ctx, "tok-deact"); ok {
		t.Fatal("deactivated user's session must not resolve")
	}
}

func TestAuthRepo_TouchSession(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	uid, err := repo.CreateUser(ctx, "ops", "Ops", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertSession(ctx, "tok-a", uid, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := repo.TouchSession(ctx, "tok-a"); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	var lastSeen *string
	if err := d.DB.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash = 'tok-a'`).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen == nil {
		t.Fatal("TouchSession must write last_seen_at")
	}
	if _, err := time.Parse(time.RFC3339, *lastSeen); err != nil {
		t.Fatalf("last_seen_at not RFC3339 (%q): %v — LookupSession's first parse layout depends on this", *lastSeen, err)
	}
	// After a touch, LookupSession must surface the RFC3339 value (first
	// layout), not the created_at fallback.
	s, ok, err := repo.LookupSession(ctx, "tok-a")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if s.LastSeenAt.IsZero() {
		t.Fatal("LastSeenAt zero after touch")
	}

	// Touching a revoked session is a silent no-op (the row keeps its
	// pre-revocation last_seen_at). A plain before/after comparison would be
	// a false-pass here: TouchSession writes at 1-second RFC3339 resolution
	// and this test runs in milliseconds, so two writes would collide into
	// identical strings. Seed a sentinel value instead — any real write is
	// time.Now()-derived and can never equal it.
	if err := repo.RevokeSession(ctx, "tok-a"); err != nil {
		t.Fatal(err)
	}
	const sentinel = "2001-01-01T00:00:00Z"
	mustExec(t, d, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = 'tok-a'`, sentinel)
	if err := repo.TouchSession(ctx, "tok-a"); err != nil {
		t.Fatalf("TouchSession(revoked): %v", err)
	}
	var after *string
	if err := d.DB.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash = 'tok-a'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == nil || *after != sentinel {
		t.Fatalf("TouchSession must not update a revoked session (got %v, want sentinel)", after)
	}
}

func TestAuthRepo_PurgeDeadSessions(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	uid, err := repo.CreateUser(ctx, "ops", "Ops", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// live: expires in the future, never revoked — must survive.
	if _, err := repo.InsertSession(ctx, "tok-live", uid, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// revoked: still unexpired, but revoked — must be purged.
	if _, err := repo.InsertSession(ctx, "tok-revoked", uid, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeSession(ctx, "tok-revoked"); err != nil {
		t.Fatal(err)
	}
	// long-expired: expired before the cutoff — must be purged.
	if _, err := repo.InsertSession(ctx, "tok-old", uid, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// recently-expired: expired, but after the cutoff — must survive (the
	// grace window exists so an in-flight request can still see its row).
	if _, err := repo.InsertSession(ctx, "tok-recent", uid, now.Add(-1*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := repo.PurgeDeadSessions(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("PurgeDeadSessions: %v", err)
	}

	remaining := map[string]bool{}
	rows, err := d.DB.Query(`SELECT token_hash FROM sessions WHERE user_id = ?`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			t.Fatal(err)
		}
		remaining[tok] = true
	}
	if !remaining["tok-live"] || !remaining["tok-recent"] {
		t.Fatalf("purge deleted sessions it must keep: %v", remaining)
	}
	if remaining["tok-revoked"] || remaining["tok-old"] {
		t.Fatalf("purge kept sessions it must delete: %v", remaining)
	}
}

func TestAuthRepo_RevokeUserSessions(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	u1, err := repo.CreateUser(ctx, "one", "One", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := repo.CreateUser(ctx, "two", "Two", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"u1-a", "u1-b"} {
		if _, err := repo.InsertSession(ctx, tok, u1, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.InsertSession(ctx, "u2-a", u2, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := repo.RevokeUserSessions(ctx, u1); err != nil {
		t.Fatalf("RevokeUserSessions: %v", err)
	}

	var revokedU1, revokedU2 int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NOT NULL`, u1).Scan(&revokedU1); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NOT NULL`, u2).Scan(&revokedU2); err != nil {
		t.Fatal(err)
	}
	if revokedU1 != 2 {
		t.Fatalf("expected both of u1's sessions revoked, got %d", revokedU1)
	}
	if revokedU2 != 0 {
		t.Fatal("u2's session must be untouched")
	}
	// And neither of u1's tokens resolves anymore.
	for _, tok := range []string{"u1-a", "u1-b"} {
		if _, ok, _ := repo.LookupSession(ctx, tok); ok {
			t.Fatalf("session %s still resolves after user-wide revoke", tok)
		}
	}
}

// TestAuthRepo_HasPermission covers migration 039's seed grants (#554,
// including the "settings" action — now actually consumed by
// internal/pages/settings_page.go's canPerform() call sites as of ut-docs#710,
// so it belongs in this assertion list same as every other wired-up action),
// 042's reports/audit additions (#709), 043's plugin_management addition
// (#706), and 044's data_management/sync_management additions (#707):
// manager/admin/super_admin get every catalog action, cashier gets
// none, and an action outside the catalog is denied for everyone rather than
// erroring — "no row" means denied, not "unknown." Exercising these here
// (against a REAL migrated DB, unlike internal/pages' hand-rolled fixture) is
// what actually pins each migration's output — ut-docs#709 review finding:
// nothing else in the codebase did. (#707 review: this list is the ONLY
// thing that pins a migration's role/action grants against the real
// migrated DB — internal/pages' seedForPages hand-seeds every role for
// every action in its own catalog list regardless of what a migration
// actually grants, so a typo in a migration's role list ships undetected
// unless it's caught here.)
func TestAuthRepo_HasPermission(t *testing.T) {
	ctx := context.Background()
	repo, _ := newAuthTestRepo(t)

	for _, role := range []string{"manager", "admin", "super_admin"} {
		for _, action := range []string{"refund", "reports", "audit", "plugin_management", "data_management", "sync_management", "settings"} {
			granted, err := repo.HasPermission(ctx, role, action)
			if err != nil {
				t.Fatalf("HasPermission(%s, %s): %v", role, action, err)
			}
			if !granted {
				t.Fatalf("expected %s granted %s by the seed data", role, action)
			}
		}
	}

	for _, action := range []string{"refund", "reports", "audit", "plugin_management", "data_management", "sync_management", "settings"} {
		granted, err := repo.HasPermission(ctx, "cashier", action)
		if err != nil {
			t.Fatalf("HasPermission(cashier, %s): %v", action, err)
		}
		if granted {
			t.Fatalf("cashier must not be granted %s by the seed data", action)
		}
	}

	granted, err := repo.HasPermission(ctx, "manager", "no-such-action")
	if err != nil {
		t.Fatalf("HasPermission(manager, no-such-action): %v", err)
	}
	if granted {
		t.Fatal("an action outside the catalog must be denied, not granted")
	}
}

// TestAuthRepo_HasPermission_KioskUserZeroGrants is the regression test
// #554's acceptance criteria call for: the kiosk user (018_kiosk_user.sql,
// role=cashier) must have zero grants across the whole action catalog,
// since /self-order routes are auth-exempt and reachable by any anonymous
// LAN client (ADR-0020).
func TestAuthRepo_HasPermission_KioskUserZeroGrants(t *testing.T) {
	ctx := context.Background()
	repo, d := newAuthTestRepo(t)

	kiosk, ok, err := repo.GetUser(ctx, "kiosk")
	if err != nil || !ok {
		t.Fatalf("GetUser(kiosk): ok=%v err=%v", ok, err)
	}
	if kiosk.Role != "cashier" {
		t.Fatalf("expected kiosk user role=cashier, got %q", kiosk.Role)
	}

	rows, err := d.DB.QueryContext(ctx, `SELECT action FROM permission_actions`)
	if err != nil {
		t.Fatalf("list catalog actions: %v", err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actions) == 0 {
		t.Fatal("expected a non-empty action catalog")
	}

	for _, action := range actions {
		granted, err := repo.HasPermission(ctx, kiosk.Role, action)
		if err != nil {
			t.Fatalf("HasPermission(kiosk, %s): %v", action, err)
		}
		if granted {
			t.Fatalf("kiosk user must have zero grants, but got %s", action)
		}
	}
}
