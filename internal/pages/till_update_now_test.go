package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2945 (LAN half): POST /api/tills/{id}/update-now on the main
// till asks a linked till to install the main till's version now.

func postUpdateNow(t *testing.T, mux *http.ServeMux, id, userRole string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tills/"+id+"/update-now", nil)
	req = auth.WithUser(req, auth.User{ID: "u-" + userRole, Role: userRole})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func setBuildVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

func TestTillUpdateNow_RefusalsAreLocalizedAndNothingIsSent(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	initTillRoleI18n(t)
	setBuildVersion(t, "1.4.0")
	f := newSyncLinkFixture(t)
	registerTillUpdateNow(f.mux, f.dp)
	id := f.enrol(t, "Counter", "token-abc")

	if rec := postUpdateNow(t, f.mux, id, "cashier"); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: %d, want 403", rec.Code)
	}
	if rec := postUpdateNow(t, f.mux, "no-such-till", "manager"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown till: %d, want 404", rec.Code)
	}
	rec := postUpdateNow(t, f.mux, id, "manager")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not linked") ||
		rec.Header().Get("X-UT-Response") != "refused" {
		t.Fatalf("not linked: %d %q (X-UT-Response %q), want a localized 409 refusal", rec.Code, rec.Body, rec.Header().Get("X-UT-Response"))
	}

	setBuildVersion(t, "dev")
	rec = postUpdateNow(t, f.mux, id, "manager")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "development build") {
		t.Fatalf("dev build: %d %q, want a localized 409", rec.Code, rec.Body)
	}
	if got := auditRows(t, f.dp, "update_requested"); len(got) != 0 {
		t.Fatalf("refused requests were audited: %v", got)
	}
}

// Like role change and revoke: a joined till's roster is a synced copy.
func TestTillUpdateNow_RefusedOnAJoinedTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	setBuildVersion(t, "1.4.0")
	f := newSyncLinkFixture(t)
	registerTillUpdateNow(f.mux, f.dp)
	id := f.enrol(t, "Counter", "token-abc")
	if err := f.dp.Settings.Set(t.Context(), "sync.primary_url", "http://192.0.2.1:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postUpdateNow(t, f.mux, id, "manager"); rec.Code != http.StatusConflict {
		t.Fatalf("on a joined till: %d, want 409", rec.Code)
	}
}

// End to end over the real link: the press reaches the replica as an
// update to exactly the main till's version, and is audited.
func TestTillUpdateNow_ReachesTheLinkedReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	initTillRoleI18n(t)
	drainFollowKick(t)
	setBuildVersion(t, "1.4.0")
	f := newSyncLinkFixture(t)
	registerTillUpdateNow(f.mux, f.dp)
	id := f.enrol(t, "Counter", "token-abc")
	if _, err := f.dp.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('u-manager','mgr','Manager','x','manager')`); err != nil {
		t.Fatal(err)
	}

	replica := linkReplica(t, f.srv.URL, id, fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, func() bool {
		p := f.dp.Link.Peer(id)
		if p == nil || !replica.LinkClient.Linked() {
			return false
		}
		_, ok := p.Hello()
		return ok
	}) {
		t.Fatal("replica never linked")
	}

	rec := postUpdateNow(t, f.mux, id, "manager")
	if rec.Code != http.StatusOK {
		t.Fatalf("update-now: %d %s, want 200", rec.Code, rec.Body)
	}
	if !waitFor(t, 3*time.Second, func() bool { return followForced.get() == "1.4.0" }) {
		t.Fatalf("the replica never took the request (forced = %q)", followForced.get())
	}
	got := auditRows(t, f.dp, "update_requested")
	if len(got) != 1 || got[0] != `till|`+id+`|u-manager|{"target":"1.4.0"}` {
		t.Fatalf("audit = %v", got)
	}
}

// The roster offers Update now only for a linked till behind the main
// till that can act on it; "manual" reads as needing the installer.
func TestTillPeerView_CanUpdateNowAndManual(t *testing.T) {
	now := time.Now()
	behind := fleetlink.PeerInfo{TillID: "a", HasHello: true, Hello: fleetlink.Hello{Role: "replica", Version: "1.3.0"},
		LastFrame: now, HasReport: true, Report: fleetlink.Report{Version: "1.3.0", UpdateState: "idle"}}
	with := func(f func(*fleetlink.PeerInfo)) fleetlink.PeerInfo { p := behind; f(&p); return p }
	for _, c := range []struct {
		name  string
		p     fleetlink.PeerInfo
		main  string
		can   bool
		state string
	}{
		{"linked and behind", behind, "1.4.0", true, "idle"},
		{"failed: a press retries", with(func(p *fleetlink.PeerInfo) { p.Report.UpdateState = "failed:network" }), "1.4.0", true, "failed"},
		{"same version", behind, "1.3.0", false, "idle"},
		{"ahead of the main till", behind, "1.2.0", false, "idle"},
		{"not linked", with(func(p *fleetlink.PeerInfo) { p.LastFrame = now.Add(-time.Minute) }), "1.4.0", false, "idle"},
		{"already downloading", with(func(p *fleetlink.PeerInfo) { p.Report.UpdateState = "downloading" }), "1.4.0", false, "downloading"},
		{"already waiting for a safe moment", with(func(p *fleetlink.PeerInfo) { p.Report.UpdateState = "waiting-safe-moment" }), "1.4.0", false, "waiting-safe-moment"},
		{"needs the installer", with(func(p *fleetlink.PeerInfo) { p.Report.UpdateState = "failed:unsupported" }), "1.4.0", false, "manual"},
		{"an unknown state is dropped", with(func(p *fleetlink.PeerInfo) { p.Report.UpdateState = "manual" }), "1.4.0", true, ""},
		{"main till on a dev build", behind, "dev", false, "idle"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := tillPeerViewOf(c.p, c.main, now)
			if v.CanUpdateNow != c.can || v.UpdateState != c.state {
				t.Fatalf("view = %+v, want CanUpdateNow=%v UpdateState=%q", v, c.can, c.state)
			}
		})
	}
}

// The roster renders the button (confirm naming the till and the version)
// only where it can act, and the "needs the installer" note.
func TestTillsRoster_UpdateNowButtonAndManualNote(t *testing.T) {
	initTillRoleI18n(t)
	chdirRoot(t)
	row := func(id, name string, v tillPeerView) tillRosterRow {
		return tillRosterRow{TillRow: data.TillRow{ID: id, Name: name, Role: "additional"}, Link: &v}
	}
	m := map[string]any{
		"Tills": []tillRosterRow{
			row("t1", "Counter", tillPeerView{Linked: true, Version: "1.3.0", VersionCmp: "older", UpdateState: "idle", CanUpdateNow: true}),
			row("t2", "Back room", tillPeerView{Linked: true, Version: "1.3.0", VersionCmp: "older", UpdateState: "manual"}),
			row("t3", "Bar", tillPeerView{Linked: true, Version: "1.4.0", VersionCmp: "same", UpdateState: "idle"}),
		},
		"PrimaryTillName": "Main",
		"LinkInfo":        true,
		"MainVersion":     "1.4.0",
	}
	rec := httptest.NewRecorder()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/tills/roster", nil), auth.User{ID: "u1", Role: "manager"})
	httpx.RenderPartial("ui/partials/tills_roster.html", m)(rec, req)
	body := rec.Body.String()

	counter := rowOf(t, body, "Counter")
	for _, w := range []string{`hx-post="/api/tills/t1/update-now"`, "Update now", "Update Counter to 1.4.0 now?",
		`data-after-request="ok refresh-region:tills-roster; fail save-error:tills-update-now-error"`} {
		if !strings.Contains(counter, w) {
			t.Errorf("Counter row missing %q: %s", w, counter)
		}
	}
	back := rowOf(t, body, "Back room")
	if strings.Contains(back, "update-now") || !strings.Contains(back, "Needs the installer on that till") {
		t.Errorf("Back room row: want the installer note and no button: %s", back)
	}
	if bar := rowOf(t, body, "Bar"); strings.Contains(bar, "update-now") {
		t.Errorf("an up-to-date till offers Update now: %s", bar)
	}

	m["SyncPrimary"] = "http://192.0.2.1:8080" // a joined till's synced copy never offers it
	rec = httptest.NewRecorder()
	httpx.RenderPartial("ui/partials/tills_roster.html", m)(rec, req)
	if strings.Contains(rec.Body.String(), "update-now") {
		t.Error("a joined till's roster offers Update now")
	}
}
