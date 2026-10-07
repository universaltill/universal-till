package pages

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2781: a joined till's manager-assigned role (additional |
// satellite), chosen before the till is created on both join paths,
// changeable later on the main till's Tills page, learnt by the till after
// its next pull, and gating its device profile (a satellite runs as the
// self-order kiosk only).

// auditRowsFor returns every audit row with action, payload decoded, plus
// _actor/_entity_type/_entity_id (renameTillAudits' shape, any action).
func auditRowsFor(t *testing.T, dp *common.Deps, action string) []map[string]any {
	t.Helper()
	rows, err := dp.Db.QueryContext(t.Context(),
		`SELECT actor_id, entity_type, entity_id, COALESCE(data_json, '') FROM audit_log WHERE action = ? ORDER BY created_at`, action)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor, typ, id, js string
		if err := rows.Scan(&actor, &typ, &id, &js); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		row := map[string]any{}
		if js != "" {
			if err := json.Unmarshal([]byte(js), &row); err != nil {
				t.Fatalf("audit payload %q: %v", js, err)
			}
		}
		row["_actor"], row["_entity_type"], row["_entity_id"] = actor, typ, id
		out = append(out, row)
	}
	return out
}

// enrolWith posts token to /api/sync/enroll and returns the till id and the
// role the response reports.
func enrolWith(t *testing.T, mux *http.ServeMux, token, name string) (tillID, role string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			TillID string `json:"till_id"`
			Role   string `json:"role"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Data.TillID, out.Data.Role
}

func persistedRole(t *testing.T, dp *common.Deps, tillID string) string {
	t.Helper()
	role, ok, err := data.NewTillsRepo(dp.Db).RoleByID(t.Context(), tillID)
	if err != nil || !ok {
		t.Fatalf("RoleByID(%s) = %q ok=%v err=%v", tillID, role, ok, err)
	}
	return role
}

var shortCodeRe = regexp.MustCompile(`class="pairing-short-code"[^>]*>([^<]+)<`)
var longCodeRe = regexp.MustCompile(`<code style="user-select:all">([^<]+)</code>`)

// mintPairingCode posts the Tills page's "Show pairing code" with role and
// returns the short code and the long token it shows.
func mintPairingCode(t *testing.T, mux *http.ServeMux, role string) (short, long string) {
	t.Helper()
	form := url.Values{"url": {"http://192.168.1.10:8080"}}
	if role != "" {
		form.Set("role", role)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll-token = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	sm, lm := shortCodeRe.FindStringSubmatch(body), longCodeRe.FindStringSubmatch(body)
	if sm == nil || lm == nil {
		t.Fatalf("no short/long code in the panel: %s", body)
	}
	_, token, err := decodeEnrollCode(lm[1])
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(sm[1]), token
}

// "Paste a pairing code": the role chosen on the card that mints the code
// is the role the till is created with — by the long code and by the
// short one alike — and it reaches the enrol response and audit row.
func TestPairingCode_ChosenRoleIsPersistedOnEnrolment(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)

	_, long := mintPairingCode(t, mux, "satellite")
	id, role := enrolWith(t, mux, long, "Kiosk 1")
	if role != data.TillRoleSatellite || persistedRole(t, dp, id) != data.TillRoleSatellite {
		t.Fatalf("long code: response role %q, persisted %q; want satellite", role, persistedRole(t, dp, id))
	}

	short, _ := mintPairingCode(t, mux, "satellite")
	id2, role2 := enrolWith(t, mux, short, "Kiosk 2")
	if role2 != data.TillRoleSatellite || persistedRole(t, dp, id2) != data.TillRoleSatellite {
		t.Fatalf("short code: response role %q, persisted %q; want satellite", role2, persistedRole(t, dp, id2))
	}

	// No role picked (an older page, or the API): today's behaviour.
	_, long3 := mintPairingCode(t, mux, "")
	id3, role3 := enrolWith(t, mux, long3, "Till 4")
	if role3 != data.TillRoleAdditional || persistedRole(t, dp, id3) != data.TillRoleAdditional {
		t.Fatalf("default: response role %q, persisted %q; want additional", role3, persistedRole(t, dp, id3))
	}

	enrolled := auditRowsFor(t, dp, "till_enrolled")
	if len(enrolled) != 3 || enrolled[0]["role"] != "satellite" || enrolled[2]["role"] != "additional" {
		t.Fatalf("till_enrolled audit rows = %+v, want the role recorded", enrolled)
	}
}

func TestPairingCode_UnknownRoleRefusedBeforeMinting(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token",
		strings.NewReader(url.Values{"role": {"register"}, "url": {"http://192.168.1.10:8080"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "pairing-short-code") {
		t.Fatalf("unknown role = %d %s; want 400 and no code", rec.Code, rec.Body.String())
	}
}

// The role rides the token, not the request: a burnt token's role cannot
// leak to a later one, and an expired entry is pruned with its token.
func TestEnrolTokens_RoleTravelsWithItsTokenOnly(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	sat := e.issue()
	e.setRole(sat, data.TillRoleSatellite)
	plain := e.issue()
	if role, ok := e.consumeWithRole(plain); !ok || role != data.TillRoleAdditional {
		t.Fatalf("plain token = %q, %v; want additional", role, ok)
	}
	if role, ok := e.consumeWithRole(sat); !ok || role != data.TillRoleSatellite {
		t.Fatalf("satellite token = %q, %v", role, ok)
	}
	if role, ok := e.consumeWithRole(sat); ok || role != data.TillRoleAdditional {
		t.Fatalf("reused token = %q, %v; want refused, no role", role, ok)
	}
	if len(e.roles) != 0 {
		t.Fatalf("roles left behind: %v", e.roles)
	}
	// setRole on a token that is not live records nothing.
	e.setRole("never-issued", data.TillRoleSatellite)
	if len(e.roles) != 0 {
		t.Fatalf("setRole recorded a role for a dead token: %v", e.roles)
	}
}

// "Find on this network": the role picked on the approval card is the
// role the till is created with when it enrols with the approved token.
func TestApprovePairRequest_ChosenRoleIsPersistedOnEnrolment(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	secret := "kiosk-secret"
	rec := postPairRequest(t, mux, "Kiosk", commitOf(secret), "10.0.0.8:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// An unknown role is refused before anything is approved.
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve",
		strings.NewReader(url.Values{"role": {"backoffice"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approve with an unknown role = %d, want 400", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve",
		strings.NewReader(url.Values{"role": {"satellite"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", rec.Code, rec.Body.String())
	}
	if a := auditRowsFor(t, dp, "pairing_approved"); len(a) != 1 || a[0]["role"] != "satellite" {
		t.Fatalf("pairing_approved audit = %+v, want role satellite", a)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+created.Data.ID+"?request_secret="+secret, nil))
	var tok struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.Data.Token == "" {
		t.Fatalf("retrieve token: %d %s", rec.Code, rec.Body.String())
	}
	id, role := enrolWith(t, mux, tok.Data.Token, "Kiosk")
	if role != data.TillRoleSatellite || persistedRole(t, dp, id) != data.TillRoleSatellite {
		t.Fatalf("approved satellite enrolled as %q / %q", role, persistedRole(t, dp, id))
	}
}

// The pending-request card offers the role picker and sends it with Approve
// (not Deny).
func TestPendingPairingsUI_OffersRolePickerOnApprove(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)
	rec := postPairRequest(t, mux, "Kiosk", commitOf("s"), "10.0.0.9:1")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil))
	body := rec.Body.String()
	id := created.Data.ID
	for _, want := range []string{
		`<select name="role" id="role-` + id + `"`,
		`<option value="additional" selected>`,
		`<option value="satellite">`,
		`hx-post="/api/sync/pair-requests/` + id + `/approve" hx-include="#pin-` + id + `, #role-` + id + `"`,
		`hx-post="/api/sync/pair-requests/` + id + `/deny" hx-include="#pin-` + id + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("pending card is missing %q:\n%s", want, body)
		}
	}
}

// --- changing the role later: POST /api/sync/tills/{id}/role ---

func postTillRole(mux *http.ServeMux, id, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+id+"/role",
		strings.NewReader(url.Values{"role": {role}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestTillRoleChange_OnPrimaryPersistsAuditsAndRefreshesRoster(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	id, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Till 2", hashBearer("tok-2"), data.TillRoleAdditional)
	if err != nil {
		t.Fatal(err)
	}

	rec := postTillRole(mux, id, "satellite")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("role change = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "tills-changed" {
		t.Fatalf("HX-Trigger = %q, want tills-changed", got)
	}
	if rec.Header().Get("HX-Refresh") != "" {
		t.Fatal("role change must not force a full reload (ut-docs#2904)")
	}
	if got := persistedRole(t, dp, id); got != data.TillRoleSatellite {
		t.Fatalf("role = %q, want satellite", got)
	}
	a := auditRowsFor(t, dp, "till_role_changed")
	if len(a) != 1 || a[0]["_entity_type"] != "till" || a[0]["_entity_id"] != id ||
		a[0]["from"] != "additional" || a[0]["to"] != "satellite" {
		t.Fatalf("audit = %+v, want one till_role_changed additional -> satellite", a)
	}

	// Saving the same role again is a no-op: no second audit row.
	if rec := postTillRole(mux, id, "satellite"); rec.Code != http.StatusNoContent {
		t.Fatalf("same-role save = %d", rec.Code)
	}
	if a := auditRowsFor(t, dp, "till_role_changed"); len(a) != 1 {
		t.Fatalf("a no-op save wrote an audit row: %+v", a)
	}

	for _, bad := range []string{"", "register", "Satellite"} {
		if rec := postTillRole(mux, id, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("role %q = %d, want 400", bad, rec.Code)
		}
	}
	if rec := postTillRole(mux, "no-such-till", "satellite"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown till = %d, want 404", rec.Code)
	}
	if got := persistedRole(t, dp, id); got != data.TillRoleSatellite {
		t.Fatalf("refused requests changed the role to %q", got)
	}
}

// Primary-authoritative, like revoke: refused on a replica, where the write
// would only be undone by the next admin pull.
func TestTillRoleChange_RejectedOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	id, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Till 3", hashBearer("tok-3"), data.TillRoleAdditional)
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postTillRole(mux, id, "satellite"); rec.Code != http.StatusConflict {
		t.Fatalf("role change on a replica = %d, want 409", rec.Code)
	}
	if got := persistedRole(t, dp, id); got != data.TillRoleAdditional {
		t.Fatalf("role = %q after a refused change", got)
	}
	if a := auditRowsFor(t, dp, "till_role_changed"); len(a) != 0 {
		t.Fatalf("a refused change was audited: %+v", a)
	}
}

// The Tills page: the pairing-code card offers the role picker, each
// enrolled till gets a role select + save on the main till, and a joined
// till's read-only roster shows the role instead.
func TestTillsPage_RolePickers(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	id, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Kiosk", hashBearer("tok-k"), data.TillRoleSatellite)
	if err != nil {
		t.Fatal(err)
	}
	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tills", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /tills = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	for _, want := range []string{
		`<select id="enrol-role" name="role">`,
		`hx-post="/api/sync/enroll-token" hx-include="#enrol-role"`,
		`hx-post="/api/sync/tills/` + id + `/role"`,
		`<option value="satellite" selected>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("main till's Tills page is missing %q", want)
		}
	}

	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}
	body = get()
	if strings.Contains(body, "/role\"") || strings.Contains(body, `id="enrol-role"`) {
		t.Fatal("a joined till must not offer role changes or pairing")
	}
	if !strings.Contains(body, httpx.T(httpx.DefaultLocale(), "tills.role.satellite")) {
		t.Fatal("a joined till's roster should still show each till's role")
	}
}

// --- the joined till's side: hello, pull reconcile, device-profile gate ---

func TestLinkHelloRole_ReportsTheTillsOwnRole(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if got := linkHelloRole(ctx, d); got != "replica" {
		t.Fatalf("no role = %q, want replica", got)
	}
	if err := d.Settings.Set(ctx, appdb.TillRoleSettingsKey, data.TillRoleAdditional); err != nil {
		t.Fatal(err)
	}
	if got := linkHelloRole(ctx, d); got != "replica" {
		t.Fatalf("additional = %q, want replica", got)
	}
	if err := d.Settings.Set(ctx, appdb.TillRoleSettingsKey, data.TillRoleSatellite); err != nil {
		t.Fatal(err)
	}
	if got := linkHelloRole(ctx, d); got != "satellite" {
		t.Fatalf("satellite = %q, want satellite", got)
	}
}

// End to end over a real link: a satellite's hello reaches the main till's
// hub as Role "satellite" (resolving the hub's "a future satellite client
// must set its hello Role" note), and a role change made on the main till's
// Tills page reaches the joined till at its next pull — kicked at once by
// the link nudge.
func TestReplicaLink_SatelliteHelloAndRoleChangeArrive(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // the Tills-page role change is a manager's
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	f := newSyncLinkFixture(t)
	satID, err := data.NewTillsRepo(f.dp.Db).InsertTill(t.Context(), "Kiosk", hashBearer("token-abc"), data.TillRoleSatellite)
	if err != nil {
		t.Fatal(err)
	}
	replica := linkReplica(t, f.srv.URL, satID, fastLinkClientOptions())
	// As db.ApplyReplicaIdentity leaves a till that joined as a satellite.
	for k, v := range map[string]string{appdb.TillRoleSettingsKey: "satellite", appdb.TillRoleMainSettingsKey: "satellite"} {
		if err := replica.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, replica.LinkClient.Linked) {
		t.Fatal("replica never linked")
	}
	h, ok := waitPeerHello(t, f.dp.Link, satID, 3*time.Second)
	if !ok || h.Role != "satellite" {
		t.Fatalf("satellite hello at the main till = %+v (ok=%v), want Role satellite", h, ok)
	}

	// The manager makes it an additional till on the main till.
	if rec := postTillRole(f.mux, satID, "additional"); rec.Code != http.StatusNoContent {
		t.Fatalf("role change = %d: %s", rec.Code, rec.Body.String())
	}
	if !waitFor(t, 3*time.Second, func() bool {
		return settingOf(t, replica, appdb.TillRoleSettingsKey) == data.TillRoleAdditional
	}) {
		t.Fatalf("the joined till never learnt its new role (sync.till_role = %q)", settingOf(t, replica, appdb.TillRoleSettingsKey))
	}
}

// joinedTill seeds d as a joined till with roster row role mainRole and
// local settings tillRole/seenRole/mode.
func joinedTill(t *testing.T, d *common.Deps, mainRole, tillRole, seenRole, mode string) string {
	t.Helper()
	ctx := t.Context()
	id, err := data.NewTillsRepo(d.Db).InsertTill(ctx, "Kiosk", "hash-kiosk", mainRole)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"sync.primary_url":            "http://192.168.1.10:8080",
		"sync.till_id":                id,
		appdb.TillRoleSettingsKey:     tillRole,
		appdb.TillRoleMainSettingsKey: seenRole,
		"display.mode":                mode,
	} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// After a pull, a till whose row on the main till became satellite adopts
// it, and — being left on the register profile — is forced to the
// self-order kiosk with an audit row rather than left inconsistent.
func TestReconcileOwnTillRole_LearnsSatelliteAndForcesKiosk(t *testing.T) {
	httpx.InitSelfOrderMode(false)
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	_, _, d := newFullAuthDeps(t)
	id := joinedTill(t, d, data.TillRoleSatellite, data.TillRoleAdditional, data.TillRoleAdditional, "")

	reconcileOwnTillRole(t.Context(), d)

	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != data.TillRoleSatellite {
		t.Fatalf("sync.till_role = %q, want satellite", got)
	}
	if got := settingValue(t, d, appdb.TillRoleMainSettingsKey); got != data.TillRoleSatellite {
		t.Fatalf("sync.till_role_main = %q, want satellite", got)
	}
	if got := settingValue(t, d, "display.mode"); got != "self_order" {
		t.Fatalf("display.mode = %q, want self_order", got)
	}
	changed := auditRowsFor(t, d, "till_role_changed")
	if len(changed) != 1 || changed[0]["_entity_id"] != id || changed[0]["to"] != "satellite" || changed[0]["source"] != "main_till" {
		t.Fatalf("till_role_changed audit = %+v", changed)
	}
	forced := auditRowsFor(t, d, "display_mode_forced")
	if len(forced) != 1 || forced[0]["from"] != "register" || forced[0]["mode"] != "self_order" || forced[0]["_actor"] != "system" {
		t.Fatalf("display_mode_forced audit = %+v, want one register -> self_order by system", forced)
	}

	// Idempotent: a second pull changes and audits nothing.
	reconcileOwnTillRole(t.Context(), d)
	if n := len(auditRowsFor(t, d, "display_mode_forced")) + len(auditRowsFor(t, d, "till_role_changed")); n != 2 {
		t.Fatalf("a second reconcile wrote audit rows (total %d, want 2)", n)
	}
}

// A back-office satellite is corrected the same way.
func TestReconcileOwnTillRole_ForcesBackofficeSatelliteToKiosk(t *testing.T) {
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	_, _, d := newFullAuthDeps(t)
	joinedTill(t, d, data.TillRoleSatellite, data.TillRoleSatellite, data.TillRoleSatellite, "backoffice")
	reconcileOwnTillRole(t.Context(), d)
	if got := settingValue(t, d, "display.mode"); got != "self_order" {
		t.Fatalf("display.mode = %q, want self_order", got)
	}
	if forced := auditRowsFor(t, d, "display_mode_forced"); len(forced) != 1 || forced[0]["from"] != "backoffice" {
		t.Fatalf("display_mode_forced audit = %+v", forced)
	}
}

// The main till sets a satellite back to additional: the till follows, and
// its kiosk profile is left alone (an additional till may run any profile).
// A till that made ITSELF a satellite (Settings, confirmed) keeps that until
// the main till's role for it next changes.
func TestReconcileOwnTillRole_FollowsChangesOnly(t *testing.T) {
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	id := joinedTill(t, d, data.TillRoleAdditional, data.TillRoleSatellite, data.TillRoleAdditional, "self_order")

	reconcileOwnTillRole(ctx, d)
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != data.TillRoleSatellite {
		t.Fatalf("a locally confirmed satellite was reverted to %q with no change on the main till", got)
	}

	// The manager makes it a satellite on the main till, then additional.
	repo := data.NewTillsRepo(d.Db)
	if _, err := repo.UpdateRole(ctx, id, data.TillRoleSatellite); err != nil {
		t.Fatal(err)
	}
	reconcileOwnTillRole(ctx, d)
	if _, err := repo.UpdateRole(ctx, id, data.TillRoleAdditional); err != nil {
		t.Fatal(err)
	}
	reconcileOwnTillRole(ctx, d)
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != data.TillRoleAdditional {
		t.Fatalf("sync.till_role = %q after the main till set additional", got)
	}
	if got := settingValue(t, d, "display.mode"); got != "self_order" {
		t.Fatalf("display.mode = %q; becoming additional must not change the profile", got)
	}
}

// A main/standalone till has no roster row of its own: nothing happens.
func TestReconcileOwnTillRole_NoOpOnAMainTill(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	reconcileOwnTillRole(t.Context(), d)
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != "" {
		t.Fatalf("a main till got sync.till_role = %q", got)
	}
}

// --- Settings → device profile ---

func TestDisplayMode_SatelliteRefusesRegisterAndBackoffice(t *testing.T) {
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	mux, _, d := newFullAuthDeps(t)
	joinedTill(t, d, data.TillRoleSatellite, data.TillRoleSatellite, data.TillRoleSatellite, "self_order")

	for _, mode := range []string{"register", "backoffice"} {
		rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {mode}}, &mgrUser)
		if rec.Code != http.StatusConflict {
			t.Fatalf("display-mode %s on a satellite = %d, want 409", mode, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), httpx.T(httpx.DefaultLocale(), "tills.role.error.satellite_mode")) {
			t.Fatalf("refusal is not the localized message: %q", rec.Body.String())
		}
	}
	// The generic key/value door is gated the same way ("" is register).
	for _, v := range []string{"", "backoffice"} {
		if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"display.mode"}, "value": {v}}, &mgrUser); rec.Code != http.StatusConflict {
			t.Fatalf("upsert display.mode=%q on a satellite = %d, want 409", v, rec.Code)
		}
	}
	if got := settingValue(t, d, "display.mode"); got != "self_order" {
		t.Fatalf("display.mode = %q after refused switches", got)
	}
	if err := applyDisplayMode(t.Context(), d, "register"); err != errSatelliteDisplayMode {
		t.Fatalf("applyDisplayMode(register) on a satellite = %v, want errSatelliteDisplayMode", err)
	}
	if a := auditRowsFor(t, d, "display_mode_changed"); len(a) != 0 {
		t.Fatalf("refused switches were audited: %+v", a)
	}
	// The kiosk itself stays allowed.
	if rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"self_order"}}, &mgrUser); rec.Code >= 400 {
		t.Fatalf("self_order on a satellite = %d", rec.Code)
	}
	// Its role is not editable through the raw settings table.
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {appdb.TillRoleSettingsKey}, "value": {"additional"}}, &mgrUser); rec.Code != http.StatusForbidden {
		t.Fatalf("upsert sync.till_role = %d, want 403", rec.Code)
	}
}

func settingsPageHTML(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := postFormGET(mux, "/settings")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	return rec.Body.String()
}

func postFormGET(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSettingsPage_DeviceProfileFollowsTheTillsRole(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()

	// A main till: every profile, no satellite prompt.
	body := settingsPageHTML(t, mux)
	if !strings.Contains(body, `<option value="register"`) || strings.Contains(body, `name="become_satellite"`) ||
		strings.Contains(body, `data-confirm-value="self_order"`) {
		t.Fatal("a main till's device-profile form should offer every profile and no satellite prompt")
	}

	// An additional joined till: every profile, and "Make this a
	// satellite?" asked for the kiosk only.
	joinedTill(t, d, data.TillRoleAdditional, data.TillRoleAdditional, data.TillRoleAdditional, "")
	body = settingsPageHTML(t, mux)
	confirm := httpx.T(httpx.DefaultLocale(), "tills.role.confirm_satellite")
	for _, want := range []string{
		`<option value="register"`, `<option value="backoffice"`,
		`data-confirm-field="mode" data-confirm-value="self_order"`,
		`<input type="hidden" name="become_satellite" value="1">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("additional joined till's settings page is missing %q", want)
		}
	}
	if !strings.Contains(body, `hx-confirm="`+html.EscapeString(confirm)+`"`) {
		t.Fatal("additional joined till's device-profile form has no satellite confirm")
	}

	// A satellite: the kiosk only.
	if err := d.Settings.Set(ctx, appdb.TillRoleSettingsKey, data.TillRoleSatellite); err != nil {
		t.Fatal(err)
	}
	body = settingsPageHTML(t, mux)
	if strings.Contains(body, `<option value="register"`) || strings.Contains(body, `<option value="backoffice"`) {
		t.Fatal("a satellite must not be offered register or back office")
	}
	if !strings.Contains(body, `<option value="self_order" selected>`) || !strings.Contains(body, `data-testid="satellite-mode-note"`) {
		t.Fatal("a satellite's device profile should show the kiosk, selected, with the note")
	}
}

// Confirming "Make this a satellite?" on an additional joined till sets its
// own role, audited; picking the kiosk without the confirmation does not.
func TestDisplayMode_ConfirmedSelfOrderMakesThisTillASatellite(t *testing.T) {
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	mux, _, d := newFullAuthDeps(t)
	id := joinedTill(t, d, data.TillRoleAdditional, data.TillRoleAdditional, data.TillRoleAdditional, "")

	if rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"self_order"}}, &mgrUser); rec.Code >= 400 {
		t.Fatalf("self_order = %d", rec.Code)
	}
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != data.TillRoleAdditional {
		t.Fatalf("an unconfirmed kiosk switch changed the role to %q", got)
	}
	if rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"register"}}, &mgrUser); rec.Code >= 400 {
		t.Fatalf("back to register = %d", rec.Code)
	}

	rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"self_order"}, "become_satellite": {"1"}}, &mgrUser)
	if rec.Code >= 400 {
		t.Fatalf("confirmed self_order = %d %s", rec.Code, rec.Body.String())
	}
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != data.TillRoleSatellite {
		t.Fatalf("sync.till_role = %q, want satellite", got)
	}
	a := auditRowsFor(t, d, "till_role_changed")
	if len(a) != 1 || a[0]["_entity_id"] != id || a[0]["to"] != "satellite" || a[0]["source"] != "this_till" {
		t.Fatalf("till_role_changed audit = %+v", a)
	}
	// Now gated like any satellite.
	if rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"register"}}, &mgrUser); rec.Code != http.StatusConflict {
		t.Fatalf("register after becoming a satellite = %d, want 409", rec.Code)
	}
	// And the next pull does not undo it: the main till's role for this
	// till has not changed since the till last saw it.
	reconcileOwnTillRole(t.Context(), d)
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != data.TillRoleSatellite {
		t.Fatalf("the next pull reverted the confirmed satellite to %q", got)
	}
}

// become_satellite means nothing on a main till: it has no role to change.
func TestDisplayMode_BecomeSatelliteIgnoredOnAMainTill(t *testing.T) {
	t.Cleanup(func() { httpx.InitSelfOrderMode(false); httpx.InitDisplayMode("") })
	mux, _, d := newFullAuthDeps(t)
	if rec := postForm(mux, "/api/settings/display-mode", url.Values{"mode": {"self_order"}, "become_satellite": {"1"}}, &mgrUser); rec.Code >= 400 {
		t.Fatalf("self_order = %d", rec.Code)
	}
	if got := settingValue(t, d, appdb.TillRoleSettingsKey); got != "" {
		t.Fatalf("a main till got sync.till_role = %q", got)
	}
}
