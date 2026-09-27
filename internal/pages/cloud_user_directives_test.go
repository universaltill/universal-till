package pages

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/hpke"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// Till user directives, main-till apply (ut-docs
// reference/till-user-directives.md §1, §4, §5; ADR-0115 amendment
// 2026-09-25), driven through buildCloudHooks exactly as Tick does.

// pinVector is the shared cross-repo seal vector; the canonical copy is
// ut-docs reference/contracts/till-user-pin-v1.json.
type pinVector struct {
	RecipientSK     string `json:"recipient_sk"`
	KID             string `json:"kid"`
	PublicKeyB64URL string `json:"public_key_b64url"`
	StoreExternalID string `json:"store_external_id"`
	DirectiveID     string `json:"directive_id"`
	Type            string `json:"type"`
	UserID          string `json:"user_id"`
	PIN             string `json:"pin"`
	PINSealed       string `json:"pin_sealed"`
}

type userDirectiveEnv struct {
	dp    *common.Deps
	hooks cloudsync.Hooks
	vec   pinVector
	skR   *ecdh.PrivateKey
	logs  *lockedBuf
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newUserDirectiveEnv is a main till whose directive key is the vector's
// recipient key, at the production path under a temp data dir, with the
// vector's store id; every log line is captured.
func newUserDirectiveEnv(t *testing.T) *userDirectiveEnv {
	t.Helper()
	dp := newCloudSyncTestDeps(t) // chdirs to the repo root
	raw, err := os.ReadFile(filepath.Join("internal", "directivekey", "testdata", "till-user-pin-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v pinVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })
	sk, _ := hex.DecodeString(v.RecipientSK)
	keyPath := paths.Data("secrets", "directive-x25519.key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, sk, 0o600); err != nil {
		t.Fatal(err)
	}
	skR, err := ecdh.X25519().NewPrivateKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	dp.Cfg.Marketplace.StoreID = v.StoreExternalID
	logs := &lockedBuf{}
	t.Cleanup(logging.CaptureForTest(logs))
	return &userDirectiveEnv{dp: dp, hooks: buildCloudHooks(dp, nil), vec: v, skR: skR, logs: logs}
}

// seal seals pin to the till's key for one directive, as ut-cloud does.
func (e *userDirectiveEnv) seal(t *testing.T, pin, directiveID, typ, userID string) string {
	t.Helper()
	skE, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	aad := directivekey.AAD(e.vec.StoreExternalID, directiveID, typ, userID)
	enc, ct, err := hpke.SealDeterministic(e.skR.PublicKey(), skE, []byte(directivekey.Info), []byte(aad), []byte(pin))
	if err != nil {
		t.Fatal(err)
	}
	return "v1." + e.vec.KID + "." + base64.RawURLEncoding.EncodeToString(enc) + "." + base64.RawURLEncoding.EncodeToString(ct)
}

// addUser inserts a user (optionally with a PIN) straight through the repo.
func (e *userDirectiveEnv) addUser(t *testing.T, id, username, role, pin string, active bool) {
	t.Helper()
	ctx := t.Context()
	repo := data.NewAuthRepo(e.dp.Db)
	tx, err := e.dp.Db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateUserWithID(ctx, tx, id, username, strings.ToUpper(username[:1])+username[1:], role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if pin != "" {
		h, err := auth.HashPIN(pin)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.SetUserPIN(ctx, id, h); err != nil {
			t.Fatal(err)
		}
	}
	if !active {
		if err := repo.SetUserActive(ctx, id, false); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *userDirectiveEnv) user(t *testing.T, id string) data.UserRow {
	t.Helper()
	u, ok, err := data.NewAuthRepo(e.dp.Db).GetUser(t.Context(), id)
	if err != nil || !ok {
		t.Fatalf("user %s: %v %v", id, ok, err)
	}
	return u
}

func (e *userDirectiveEnv) session(t *testing.T, userID, tok string) {
	t.Helper()
	if _, err := data.NewAuthRepo(e.dp.Db).InsertSession(t.Context(), tok, userID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func (e *userDirectiveEnv) revoked(t *testing.T, tok string) bool {
	t.Helper()
	var revoked sql.NullString
	if err := e.dp.Db.QueryRow(`SELECT revoked_at FROM sessions WHERE token_hash = ?`, tok).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked.Valid
}

func (e *userDirectiveEnv) auditRows(t *testing.T, userID string) []string {
	t.Helper()
	rows, err := e.dp.Db.Query(`SELECT action || ' ' || COALESCE(data_json,'') FROM audit_log WHERE entity_type = 'user' AND entity_id = ? AND actor_id = 'system' ORDER BY created_at`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// assertNoPIN scans every log line, the whole audit table and the given
// result messages for a PIN or a sealed value.
func (e *userDirectiveEnv) assertNoPIN(t *testing.T, secrets []string, msgs ...string) {
	t.Helper()
	var audit strings.Builder
	rows, err := e.dp.Db.Query(`SELECT COALESCE(data_json,'') || ' ' || action || ' ' || entity_id FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		audit.WriteString(s + "\n")
	}
	rows.Close()
	logs := e.logs.String()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(logs, s) {
			t.Fatalf("a log line carries %q:\n%s", s, logs)
		}
		if strings.Contains(audit.String(), s) {
			t.Fatalf("the audit table carries %q:\n%s", s, audit.String())
		}
		for _, m := range msgs {
			if strings.Contains(m, s) {
				t.Fatalf("result message %q carries %q", m, s)
			}
		}
	}
}

func setPIN(e *userDirectiveEnv, did, userID, sealed string) cloudsync.UserDirective {
	return cloudsync.UserDirective{DirectiveID: did, Type: "set_user_pin", CreatedBy: "owner-1", UserID: userID, PINSealed: &sealed}
}

func TestCloudUserDirective_SharedVectorPINStoredAsHash(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, e.vec.UserID, "anna", "cashier", "", true)
	e.session(t, e.vec.UserID, "tok-anna")

	msg, err := e.hooks.SetUserPIN(ctx, setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed))
	if err != nil {
		t.Fatalf("set pin: %v", err)
	}
	u := e.user(t, e.vec.UserID)
	if auth.ValidatePINHash(u.PinHash) != nil || !auth.VerifyPIN(e.vec.PIN, u.PinHash) {
		t.Fatalf("stored %q is not a HashPIN hash of the PIN", u.PinHash)
	}
	if !e.revoked(t, "tok-anna") {
		t.Fatal("a PIN change must revoke the user's sessions")
	}
	audit := e.auditRows(t, e.vec.UserID)
	if len(audit) != 1 || !strings.HasPrefix(audit[0], "cloud_user_pin_set ") ||
		!strings.Contains(audit[0], `"via":"cloud"`) || !strings.Contains(audit[0], `"actor":"owner-1"`) {
		t.Fatalf("audit = %v", audit)
	}
	e.assertNoPIN(t, []string{e.vec.PIN, e.vec.PINSealed, strings.Split(e.vec.PINSealed, ".")[3], u.PinHash}, msg)

	// Idempotent: the same directive re-applied passes (uniqueness excludes
	// the target itself).
	if _, err := e.hooks.SetUserPIN(ctx, setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed)); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if !auth.VerifyPIN(e.vec.PIN, e.user(t, e.vec.UserID).PinHash) {
		t.Fatal("re-apply lost the PIN")
	}
}

func TestCloudUserDirective_WrongKidOrAADFails(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, e.vec.UserID, "anna", "cashier", "", true)
	parts := strings.Split(e.vec.PINSealed, ".")
	const sentence = "This PIN was sent to a till key that no longer exists. Set the PIN again."
	for name, u := range map[string]cloudsync.UserDirective{
		"wrong kid":          setPIN(e, e.vec.DirectiveID, e.vec.UserID, "v1.ffffffffffffffff."+parts[2]+"."+parts[3]),
		"other directive id": setPIN(e, "another-directive", e.vec.UserID, e.vec.PINSealed),
	} {
		if _, err := e.hooks.SetUserPIN(ctx, u); err == nil || err.Error() != sentence {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A store id other than the one the PIN was sealed for fails too.
	e.dp.Cfg.Marketplace.StoreID = "another-store"
	if _, err := e.hooks.SetUserPIN(ctx, setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed)); err == nil || err.Error() != sentence {
		t.Errorf("other store: %v", err)
	}
	if e.user(t, e.vec.UserID).PinHash != "" {
		t.Fatal("a failed open wrote a PIN")
	}
	if len(e.auditRows(t, e.vec.UserID)) != 0 {
		t.Fatal("a failed directive wrote an audit row")
	}
}

func TestCloudUserDirective_KeyFileGone(t *testing.T) {
	e := newUserDirectiveEnv(t)
	e.addUser(t, e.vec.UserID, "anna", "cashier", "", true)
	if err := os.Remove(paths.Data("secrets", "directive-x25519.key")); err != nil {
		t.Fatal(err)
	}
	_, err := e.hooks.SetUserPIN(t.Context(), setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed))
	if err == nil || err.Error() != "This PIN was sent to a till key that no longer exists. Set the PIN again." {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(paths.Data("secrets", "directive-x25519.key")); !os.IsNotExist(err) {
		t.Fatal("applying a PIN directive created a key")
	}
}

func TestCloudUserDirective_PINInUseNeverNamesTheOtherUser(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, e.vec.UserID, "anna", "cashier", "", true)
	e.addUser(t, "u-bob", "bobholder", "cashier", e.vec.PIN, true)
	_, err := e.hooks.SetUserPIN(ctx, setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed))
	if err == nil || err.Error() != "This PIN is already in use. Choose a different PIN." {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "bob") || strings.Contains(err.Error(), "u-bob") {
		t.Fatal("the refusal names the other user")
	}
	if e.user(t, e.vec.UserID).PinHash != "" {
		t.Fatal("a refused PIN was stored")
	}
	// An INACTIVE holder does not block (FindUserByPIN's rule).
	if err := data.NewAuthRepo(e.dp.Db).SetUserActive(ctx, "u-bob", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hooks.SetUserPIN(ctx, setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed)); err != nil {
		t.Fatalf("inactive holder blocked: %v", err)
	}
	e.assertNoPIN(t, []string{e.vec.PIN, e.vec.PINSealed})
}

// PBKDF2 (the uniqueness VerifyPIN loop and HashPIN) must run while no
// write transaction is open: each call probes the write lock with a write
// of its own on the single-connection test pool, which cannot complete
// while the hook holds a transaction.
func TestCloudUserDirective_PBKDF2RunsWithoutTheWriteLock(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, e.vec.UserID, "anna", "cashier", "", true)
	e.addUser(t, "u-other", "other", "cashier", "9999", true)
	probes := 0
	probe := func() {
		probes++
		pctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := e.dp.Db.ExecContext(pctx, `INSERT INTO settings (key, value) VALUES ('probe.write_lock', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, time.Now().String()); err != nil {
			t.Errorf("a write could not run during PBKDF2 (write lock held?): %v", err)
		}
	}
	origHash, origVerify := userDirectivePINHash, userDirectivePINVerify
	t.Cleanup(func() { userDirectivePINHash, userDirectivePINVerify = origHash, origVerify })
	userDirectivePINHash = func(pin string) (string, error) { probe(); return origHash(pin) }
	userDirectivePINVerify = func(pin, stored string) bool { probe(); return origVerify(pin, stored) }

	// Bounded so a regression that opens the transaction first fails here in
	// seconds (the hook's own reads block on the held connection) instead of
	// at the package's 10-minute timeout (review nit-4).
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := e.hooks.SetUserPIN(hctx, setPIN(e, e.vec.DirectiveID, e.vec.UserID, e.vec.PINSealed)); err != nil {
		t.Fatalf("SetUserPIN (write lock held during PBKDF2?): %v", err)
	}
	if probes < 2 {
		t.Fatalf("probes = %d: the verify loop and the hash must both run", probes)
	}
}

func TestCloudUserDirective_CreateUpdateReactivateDeactivate(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, "u-admin", "boss", "admin", "1111", true)
	const id = "5b0a8c3e-2222-4222-8222-222222222222"
	sealed := e.seal(t, "5678", "dir-c1", "save_user", id)
	create := cloudsync.UserDirective{DirectiveID: "dir-c1", Type: "save_user", CreatedBy: "owner-1", UserID: id, Create: true,
		Username: sp("carla"), DisplayName: sp("Carla"), Role: sp("cashier"), PINSealed: &sealed}
	msg, err := e.hooks.SaveUser(ctx, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	u := e.user(t, id)
	if u.Username != "carla" || u.DisplayName != "Carla" || u.Role != "cashier" || !u.IsActive || !auth.VerifyPIN("5678", u.PinHash) {
		t.Fatalf("created %+v", u)
	}
	// Idempotent: the lost-result replay leaves one user, same state.
	if _, err := e.hooks.SaveUser(ctx, create); err != nil {
		t.Fatalf("replay: %v", err)
	}
	var n int
	_ = e.dp.Db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'carla'`).Scan(&n)
	if n != 1 {
		t.Fatalf("users named carla = %d", n)
	}

	// Update: absent means keep.
	if _, err := e.hooks.SaveUser(ctx, cloudsync.UserDirective{DirectiveID: "dir-u1", Type: "save_user", UserID: id, Role: sp("manager")}); err != nil {
		t.Fatalf("role update: %v", err)
	}
	if u := e.user(t, id); u.Role != "manager" || u.Username != "carla" || !auth.VerifyPIN("5678", u.PinHash) {
		t.Fatalf("after role update %+v", u)
	}
	if _, err := e.hooks.SaveUser(ctx, cloudsync.UserDirective{DirectiveID: "dir-u2", Type: "save_user", UserID: id, Username: sp("carla2"), DisplayName: sp("Carla Two")}); err != nil {
		t.Fatalf("profile update: %v", err)
	}
	if u := e.user(t, id); u.Username != "carla2" || u.DisplayName != "Carla Two" {
		t.Fatalf("after profile update %+v", u)
	}

	// Deactivate revokes sessions; re-applying is a no-op success.
	e.session(t, id, "tok-carla")
	if _, err := e.hooks.DeactivateUser(ctx, cloudsync.UserDirective{DirectiveID: "dir-d1", Type: "deactivate_user", UserID: id}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if e.user(t, id).IsActive || !e.revoked(t, "tok-carla") {
		t.Fatal("deactivate did not deactivate and revoke")
	}
	if _, err := e.hooks.DeactivateUser(ctx, cloudsync.UserDirective{DirectiveID: "dir-d1", Type: "deactivate_user", UserID: id}); err != nil {
		t.Fatalf("deactivate replay: %v", err)
	}

	// Reactivation needs a fresh PIN.
	if _, err := e.hooks.SaveUser(ctx, cloudsync.UserDirective{DirectiveID: "dir-r1", Type: "save_user", UserID: id, Active: bp(true)}); err == nil ||
		err.Error() != "Reactivating a user needs a new PIN." {
		t.Fatalf("reactivate without pin: %v", err)
	}
	if e.user(t, id).IsActive {
		t.Fatal("reactivated without a PIN")
	}
	re := e.seal(t, "2468", "dir-r2", "save_user", id)
	if _, err := e.hooks.SaveUser(ctx, cloudsync.UserDirective{DirectiveID: "dir-r2", Type: "save_user", UserID: id, Active: bp(true), PINSealed: &re}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if u := e.user(t, id); !u.IsActive || !auth.VerifyPIN("2468", u.PinHash) {
		t.Fatalf("after reactivate %+v", u)
	}
	audit := e.auditRows(t, id)
	if len(audit) < 5 {
		t.Fatalf("audit rows = %v", audit)
	}
	for _, a := range audit {
		if !strings.Contains(a, `"via":"cloud"`) {
			t.Fatalf("audit row without provenance: %s", a)
		}
	}
	e.assertNoPIN(t, []string{"5678", "2468", sealed, re}, msg)
}

func TestCloudUserDirective_Refusals(t *testing.T) {
	e := newUserDirectiveEnv(t)
	ctx := t.Context()
	e.addUser(t, "u-admin", "boss", "admin", "1111", true)
	e.addUser(t, "u-super", "root", "super_admin", "2222", true)
	e.addUser(t, "u-cash", "cash", "cashier", "3333", true)
	const protected = "Super admins and built-in till users can only be changed at a till."

	sealedFor := func(did, typ, uid string) *string { s := e.seal(t, "7777", did, typ, uid); return &s }
	for name, c := range map[string]struct {
		hook func(context.Context, cloudsync.UserDirective) (string, error)
		u    cloudsync.UserDirective
		want string
	}{
		"super_admin target pin":        {e.hooks.SetUserPIN, cloudsync.UserDirective{DirectiveID: "a", Type: "set_user_pin", UserID: "u-super", PINSealed: sealedFor("a", "set_user_pin", "u-super")}, protected},
		"super_admin target deactivate": {e.hooks.DeactivateUser, cloudsync.UserDirective{DirectiveID: "b", Type: "deactivate_user", UserID: "u-super"}, protected},
		"system target":                 {e.hooks.DeactivateUser, cloudsync.UserDirective{DirectiveID: "c", Type: "deactivate_user", UserID: "system"}, protected},
		"kiosk target":                  {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "d", Type: "save_user", UserID: "kiosk", Role: sp("cashier")}, protected},
		"promote to super":              {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "e", Type: "save_user", UserID: "u-cash", Role: sp("super_admin")}, protected},
		"create as super":               {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "f", Type: "save_user", UserID: "new-1", Create: true, Username: sp("x"), Role: sp("super_admin"), PINSealed: sealedFor("f", "save_user", "new-1")}, protected},
		"unknown role":                  {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "g", Type: "save_user", UserID: "u-cash", Role: sp("owner")}, "The role owner can't be set from the cloud."},
		"last admin deactivate":         {e.hooks.DeactivateUser, cloudsync.UserDirective{DirectiveID: "h", Type: "deactivate_user", UserID: "u-admin"}, "The last active admin can't be deactivated or demoted."},
		"last admin demote":             {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "i", Type: "save_user", UserID: "u-admin", Role: sp("manager")}, "The last active admin can't be deactivated or demoted."},
		"username taken update":         {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "j", Type: "save_user", UserID: "u-cash", Username: sp("boss")}, "The username boss is already taken."},
		"username taken create":         {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "k", Type: "save_user", UserID: "new-2", Create: true, Username: sp("cash"), Role: sp("cashier"), PINSealed: sealedFor("k", "save_user", "new-2")}, "The username cash is already taken."},
		"missing user pin":              {e.hooks.SetUserPIN, cloudsync.UserDirective{DirectiveID: "l", Type: "set_user_pin", UserID: "ghost", PINSealed: sealedFor("l", "set_user_pin", "ghost")}, "User ghost does not exist on the main till."},
		"missing user update":           {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "m", Type: "save_user", UserID: "ghost", Role: sp("cashier")}, "User ghost does not exist on the main till."},
		"missing user deact":            {e.hooks.DeactivateUser, cloudsync.UserDirective{DirectiveID: "n", Type: "deactivate_user", UserID: "ghost"}, "User ghost does not exist on the main till."},
		"create without role":           {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "q", Type: "save_user", UserID: "new-4", Create: true, Username: sp("z"), PINSealed: sealedFor("q", "save_user", "new-4")}, "missing role"},
		"create without pin":            {e.hooks.SaveUser, cloudsync.UserDirective{DirectiveID: "o", Type: "save_user", UserID: "new-3", Create: true, Username: sp("y"), Role: sp("cashier")}, "A new user needs a PIN."},
		"bad pin format":                {e.hooks.SetUserPIN, cloudsync.UserDirective{DirectiveID: "p", Type: "set_user_pin", UserID: "u-cash", PINSealed: func() *string { s := e.seal(t, "12a4", "p", "set_user_pin", "u-cash"); return &s }()}, "The PIN must be 4 to 8 digits."},
	} {
		_, err := c.hook(ctx, c.u)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if u := e.user(t, "u-admin"); u.Role != "admin" || !u.IsActive {
		t.Fatalf("admin changed: %+v", u)
	}
	if u := e.user(t, "u-cash"); u.Role != "cashier" || u.Username != "cash" {
		t.Fatalf("cashier changed: %+v", u)
	}
	for _, id := range []string{"new-1", "new-2", "new-3", "new-4"} {
		if _, ok, _ := data.NewAuthRepo(e.dp.Db).GetUser(ctx, id); ok {
			t.Fatalf("refused create %s wrote a user", id)
		}
	}
	e.assertNoPIN(t, []string{"7777", "12a4"})
}

// A satellite till never creates or reports a directive key, and the
// hooks refuse there (Tick already skips the types; this is the second
// line).
func TestCloudUserDirective_SatelliteNoKeyNoApply(t *testing.T) {
	e := newUserDirectiveEnv(t)
	keyPath := paths.Data("secrets", "directive-x25519.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	setReplica(t, e.dp)
	hooks := buildCloudHooks(e.dp, nil)
	extra := hooks.DeviceExtra(t.Context())
	if _, ok := extra["directive_key"]; ok {
		t.Fatal("a satellite reported a directive key")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("a satellite created a directive key")
	}
	if _, ok := extra["users"]; ok {
		t.Fatal("a satellite reported users")
	}
	e.addUser(t, "u-cash", "cash", "cashier", "", true)
	if _, err := hooks.DeactivateUser(t.Context(), cloudsync.UserDirective{DirectiveID: "x", Type: "deactivate_user", UserID: "u-cash"}); err == nil {
		t.Fatal("a satellite applied deactivate_user")
	}
	if !e.user(t, "u-cash").IsActive {
		t.Fatal("a satellite deactivated a user")
	}
}

// The main till creates its key at the first check-in and reports it on
// every one after.
func TestCloudUserDirective_MainTillReportsKey(t *testing.T) {
	e := newUserDirectiveEnv(t)
	keyPath := paths.Data("secrets", "directive-x25519.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	hooks := buildCloudHooks(e.dp, nil)
	rep1, ok := hooks.DeviceExtra(t.Context())["directive_key"].(map[string]string)
	if !ok || len(rep1["kid"]) != 16 || rep1["public_key"] == "" {
		t.Fatalf("first report: %v", rep1)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key file: %d %v", len(raw), err)
	}
	rep2, _ := hooks.DeviceExtra(t.Context())["directive_key"].(map[string]string)
	if rep2["kid"] != rep1["kid"] || rep2["public_key"] != rep1["public_key"] {
		t.Fatalf("key changed between check-ins: %v %v", rep1, rep2)
	}
	if strings.Contains(e.logs.String(), hex.EncodeToString(raw)) || strings.Contains(e.logs.String(), base64.RawURLEncoding.EncodeToString(raw)) {
		t.Fatal("the private key reached the log")
	}
	// An unparseable file: no key reported, file untouched.
	if err := os.WriteFile(keyPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := buildCloudHooks(e.dp, nil).DeviceExtra(t.Context())["directive_key"]; ok {
		t.Fatal("reported a key for an unparseable file")
	}
	if got, _ := os.ReadFile(keyPath); string(got) != "garbage" {
		t.Fatal("the unparseable key file was overwritten")
	}
}

// §5: the main till's config report carries users without system/kiosk,
// never a hash or sessions.
func TestCloudUsersReport(t *testing.T) {
	e := newUserDirectiveEnv(t)
	e.addUser(t, "u-admin", "boss", "admin", "1111", true)
	e.addUser(t, "u-gone", "gone", "cashier", "", false)
	extra := buildCloudHooks(e.dp, nil).DeviceExtra(t.Context())
	users, ok := extra["users"].([]cloudUserReport)
	if !ok {
		t.Fatalf("users = %#v", extra["users"])
	}
	byID := map[string]cloudUserReport{}
	for _, u := range users {
		byID[u.UserID] = u
	}
	if _, ok := byID["system"]; ok {
		t.Fatal("system reported")
	}
	if _, ok := byID["kiosk"]; ok {
		t.Fatal("kiosk reported")
	}
	if b := byID["u-admin"]; b.Username != "boss" || b.Role != "admin" || !b.Active || !b.HasPIN {
		t.Fatalf("boss = %+v", b)
	}
	if g := byID["u-gone"]; g.Active || g.HasPIN {
		t.Fatalf("gone = %+v", g)
	}
	raw, _ := json.Marshal(extra["users"])
	for _, bad := range []string{"pin_hash", "pbkdf2", "session"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("users report carries %q: %s", bad, raw)
		}
	}
	for _, key := range []string{`"user_id"`, `"username"`, `"display_name"`, `"role"`, `"active"`, `"has_pin"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("users report lacks %s: %s", key, raw)
		}
	}
}
