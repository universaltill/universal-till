package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// Main-till side of ADR-0115 §1 (ut-docs#2755): POST /api/sync/users/apply
// is the bearer-authed endpoint an additional till's users page and own-PIN
// change write through to. It re-checks every invariant it can without the
// plaintext PIN, writes through its own AuthRepo, and answers the row
// WITHOUT pin_hash. Same shape as sync_held_sales_test.go: syncTill auth,
// JSON envelope, a real migrated database.

const syncUsersBearer = "bearer-t2"

func newSyncUsersTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })
	dp := &common.Deps{Db: dbase, Settings: settings.NewStore(dbase)}
	seedSyncOrdersTill(t, dp, "Till 2", syncUsersBearer)
	// adm-1 is the acting admin (audit_log.actor_id FKs onto users).
	pin, err := auth.HashPIN("7391")
	if err != nil {
		t.Fatal(err)
	}
	insertTestUserWithPIN(t, dbase, "adm-1", "adm1", "Admin One", "admin", pin)
	mux := http.NewServeMux()
	registerSyncUsers(mux, dp)
	return mux, dp
}

func postSyncUserApply(mux *http.ServeMux, body, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/users/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func applyBody(t *testing.T, in syncUserApplyRequest) string {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type syncUserApplyResponse struct {
	Data  *syncUserRow `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeSyncUserApply(t *testing.T, rec *httptest.ResponseRecorder) syncUserApplyResponse {
	t.Helper()
	var out syncUserApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	return out
}

func wantSyncUserError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
	out := decodeSyncUserApply(t, rec)
	if out.Data != nil || out.Error == nil || out.Error.Code != code {
		t.Fatalf("body = %q, want data null and error.code %q", rec.Body.String(), code)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestSyncUsersApply_RequiresBearer(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	body := applyBody(t, syncUserApplyRequest{Op: "create", Username: "x", DisplayName: "X", Role: "cashier", ActorID: "adm-1"})
	for _, bearer := range []string{"", "wrong"} {
		wantSyncUserError(t, postSyncUserApply(mux, body, bearer), http.StatusUnauthorized, "unauthorized")
	}
	if taken, _ := data.NewAuthRepo(dp.Db).UsernameTaken(t.Context(), "x"); taken {
		t.Fatal("a refused call must write nothing")
	}
}

// A till that itself follows a main till must never accept a write-through:
// its own next pull would revert it, the exact bug ADR-0115 closes.
func TestSyncUsersApply_RefusedOnReplica(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	setReplicaSettings(t, dp.Settings, "http://192.0.2.1:8080", "b-123")
	body := applyBody(t, syncUserApplyRequest{Op: "create", Username: "x", DisplayName: "X", Role: "cashier", ActorID: "adm-1"})
	wantSyncUserError(t, postSyncUserApply(mux, body, syncUsersBearer), http.StatusConflict, "replica")
	if taken, _ := data.NewAuthRepo(dp.Db).UsernameTaken(t.Context(), "x"); taken {
		t.Fatal("a replica must not apply a write-through")
	}
}

func TestSyncUsersApply_ValidationIs4xx(t *testing.T) {
	mux, _ := newSyncUsersTestDeps(t)
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"not json", `not json`, "invalid_body", 400},
		{"unknown op", `{"op":"drop","actor_id":"adm-1"}`, "invalid_op", 400},
		{"missing actor", `{"op":"create","username":"a","display_name":"A","role":"cashier"}`, "unknown_actor", 400},
		{"unknown actor", `{"op":"create","username":"a","display_name":"A","role":"cashier","actor_id":"ghost"}`, "unknown_actor", 400},
		{"bad role", `{"op":"create","username":"a","display_name":"A","role":"owner","actor_id":"adm-1"}`, "invalid_role", 400},
		{"missing name", `{"op":"create","username":"","display_name":"A","role":"cashier","actor_id":"adm-1"}`, "required", 400},
		{"unknown user", `{"op":"set_active","user_id":"ghost","active":true,"actor_id":"adm-1"}`, "not_found", 404},
		{"system user", `{"op":"set_active","user_id":"system","active":false,"actor_id":"adm-1"}`, "protected_user", 403},
		{"active missing", `{"op":"set_active","user_id":"adm-1","actor_id":"adm-1"}`, "required", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantSyncUserError(t, postSyncUserApply(mux, tc.body, syncUsersBearer), tc.status, tc.code)
		})
	}
}

func TestSyncUsersApply_CreateAssignsIDAndAudits(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	rec := postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "create", Username: "noor", DisplayName: "Noor", Role: "manager", ActorID: "adm-1"}), syncUsersBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pin_hash") {
		t.Fatalf("response must never carry pin_hash: %s", rec.Body.String())
	}
	out := decodeSyncUserApply(t, rec)
	if out.Data == nil || out.Data.ID == "" || out.Data.Username != "noor" || out.Data.Role != "manager" || !out.Data.Active {
		t.Fatalf("create answer = %s", rec.Body.String())
	}
	repo := data.NewAuthRepo(dp.Db)
	u, ok, err := repo.GetUser(t.Context(), out.Data.ID)
	if err != nil || !ok || u.Username != "noor" {
		t.Fatalf("main till row: %+v ok=%v err=%v", u, ok, err)
	}
	assertUserAudit(t, dp, "adm-1", out.Data.ID, "user_create")

	// Username uniqueness is the main till's to enforce.
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "create", Username: "noor", DisplayName: "Other", Role: "cashier", ActorID: "adm-1"}), syncUsersBearer),
		http.StatusConflict, "username_taken")
}

func TestSyncUsersApply_SetPINStoresHashAndRevokesSessions(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	insertTestUser(t, dp.Db, "c-1", "cara", "Cara", "cashier")
	repo := data.NewAuthRepo(dp.Db)
	if _, err := repo.InsertSession(t.Context(), "tok-c1", "c-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Invalid hash: refused, nothing stored.
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_pin", UserID: "c-1", PinHash: "1234", ActorID: "adm-1"}), syncUsersBearer),
		http.StatusBadRequest, "invalid_pin_hash")
	if u, _, _ := repo.GetUser(t.Context(), "c-1"); u.PinHash != "" {
		t.Fatalf("an invalid hash must not be stored, got %q", u.PinHash)
	}

	hash, _ := auth.HashPIN("5566")
	rec := postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_pin", UserID: "c-1", PinHash: hash, ActorID: "adm-1"}), syncUsersBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("set_pin = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), hash) || strings.Contains(rec.Body.String(), "pin_hash") {
		t.Fatalf("response must never carry the hash: %s", rec.Body.String())
	}
	if out := decodeSyncUserApply(t, rec); out.Data == nil || !out.Data.HasPIN {
		t.Fatalf("set_pin answer = %s", rec.Body.String())
	}
	if u, _, _ := repo.GetUser(t.Context(), "c-1"); u.PinHash != hash {
		t.Fatal("the main till must store exactly the hash it received")
	}
	if _, live, _ := repo.LookupSession(t.Context(), "tok-c1"); live {
		t.Fatal("a PIN change must revoke the target's sessions")
	}
	assertUserAudit(t, dp, "adm-1", "c-1", "user_pin_set")
}

func TestSyncUsersApply_LastAdminGuards(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	repo := data.NewAuthRepo(dp.Db)

	// adm-1 is the only active admin with a PIN: neither deactivating nor
	// demoting it may land on the main till.
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_active", UserID: "adm-1", Active: boolPtr(false), ActorID: "adm-1"}), syncUsersBearer),
		http.StatusConflict, "last_admin")
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "adm-1", Role: "cashier", ActorID: "adm-1"}), syncUsersBearer),
		http.StatusConflict, "last_admin")
	if u, _, _ := repo.GetUser(t.Context(), "adm-1"); !u.IsActive || u.Role != "admin" {
		t.Fatalf("guarded admin changed: %+v", u)
	}

	// Same for the last super_admin.
	pin, _ := auth.HashPIN("8080")
	insertTestUserWithPIN(t, dp.Db, "sa-1", "sa1", "Super", "super_admin", pin)
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "sa-1", Role: "admin", ActorID: "sa-1"}), syncUsersBearer),
		http.StatusConflict, "last_super_admin")
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_active", UserID: "sa-1", Active: boolPtr(false), ActorID: "sa-1"}), syncUsersBearer),
		http.StatusConflict, "last_super_admin")

	// With a second admin holding a PIN, the demotion applies and is audited.
	insertTestUserWithPIN(t, dp.Db, "adm-2", "adm2", "Admin Two", "admin", pin)
	if _, err := repo.InsertSession(t.Context(), "tok-a1", "adm-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec := postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "adm-1", Role: "manager", ActorID: "adm-2"}), syncUsersBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("set_role = %d: %s", rec.Code, rec.Body.String())
	}
	if out := decodeSyncUserApply(t, rec); out.Data == nil || out.Data.Role != "manager" {
		t.Fatalf("set_role answer = %s", rec.Body.String())
	}
	if _, live, _ := repo.LookupSession(t.Context(), "tok-a1"); live {
		t.Fatal("a role change must revoke the target's sessions")
	}
	assertUserAudit(t, dp, "adm-2", "adm-1", "user_role_changed")

	rec = postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_active", UserID: "adm-1", Active: boolPtr(false), ActorID: "adm-2"}), syncUsersBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("set_active = %d: %s", rec.Code, rec.Body.String())
	}
	if u, _, _ := repo.GetUser(t.Context(), "adm-1"); u.IsActive {
		t.Fatal("deactivation not applied")
	}
	assertUserAudit(t, dp, "adm-2", "adm-1", "user_deactivate")
}

func assertUserAudit(t *testing.T, dp *common.Deps, actorID, targetID, action string) {
	t.Helper()
	entries, err := data.NewPOSRepo(dp.Db).ListAudit(context.Background(), data.AuditFilters{EntityType: "user", ActorID: actorID})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action == action && e.EntityID == targetID {
			return
		}
	}
	t.Fatalf("no %s audit by %s on %s, got %+v", action, actorID, targetID, entries)
}

// The main till must not trust the additional till's permission checks: an
// actor_id is only as strong as the role the MAIN till holds for it. A
// compromised or buggy additional till holding a bearer must not be able
// to escalate by naming a cashier as the actor of a privileged change
// (independent-review blocker on ut-docs#2755).
func TestSyncUsersApply_ActorAuthorization(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	repo := data.NewAuthRepo(dp.Db)
	pin, _ := auth.HashPIN("2468")
	insertTestUserWithPIN(t, dp.Db, "c-1", "cara", "Cara", "cashier", pin)
	insertTestUserWithPIN(t, dp.Db, "c-2", "cody", "Cody", "cashier", pin)
	insertTestUserWithPIN(t, dp.Db, "m-1", "mia", "Mia", "manager", pin)
	insertTestUserWithPIN(t, dp.Db, "adm-2", "adm2", "Admin Two", "admin", pin)
	insertTestUserWithPIN(t, dp.Db, "adm-off", "admoff", "Admin Off", "admin", pin)
	insertTestUserWithPIN(t, dp.Db, "sa-1", "sa1", "Super", "super_admin", pin)
	if err := repo.SetUserActive(t.Context(), "adm-off", false); err != nil {
		t.Fatal(err)
	}
	newHash, _ := auth.HashPIN("1357")

	for _, tc := range []struct {
		name string
		in   syncUserApplyRequest
	}{
		{"cashier creates super_admin", syncUserApplyRequest{Op: "create", Username: "evil", DisplayName: "Evil", Role: "super_admin", ActorID: "c-1"}},
		{"cashier creates manager", syncUserApplyRequest{Op: "create", Username: "evil", DisplayName: "Evil", Role: "manager", ActorID: "c-1"}},
		{"admin creates super_admin", syncUserApplyRequest{Op: "create", Username: "evil", DisplayName: "Evil", Role: "super_admin", ActorID: "adm-1"}},
		{"cashier sets admin PIN", syncUserApplyRequest{Op: "set_pin", UserID: "adm-2", PinHash: newHash, ActorID: "c-1"}},
		{"cashier sets other cashier PIN", syncUserApplyRequest{Op: "set_pin", UserID: "c-2", PinHash: newHash, ActorID: "c-1"}},
		{"manager sets admin PIN", syncUserApplyRequest{Op: "set_pin", UserID: "adm-2", PinHash: newHash, ActorID: "m-1"}},
		{"cashier promotes self to super_admin", syncUserApplyRequest{Op: "set_role", UserID: "c-1", Role: "super_admin", ActorID: "c-1"}},
		{"cashier promotes admin to super_admin", syncUserApplyRequest{Op: "set_role", UserID: "adm-2", Role: "super_admin", ActorID: "c-1"}},
		{"admin promotes to super_admin", syncUserApplyRequest{Op: "set_role", UserID: "adm-2", Role: "super_admin", ActorID: "adm-1"}},
		{"manager makes cashier a manager", syncUserApplyRequest{Op: "set_role", UserID: "c-2", Role: "manager", ActorID: "m-1"}},
		{"cashier deactivates admin", syncUserApplyRequest{Op: "set_active", UserID: "adm-2", Active: boolPtr(false), ActorID: "c-1"}},
		{"inactive admin creates cashier", syncUserApplyRequest{Op: "create", Username: "evil", DisplayName: "Evil", Role: "cashier", ActorID: "adm-off"}},
		{"inactive admin sets PIN", syncUserApplyRequest{Op: "set_pin", UserID: "c-2", PinHash: newHash, ActorID: "adm-off"}},
		{"inactive admin sets own PIN", syncUserApplyRequest{Op: "set_pin", UserID: "adm-off", PinHash: newHash, ActorID: "adm-off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, tc.in), syncUsersBearer), http.StatusForbidden, "forbidden")
		})
	}
	// Nothing a refused call touched changed.
	if taken, _ := repo.UsernameTaken(t.Context(), "evil"); taken {
		t.Fatal("a refused create wrote a user")
	}
	for id, role := range map[string]string{"c-1": "cashier", "c-2": "cashier", "adm-2": "admin", "adm-off": "admin"} {
		u, _, _ := repo.GetUser(t.Context(), id)
		if u.Role != role || u.PinHash != pin {
			t.Fatalf("refused call changed %s: %+v", id, u)
		}
	}
	if u, _, _ := repo.GetUser(t.Context(), "adm-2"); !u.IsActive {
		t.Fatal("refused deactivation applied")
	}

	// Allowed: the staffer's own PIN change (auth_page.go's path) for any
	// role, and every op an actor's role really permits.
	for _, tc := range []struct {
		name string
		in   syncUserApplyRequest
	}{
		{"cashier sets own PIN", syncUserApplyRequest{Op: "set_pin", UserID: "c-1", PinHash: newHash, ActorID: "c-1"}},
		{"manager sets cashier PIN", syncUserApplyRequest{Op: "set_pin", UserID: "c-2", PinHash: newHash, ActorID: "m-1"}},
		{"manager creates cashier", syncUserApplyRequest{Op: "create", Username: "newc", DisplayName: "New C", Role: "cashier", ActorID: "m-1"}},
		{"admin creates manager", syncUserApplyRequest{Op: "create", Username: "newm", DisplayName: "New M", Role: "manager", ActorID: "adm-1"}},
		{"admin sets admin PIN", syncUserApplyRequest{Op: "set_pin", UserID: "adm-2", PinHash: newHash, ActorID: "adm-1"}},
		{"admin makes cashier a manager", syncUserApplyRequest{Op: "set_role", UserID: "c-2", Role: "manager", ActorID: "adm-1"}},
		{"super_admin promotes admin", syncUserApplyRequest{Op: "set_role", UserID: "adm-2", Role: "super_admin", ActorID: "sa-1"}},
		{"super_admin creates super_admin", syncUserApplyRequest{Op: "create", Username: "newsa", DisplayName: "New SA", Role: "super_admin", ActorID: "sa-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postSyncUserApply(mux, applyBody(t, tc.in), syncUsersBearer); rec.Code != http.StatusOK {
				t.Fatalf("%s = %d: %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
	if u, _, _ := repo.GetUser(t.Context(), "c-1"); u.PinHash != newHash {
		t.Fatal("own PIN change not applied")
	}
	if u, _, _ := repo.GetUser(t.Context(), "adm-2"); u.Role != "super_admin" {
		t.Fatal("super_admin promotion not applied")
	}
}

// The last-admin guard and the write it protects are one decision on the
// main till: a second call made after the first landed must see the first
// one's effect and be refused (review finding: the count ran outside the
// write's transaction).
func TestSyncUsersApply_LastAdminGuardSeesPriorWrite(t *testing.T) {
	mux, dp := newSyncUsersTestDeps(t)
	pin, _ := auth.HashPIN("2468")
	insertTestUserWithPIN(t, dp.Db, "adm-2", "adm2", "Admin Two", "admin", pin)
	insertTestUserWithPIN(t, dp.Db, "sa-1", "sa1", "Super", "super_admin", pin)

	if rec := postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_active", UserID: "adm-1", Active: boolPtr(false), ActorID: "sa-1"}), syncUsersBearer); rec.Code != http.StatusOK {
		t.Fatalf("first deactivation = %d: %s", rec.Code, rec.Body.String())
	}
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_role", UserID: "adm-2", Role: "manager", ActorID: "sa-1"}), syncUsersBearer),
		http.StatusConflict, "last_admin")
	wantSyncUserError(t, postSyncUserApply(mux, applyBody(t, syncUserApplyRequest{Op: "set_active", UserID: "adm-2", Active: boolPtr(false), ActorID: "sa-1"}), syncUsersBearer),
		http.StatusConflict, "last_admin")
	if u, _, _ := data.NewAuthRepo(dp.Db).GetUser(t.Context(), "adm-2"); !u.IsActive || u.Role != "admin" {
		t.Fatalf("the last admin changed: %+v", u)
	}
}
