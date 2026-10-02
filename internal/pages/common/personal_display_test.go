package common

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3149 (personal display, part 1/3): ResolvedTheme and
// ResolvedBrowsingMode prefer an operator's personal user_display_settings
// row, but only when their role is granted personal_display — otherwise
// they fall through to the till/shop value in CurrentState(), exactly what
// every page renders today.

const (
	pdTillTheme = "till-theme"
	pdTillMode  = BrowsingModeCategoryTabs
)

type pdFixture struct {
	d   *Deps
	sql *sql.DB
}

// newPDFixture builds a Deps over a real migrated DB with two operators —
// a manager and a cashier — and the till-wide theme/browsing mode set.
// Migration 061 registers personal_display in permission_actions but grants
// it to no role — the shipped state until part 3 of #3149 adds grants.
func newPDFixture(t *testing.T) pdFixture {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	h := openMigratedDB(t, "personal_display.db").DB
	for _, u := range []struct{ id, role string }{{"u-mgr", "manager"}, {"u-cash", "cashier"}} {
		if _, err := h.Exec(`INSERT INTO users (id, username, display_name, role) VALUES (?, ?, ?, ?)`, u.id, u.id, u.id, u.role); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
	}
	d := &Deps{Db: h, AuthSvc: auth.NewService(h)}
	d.State.Theme = pdTillTheme
	d.State.BrowsingMode = pdTillMode
	return pdFixture{d: d, sql: h}
}

// registerAction asserts the action is registered (migration 061 does it),
// so a test's "registered" precondition is checked rather than assumed.
func (f pdFixture) registerAction(t *testing.T) {
	t.Helper()
	if n := f.countAction(t); n != 1 {
		t.Fatalf("precondition: personal_display registered count = %d, want 1 (migration 061)", n)
	}
}

func (f pdFixture) countAction(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.sql.QueryRow(`SELECT COUNT(*) FROM permission_actions WHERE action = 'personal_display'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f pdFixture) grant(t *testing.T, role string, granted bool) {
	t.Helper()
	g := 0
	if granted {
		g = 1
	}
	if _, err := f.sql.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, 'personal_display', ?)`, role, g); err != nil {
		t.Fatalf("grant personal_display to %s: %v", role, err)
	}
}

func (f pdFixture) setPersonal(t *testing.T, userID string) {
	t.Helper()
	repo := data.NewUserDisplayRepo(f.sql)
	if err := repo.Set(context.Background(), userID, KeyTheme, "personal-theme"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(context.Background(), userID, KeyBrowsingMode, BrowsingModeStripOverflow); err != nil {
		t.Fatal(err)
	}
}

func (f pdFixture) resolve(t *testing.T, userID, role string) (theme, mode string) {
	t.Helper()
	r := httptest.NewRequest("GET", "/sale", nil)
	if userID != "" {
		r = auth.WithUser(r, auth.User{ID: userID, Role: role})
	}
	return f.d.ResolvedTheme(r), f.d.ResolvedBrowsingMode(r)
}

func (f pdFixture) assertTill(t *testing.T, userID, role string) {
	t.Helper()
	theme, mode := f.resolve(t, userID, role)
	if theme != pdTillTheme || mode != pdTillMode {
		t.Fatalf("resolved (%q, %q), want till values (%q, %q)", theme, mode, pdTillTheme, pdTillMode)
	}
}

func (f pdFixture) assertPersonal(t *testing.T, userID, role string) {
	t.Helper()
	theme, mode := f.resolve(t, userID, role)
	if theme != "personal-theme" || mode != BrowsingModeStripOverflow {
		t.Fatalf("resolved (%q, %q), want personal values (%q, %q)", theme, mode, "personal-theme", BrowsingModeStripOverflow)
	}
}

func TestResolved_NoOperatorInContextFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.setPersonal(t, "u-mgr")
	f.assertTill(t, "", "")
}

func TestResolved_NilDbFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.setPersonal(t, "u-mgr")
	f.d.Db = nil
	f.assertTill(t, "u-mgr", "manager")
}

func TestResolved_NilAuthSvcFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.setPersonal(t, "u-mgr")
	f.d.AuthSvc = nil
	f.assertTill(t, "u-mgr", "manager")
}

// A till whose permission_actions lacks personal_display altogether (e.g. a
// replica that pulled an admin bundle from a main till still on a pre-061
// build): a row that somehow exists must still be ignored — no panic, no
// error, just the till value.
func TestResolved_ActionNotRegisteredFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.setPersonal(t, "u-mgr")
	if _, err := f.sql.Exec(`DELETE FROM permission_actions WHERE action = 'personal_display'`); err != nil {
		t.Fatal(err)
	}
	if n := f.countAction(t); n != 0 {
		t.Fatalf("precondition: personal_display registered count = %d, want 0", n)
	}
	f.assertTill(t, "u-mgr", "manager")
}

// The state migration 061 ships: personal_display registered, granted to
// no role. Every built-in role falls through even with a personal row —
// this is what keeps part 1 a zero-behaviour-change card.
func TestResolved_AsShippedNoRoleGrantedFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	var grants int
	if err := f.sql.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE action = 'personal_display'`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("precondition: personal_display role_permissions rows = %d (err %v), want 0", grants, err)
	}
	f.setPersonal(t, "u-mgr")
	for _, role := range []string{"cashier", "manager", "admin", "super_admin"} {
		f.assertTill(t, "u-mgr", role)
	}
}

func TestResolved_GrantedButNoRowFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.assertTill(t, "u-mgr", "manager")
}

func TestResolved_GrantedWithRowReturnsPersonal(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.setPersonal(t, "u-mgr")
	f.assertPersonal(t, "u-mgr", "manager")
	// Another operator on the same till still sees the till values.
	f.grant(t, "cashier", true)
	f.assertTill(t, "u-cash", "cashier")
}

func TestResolved_RegisteredButNotGrantedFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.grant(t, "cashier", false)
	f.setPersonal(t, "u-cash")
	f.assertTill(t, "u-cash", "cashier")
}

// A stale or hand-edited browsing-mode row outside the closed enum is
// clamped on read, the same defence LoadState applies to the till row.
func TestResolvedBrowsingMode_InvalidPersonalValueClamped(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	if err := data.NewUserDisplayRepo(f.sql).Set(context.Background(), "u-mgr", KeyBrowsingMode, "bogus"); err != nil {
		t.Fatal(err)
	}
	_, mode := f.resolve(t, "u-mgr", "manager")
	if mode != DefaultBrowsingMode {
		t.Fatalf("mode = %q, want clamped %q", mode, DefaultBrowsingMode)
	}
}

// An empty/whitespace theme row would render no theme at all; treat it as
// "no personal value".
func TestResolvedTheme_BlankPersonalValueFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	if err := data.NewUserDisplayRepo(f.sql).Set(context.Background(), "u-mgr", KeyTheme, "  "); err != nil {
		t.Fatal(err)
	}
	theme, _ := f.resolve(t, "u-mgr", "manager")
	if theme != pdTillTheme {
		t.Fatalf("theme = %q, want till theme %q", theme, pdTillTheme)
	}
}

// A display preference must never break a page render (review, ut-docs#3149):
// when the permission lookup or the row read itself errors, the resolver
// returns the till value — no panic, no error, and never the personal value.
// Each failure is injected on its own so both branches are exercised: first
// the row read (permission still grants), then the permission read.
func TestResolved_DbErrorFallsThrough(t *testing.T) {
	f := newPDFixture(t)
	f.registerAction(t)
	f.grant(t, "manager", true)
	f.setPersonal(t, "u-mgr")
	// Precondition: with everything in place, the personal value wins.
	f.assertPersonal(t, "u-mgr", "manager")

	// 1. The row read fails (table missing) while Can still grants.
	if _, err := f.sql.Exec(`ALTER TABLE user_display_settings RENAME TO user_display_settings_gone`); err != nil {
		t.Fatal(err)
	}
	f.assertTill(t, "u-mgr", "manager")
	if _, err := f.sql.Exec(`ALTER TABLE user_display_settings_gone RENAME TO user_display_settings`); err != nil {
		t.Fatal(err)
	}
	f.assertPersonal(t, "u-mgr", "manager") // restored: the happy path is back

	// 2. The permission read fails (role_permissions missing) — fail closed.
	if _, err := f.sql.Exec(`ALTER TABLE role_permissions RENAME TO role_permissions_gone`); err != nil {
		t.Fatal(err)
	}
	f.assertTill(t, "u-mgr", "manager")
}

// UT_AUTH=off: same escape hatch as pages.canPerform — the permission check
// passes without consulting role_permissions (here personal_display isn't
// even registered). There is still no personal row to read without an
// operator in context, so a request with no operator falls through.
func TestResolved_AuthDisabledMatchesCanPerform(t *testing.T) {
	f := newPDFixture(t)
	f.setPersonal(t, "u-cash")
	t.Setenv("UT_AUTH", "off")
	f.assertPersonal(t, "u-cash", "cashier")
	f.assertTill(t, "", "")
}
