package pages

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Additional-till side of ADR-0115 §1 (ut-docs#2755): on a till that
// follows a main till, every user/PIN change from the users page and the
// staffer's own PIN change writes through to the main till's
// /api/sync/users/apply and is mirrored locally only after the main till
// answered 200. Any failure refuses the change with no local write -- a
// local-only edit would be reverted by the next admin-bundle pull anyway.
//
// The main till here is the REAL handler (registerSyncUsers) behind an
// httptest server with its own database, so a test proves the change
// actually landed on the main till, not just that a request was sent.

// userSyncMain is a real main till behind an httptest server.
type userSyncMain struct {
	srv   *httptest.Server
	mux   *http.ServeMux
	dp    *common.Deps
	calls atomic.Int64
}

func newUserSyncMain(t *testing.T) *userSyncMain {
	t.Helper()
	mux, dp := newSyncUsersTestDeps(t) // seeds adm-1 (admin, PIN 7391) + the Till 2 bearer
	m := &userSyncMain{dp: dp, mux: mux}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// newUserSyncReplica is the additional till: auth + users pages over its
// own database, pointed at mainURL. adm-1 carries the same PIN hash as on
// the main till, as the admin bundle would have delivered it.
func newUserSyncReplica(t *testing.T, main *userSyncMain, mainURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, _, dp := newAuthTestMux(t)
	var hash string
	if main != nil {
		u, _, err := data.NewAuthRepo(main.dp.Db).GetUser(t.Context(), "adm-1")
		if err != nil {
			t.Fatal(err)
		}
		hash = u.PinHash
	} else {
		hash, _ = auth.HashPIN("7391")
	}
	insertTestUserWithPIN(t, dp.Db, "adm-1", "adm1", "Admin One", "admin", hash)
	setReplicaSettings(t, dp.Settings, mainURL, syncUsersBearer)
	return mux, dp
}

// seedBoth inserts the same user row on both tills (the state right after
// an admin-bundle pull).
func seedBoth(t *testing.T, main *userSyncMain, replica *common.Deps, id, username, display, role, pinHash string) {
	t.Helper()
	insertTestUserWithPIN(t, main.dp.Db, id, username, display, role, pinHash)
	insertTestUserWithPIN(t, replica.Db, id, username, display, role, pinHash)
}

var admin1 = auth.User{ID: "adm-1", Role: "admin", DisplayName: "Admin One"}

func mustGetUser(t *testing.T, dp *common.Deps, id string) data.UserRow {
	t.Helper()
	u, ok, err := data.NewAuthRepo(dp.Db).GetUser(t.Context(), id)
	if err != nil || !ok {
		t.Fatalf("GetUser(%s): ok=%v err=%v", id, ok, err)
	}
	return u
}

func wantUsersOK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "ok" {
		t.Fatalf("status=%d X-UT-Response=%q body=%q, want a 200 ok", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
}

func wantUsersRefused(t *testing.T, rec *httptest.ResponseRecorder, key string) {
	t.Helper()
	msg := httpx.T("en", key)
	if rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("status=%d X-UT-Response=%q body=%q, want refused with %q (%s)", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String(), msg, key)
	}
}

func TestUserWriteThrough_CreateLandsOnMainAndMirrorsSameID(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)

	rec := postForm(mux, "/api/users", url.Values{"username": {"rana"}, "display_name": {"Rana"}, "role": {"cashier"}}, &admin1)
	wantUsersOK(t, rec)
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	var mainID, localID string
	if err := main.dp.Db.QueryRow(`SELECT id FROM users WHERE username = 'rana'`).Scan(&mainID); err != nil {
		t.Fatalf("user not created on the main till: %v", err)
	}
	if err := dp.Db.QueryRow(`SELECT id FROM users WHERE username = 'rana'`).Scan(&localID); err != nil {
		t.Fatalf("user not mirrored locally: %v", err)
	}
	if mainID != localID {
		t.Fatalf("local id %q != main id %q: the next pull would delete the local row", localID, mainID)
	}
	assertUserAudit(t, dp, "adm-1", localID, "user_create")
	assertUserAudit(t, main.dp, "adm-1", mainID, "user_create")
}

func TestUserWriteThrough_SetPINSendsHashAndMirrorsIt(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)
	seedBoth(t, main, dp, "c-1", "cara", "Cara", "cashier", "")
	if _, err := data.NewAuthRepo(dp.Db).InsertSession(t.Context(), "tok-local", "c-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	wantUsersOK(t, postForm(mux, "/api/users/c-1/pin", url.Values{"pin": {"4455"}}, &admin1))
	onMain, local := mustGetUser(t, main.dp, "c-1"), mustGetUser(t, dp, "c-1")
	if !auth.VerifyPIN("4455", onMain.PinHash) {
		t.Fatal("main till does not hold a hash of the new PIN")
	}
	if local.PinHash != onMain.PinHash {
		t.Fatal("local mirror must hold exactly the hash the main till stored")
	}
	if _, live, _ := data.NewAuthRepo(dp.Db).LookupSession(t.Context(), "tok-local"); live {
		t.Fatal("a PIN change must revoke the target's local sessions")
	}
	assertUserAudit(t, dp, "adm-1", "c-1", "user_pin_set")
}

func TestUserWriteThrough_SetActiveAndRole(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)
	seedBoth(t, main, dp, "c-1", "cara", "Cara", "cashier", "")

	wantUsersOK(t, postForm(mux, "/api/users/c-1/role", url.Values{"role": {"manager"}}, &admin1))
	if mustGetUser(t, main.dp, "c-1").Role != "manager" || mustGetUser(t, dp, "c-1").Role != "manager" {
		t.Fatal("role change must land on the main till and be mirrored")
	}
	assertUserAudit(t, dp, "adm-1", "c-1", "user_role_changed")

	wantUsersOK(t, postForm(mux, "/api/users/c-1/active", url.Values{"active": {"0"}}, &admin1))
	if mustGetUser(t, main.dp, "c-1").IsActive || mustGetUser(t, dp, "c-1").IsActive {
		t.Fatal("deactivation must land on the main till and be mirrored")
	}
	assertUserAudit(t, dp, "adm-1", "c-1", "user_deactivate")
}

func TestUserWriteThrough_PromoteSuperAdmin(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)
	seedBoth(t, main, dp, "sa-1", "sa1", "Super", "super_admin", "")
	seedBoth(t, main, dp, "m-1", "mo", "Mo", "manager", "")

	rec := postForm(mux, "/api/users/m-1/promote-super-admin", nil, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/users" {
		t.Fatalf("promote = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if mustGetUser(t, main.dp, "m-1").Role != "super_admin" || mustGetUser(t, dp, "m-1").Role != "super_admin" {
		t.Fatal("promotion must land on the main till and be mirrored")
	}
	assertUserAudit(t, dp, "sa-1", "m-1", "user_role_changed")
}

func TestUserWriteThrough_OwnPINChange(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)
	old, _ := auth.HashPIN("1212")
	seedBoth(t, main, dp, "c-1", "cara", "Cara", "cashier", old)

	cara := auth.User{ID: "c-1", Role: "cashier"}
	rec := postForm(mux, "/api/pin/change", url.Values{"current_pin": {"1212"}, "new_pin": {"3434"}, "new_pin2": {"3434"}}, &cara)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("pin change = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	onMain, local := mustGetUser(t, main.dp, "c-1"), mustGetUser(t, dp, "c-1")
	if !auth.VerifyPIN("3434", onMain.PinHash) || local.PinHash != onMain.PinHash {
		t.Fatal("own PIN change must land on the main till and be mirrored")
	}
}

// Main till unreachable: every covered call is refused with the new key
// and the local database is left exactly as it was.
func TestUserWriteThrough_MainUnreachableRefusesWithoutLocalWrite(t *testing.T) {
	mux, dp := newUserSyncReplica(t, nil, deadPrimaryURL())
	old, _ := auth.HashPIN("1212")
	insertTestUserWithPIN(t, dp.Db, "c-1", "cara", "Cara", "cashier", old)
	insertTestUserWithPIN(t, dp.Db, "sa-1", "sa1", "Super", "super_admin", old)
	const key = "users.error.main_till_unreachable"

	wantUsersRefused(t, postForm(mux, "/api/users", url.Values{"username": {"rana"}, "display_name": {"Rana"}, "role": {"cashier"}}, &admin1), key)
	wantUsersRefused(t, postForm(mux, "/api/users/c-1/pin", url.Values{"pin": {"4455"}}, &admin1), key)
	wantUsersRefused(t, postForm(mux, "/api/users/c-1/active", url.Values{"active": {"0"}}, &admin1), key)
	wantUsersRefused(t, postForm(mux, "/api/users/c-1/role", url.Values{"role": {"manager"}}, &admin1), key)

	rec := postForm(mux, "/api/users/c-1/promote-super-admin", nil, &auth.User{ID: "sa-1", Role: "super_admin"})
	if rec.Header().Get("Location") != "/users?err="+key {
		t.Fatalf("promote refused Location = %q", rec.Header().Get("Location"))
	}
	cara := auth.User{ID: "c-1", Role: "cashier"}
	rec = postForm(mux, "/api/pin/change", url.Values{"current_pin": {"1212"}, "new_pin": {"3434"}, "new_pin2": {"3434"}}, &cara)
	if rec.Header().Get("Location") != "/pin?err="+key {
		t.Fatalf("own pin change refused Location = %q", rec.Header().Get("Location"))
	}

	if taken, _ := data.NewAuthRepo(dp.Db).UsernameTaken(t.Context(), "rana"); taken {
		t.Fatal("refused create wrote locally")
	}
	c := mustGetUser(t, dp, "c-1")
	if c.PinHash != old || !c.IsActive || c.Role != "cashier" {
		t.Fatalf("refused change wrote locally: %+v", c)
	}
}

// A replica with the main till's URL but no bearer yet is refused too
// rather than silently writing locally.
func TestUserWriteThrough_NoBearerRefuses(t *testing.T) {
	mux, _, dp := newAuthTestMux(t)
	pin, _ := auth.HashPIN("7391")
	insertTestUserWithPIN(t, dp.Db, "adm-1", "adm1", "Admin One", "admin", pin)
	setReplicaSettings(t, dp.Settings, "http://192.0.2.1:1", "")
	wantUsersRefused(t, postForm(mux, "/api/users", url.Values{"username": {"rana"}, "display_name": {"Rana"}, "role": {"cashier"}}, &admin1), "users.error.main_till_unreachable")
}

// An invariant refusal from the main till maps to the existing key for it
// and writes nothing locally.
func TestUserWriteThrough_MainRefusalMapsToExistingKeys(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)

	// Username exists on the main till but not yet in this till's mirror.
	insertTestUser(t, main.dp.Db, "x-1", "rana", "Rana", "cashier")
	wantUsersRefused(t, postForm(mux, "/api/users", url.Values{"username": {"rana"}, "display_name": {"Rana 2"}, "role": {"cashier"}}, &admin1), "users.error.create")
	if taken, _ := data.NewAuthRepo(dp.Db).UsernameTaken(t.Context(), "rana"); taken {
		t.Fatal("refused create wrote locally")
	}

	// This till's stale mirror believes a second admin exists; the main
	// till knows adm-1 is the last one and refuses.
	pin, _ := auth.HashPIN("6060")
	insertTestUserWithPIN(t, dp.Db, "adm-9", "adm9", "Stale Admin", "admin", pin)
	wantUsersRefused(t, postForm(mux, "/api/users/adm-1/active", url.Values{"active": {"0"}}, &admin1), "users.error.last_admin")
	if !mustGetUser(t, dp, "adm-1").IsActive || !mustGetUser(t, main.dp, "adm-1").IsActive {
		t.Fatal("last-admin refusal must leave both tills unchanged")
	}
}

// Neither the PIN nor its hash may appear in anything logged on either
// till, on the success path or any failure path.
func TestUserWriteThrough_NeverLogsPINOrHash(t *testing.T) {
	var buf lockedBuffer
	restore := logging.CaptureForTest(&buf)
	t.Cleanup(restore)

	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)
	seedBoth(t, main, dp, "c-1", "cara", "Cara", "cashier", "")

	wantUsersOK(t, postForm(mux, "/api/users/c-1/pin", url.Values{"pin": {"480913"}}, &admin1))
	hash := mustGetUser(t, main.dp, "c-1").PinHash

	// Main-side refusal (bad hash posted directly) and an unreachable main.
	_ = postSyncUserApply(main.mux, `{"op":"set_pin","user_id":"c-1","pin_hash":"pbkdf2$sha256$1$bad$bad","actor_id":"adm-1"}`, syncUsersBearer)
	setReplicaSettings(t, dp.Settings, deadPrimaryURL(), syncUsersBearer)
	wantUsersRefused(t, postForm(mux, "/api/users/c-1/pin", url.Values{"pin": {"480913"}}, &admin1), "users.error.main_till_unreachable")

	out := buf.String()
	// The capture must actually have seen this change's log lines, or the
	// assertion below proves nothing.
	if !strings.Contains(out, "user sync: main till unreachable on set_pin") || !strings.Contains(out, "sync users: set_pin applied") {
		t.Fatalf("log capture did not see the write-through lines:\n%s", out)
	}
	parts := strings.Split(hash, "$")
	for _, secret := range []string{"480913", hash, parts[3], parts[4], "pbkdf2$sha256$1$bad$bad"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log output leaks %q:\n%s", secret, out)
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The main till decides with ITS roles, not this till's mirror: when it
// refuses an actor as forbidden, this till answers 403 exactly like its
// own local permission refusals (http.Error 403), with no local write.
func TestUserWriteThrough_MainForbiddenIs403(t *testing.T) {
	main := newUserSyncMain(t)
	mux, dp := newUserSyncReplica(t, main, main.srv.URL)
	pin, _ := auth.HashPIN("1212")
	seedBoth(t, main, dp, "c-1", "cara", "Cara", "cashier", pin)
	// This till's stale mirror believes x-1 is a super_admin; the main
	// till knows x-1 is a cashier.
	insertTestUserWithPIN(t, main.dp.Db, "x-1", "xan", "Xan", "cashier", pin)
	insertTestUserWithPIN(t, dp.Db, "x-1", "xan", "Xan", "super_admin", pin)
	xan := auth.User{ID: "x-1", Role: "super_admin"}

	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/api/users", url.Values{"username": {"rana"}, "display_name": {"Rana"}, "role": {"admin"}}},
		{"/api/users/c-1/pin", url.Values{"pin": {"4455"}}},
		{"/api/users/c-1/active", url.Values{"active": {"0"}}},
		{"/api/users/c-1/role", url.Values{"role": {"manager"}}},
		{"/api/users/c-1/promote-super-admin", nil},
	} {
		if rec := postForm(mux, tc.path, tc.form, &xan); rec.Code != http.StatusForbidden {
			t.Fatalf("%s = %d %q, want 403", tc.path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if taken, _ := data.NewAuthRepo(dp.Db).UsernameTaken(t.Context(), "rana"); taken {
		t.Fatal("forbidden create wrote locally")
	}
	if c := mustGetUser(t, dp, "c-1"); c.PinHash != pin || !c.IsActive || c.Role != "cashier" {
		t.Fatalf("forbidden change wrote locally: %+v", c)
	}

	// Own-PIN change by a staffer the main till has deactivated.
	if err := data.NewAuthRepo(main.dp.Db).SetUserActive(t.Context(), "c-1", false); err != nil {
		t.Fatal(err)
	}
	cara := auth.User{ID: "c-1", Role: "cashier"}
	if rec := postForm(mux, "/api/pin/change", url.Values{"current_pin": {"1212"}, "new_pin": {"3434"}, "new_pin2": {"3434"}}, &cara); rec.Code != http.StatusForbidden {
		t.Fatalf("own pin change by a deactivated staffer = %d %q, want 403", rec.Code, rec.Header().Get("Location"))
	}
	if mustGetUser(t, dp, "c-1").PinHash != pin {
		t.Fatal("forbidden own PIN change wrote locally")
	}
}
