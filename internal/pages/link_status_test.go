package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2742 (ADR-0114 §10): one truthful connectivity indicator on a
// replica — linked / polling / main till not reachable since … — driven by
// the link's presence and the shared PrimaryWatch, and the main till's
// Tills page showing each peer live.

func TestDeriveLinkView(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	rfc := func(t time.Time) string { return t.Format(time.RFC3339) }
	linked := fleetlink.ClientStatus{Mode: fleetlink.ModeLinked, Linked: true, MainVersion: "1.4.0", LinkedSince: t0}
	for _, c := range []struct {
		name      string
		in        linkInputs
		state     linkState
		since     string
		update    string
		wantEmpty bool
	}{
		{name: "not a replica", in: linkInputs{}, wantEmpty: true},
		{name: "replica without a link client polls", in: linkInputs{Replica: true, ThisVersion: "1.4.0"}, state: linkPolling},
		{name: "older main till: polling", in: linkInputs{Replica: true, HasClient: true,
			Client: fleetlink.ClientStatus{Mode: fleetlink.ModePolling}}, state: linkPolling},
		{name: "still connecting: polling", in: linkInputs{Replica: true, HasClient: true,
			Client: fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting}}, state: linkPolling},
		{name: "linked, same version", in: linkInputs{Replica: true, HasClient: true, Client: linked, ThisVersion: "1.4.0"},
			state: linkLinked},
		{name: "linked, main till newer: this till's update waits", in: linkInputs{Replica: true, HasClient: true,
			Client: linked, ThisVersion: "1.3.9"}, state: linkLinked, update: linkUpdateWaiting},
		{name: "linked, this till newer: waiting for the main till", in: linkInputs{Replica: true, HasClient: true,
			Client: linked, ThisVersion: "v1.5.0"}, state: linkLinked, update: linkMainUpdateWaiting},
		{name: "dev build compares nothing", in: linkInputs{Replica: true, HasClient: true, Client: linked, ThisVersion: "dev"},
			state: linkLinked},
		{name: "link lost, nothing heard since: unreachable at once", in: linkInputs{Replica: true, HasClient: true,
			Client:      fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, LostAt: t0.Add(30 * time.Second)},
			LastContact: rfc(t0)}, state: linkUnreachable, since: rfc(t0.Add(30 * time.Second))},
		{name: "a pull that finished between the last frame and noticing the loss proves nothing", in: linkInputs{
			Replica: true, HasClient: true,
			Client:      fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, LostAt: t0, LostSeen: t0.Add(12 * time.Second)},
			LastContact: rfc(t0.Add(time.Second))}, state: linkUnreachable, since: rfc(t0)},
		{name: "link lost but a pull reached the main till since: polling", in: linkInputs{Replica: true, HasClient: true,
			Client:      fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, LostAt: t0},
			LastContact: rfc(t0.Add(2 * time.Second))}, state: linkPolling},
		{name: "watch says unreachable: since the later of last contact and last frame", in: linkInputs{Replica: true,
			HasClient: true, Client: fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, LostAt: t0.Add(5 * time.Second)},
			WatchUnreachable: true, WatchSince: rfc(t0), LastContact: rfc(t0)},
			state: linkUnreachable, since: rfc(t0.Add(5 * time.Second))},
		{name: "watch says unreachable, no link ever", in: linkInputs{Replica: true, WatchUnreachable: true, WatchSince: rfc(t0)},
			state: linkUnreachable, since: rfc(t0)},
		{name: "watch unreachable wins over a stale linked flag", in: linkInputs{Replica: true, HasClient: true,
			Client: fleetlink.ClientStatus{Mode: fleetlink.ModeLinked}, WatchUnreachable: true, WatchSince: rfc(t0)},
			state: linkUnreachable, since: rfc(t0)},
		// Independent review (Fable 5.1): a replica restarted while its main
		// till is down has no in-process loss to go on (LostAt is zero) and
		// the pull loop's first tick is 30 s away — the same "already
		// unreachable at launch" rule PrimaryWatch applies (one failure +
		// a last contact older than its window) applies to a failed dial.
		{name: "restart while the main till is down: a failed dial and a stale last contact read unreachable at once",
			in: linkInputs{Replica: true, HasClient: true,
				Client:      fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, FailedAt: t0.Add(100 * time.Second)},
				LastContact: rfc(t0), Now: t0.Add(100 * time.Second)},
			state: linkUnreachable, since: rfc(t0)},
		{name: "a failed dial with a recent last contact: polling (the main till may just be restarting)",
			in: linkInputs{Replica: true, HasClient: true,
				Client:      fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, FailedAt: t0.Add(10 * time.Second)},
				LastContact: rfc(t0), Now: t0.Add(10 * time.Second)},
			state: linkPolling},
		{name: "a failed dial and no contact ever: unreachable, no since",
			in: linkInputs{Replica: true, HasClient: true,
				Client: fleetlink.ClientStatus{Mode: fleetlink.ModeConnecting, FailedAt: t0}, Now: t0},
			state: linkUnreachable},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := deriveLinkView(c.in)
			if c.wantEmpty {
				if v.State != linkNone {
					t.Fatalf("view = %+v, want no chip", v)
				}
				return
			}
			if v.State != c.state || v.Since != c.since || v.Update != c.update {
				t.Fatalf("view = %+v, want state %q since %q update %q", v, c.state, c.since, c.update)
			}
		})
	}
}

func renderChip(t *testing.T, v linkView) string {
	t.Helper()
	chdirRoot(t)
	rec := httptest.NewRecorder()
	renderLinkChip(v)(rec, httptest.NewRequest(http.MethodGet, "/ui/main-till-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

// Each state renders its own words, colour hook and data-link-state; none
// relies on colour alone, and each is a real link to /tills (no hover on a
// touchscreen).
func TestLinkChip_RendersEachState(t *testing.T) {
	since := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name  string
		v     linkView
		want  []string
		avoid []string
	}{
		{"linked", linkView{State: linkLinked},
			[]string{`data-link-state="linked"`, "sb-link is-linked", "Main till linked", `href="/tills"`},
			[]string{"not reachable", "Update waiting"}},
		{"polling", linkView{State: linkPolling},
			[]string{`data-link-state="polling"`, "sb-link is-polling", "Main till: polling", `href="/tills"`},
			[]string{"not reachable", "linked"}},
		{"unreachable since", linkView{State: linkUnreachable, Since: since},
			[]string{`data-link-state="unreachable"`, "sb-link is-unreachable", "sb-main-till", "Main till not reachable since", "selling offline"},
			[]string{"Main till linked"}},
		{"unreachable, never reached", linkView{State: linkUnreachable},
			[]string{`data-link-state="unreachable"`, "Main till not reachable — selling offline"}, nil},
		{"unreachable with plugin updates waiting", linkView{State: linkUnreachable, PluginsWaiting: true},
			[]string{"plugin updates wait for it"}, nil},
		{"update waiting", linkView{State: linkLinked, Update: linkUpdateWaiting},
			[]string{`data-link-state="linked"`, "Update waiting"}, []string{"Waiting for the main till"}},
		{"waiting for the main till to update", linkView{State: linkLinked, Update: linkMainUpdateWaiting},
			[]string{"Waiting for the main till to update"}, []string{"Update waiting"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := renderChip(t, c.v)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %s", w, got)
				}
			}
			for _, a := range c.avoid {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in %s", a, got)
				}
			}
		})
	}
	if got := renderChip(t, linkView{}); strings.TrimSpace(got) != "" {
		t.Fatalf("not a replica rendered %q, want nothing", got)
	}
}

func getLinkChip(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/main-till-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("chip status %d", rec.Code)
	}
	return rec.Body.String()
}

// End to end against the real link: the chip reads "linked" while the main
// till's frames arrive and turns to "not reachable" within one presence
// window of them stopping — not after the pull loop's three failed ticks.
// The outage logs exactly one WARN however many ticks fail (#2719).
func TestLinkChip_FollowsTheRealLinkAndWarnsOncePerOutage(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "token-abc")
	proxy := newTCPProxy(t, f.srv.Listener.Addr().String())
	opts := fastLinkClientOptions()
	opts.PeerTimeout = 400 * time.Millisecond
	opts.BackoffMin, opts.BackoffMax = time.Minute, time.Minute // stay down once lost
	replica := linkReplica(t, proxy.url(), tillID, opts)
	replica.PrimaryWatch = discovery.NewPrimaryWatch(replica.Settings, func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		return nil, nil // the main till is nowhere on the network
	})
	mux := http.NewServeMux()
	registerMainTillStatus(mux, replica)
	pulls := runReplica(t, replica, time.Hour, time.Hour)

	if !waitFor(t, 3*time.Second, func() bool { return replica.LinkClient.Linked() && pulls.Load() >= 1 }) {
		t.Fatal("never linked")
	}
	if chip := getLinkChip(t, mux); !strings.Contains(chip, `data-link-state="linked"`) {
		t.Fatalf("linked replica chip = %s", chip)
	}

	logging.ResetRecent()
	proxy.frozen.Store(true)
	start := time.Now()
	if !waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(getLinkChip(t, mux), `data-link-state="unreachable"`)
	}) {
		t.Fatalf("chip never turned after the main till went silent: %s (inputs %+v)", getLinkChip(t, mux), linkInputsOf(t.Context(), replica))
	}
	if took := time.Since(start); took > opts.PeerTimeout+time.Second {
		t.Fatalf("chip turned after %v, want about one presence window (%v)", took, opts.PeerTimeout)
	}
	if chip := getLinkChip(t, mux); !strings.Contains(chip, "Main till not reachable since") {
		t.Fatalf("unreachable chip has no time: %s", chip)
	}

	// The pull loop keeps failing tick after tick: still one WARN.
	client := &http.Client{Timeout: 100 * time.Millisecond}
	for range 5 {
		syncPullTick(t.Context(), replica, client, func(context.Context) {})
	}
	warns := 0
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "main till unreachable") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("%d WARNs for one outage, want exactly 1: %+v", warns, logging.Recent())
	}
}

func TestLinkChip_EmptyOnAMainTill(t *testing.T) {
	f := newSyncLinkFixture(t)
	mux := http.NewServeMux()
	registerMainTillStatus(mux, f.dp)
	if chip := getLinkChip(t, mux); strings.TrimSpace(chip) != "" {
		t.Fatalf("a main till rendered a link chip: %q", chip)
	}
}

func getRoster(t *testing.T, mux *http.ServeMux, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/tills/roster", nil), auth.User{ID: "u1", Role: role})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The main till's Tills page: each enrolled till live — link up/down, role,
// version against the main till's, update state, queue, TLS pin.
func TestTillsRoster_ShowsEachPeerLive(t *testing.T) {
	f := newSyncLinkFixture(t)
	registerTillsRoster(f.mux, f.dp)
	linkedID := f.enrol(t, "Counter", "token-abc")
	f.enrol(t, "Back room", "token-def")

	replica := linkReplica(t, f.srv.URL, linkedID, fastLinkClientOptions())
	runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, func() bool {
		r, ok := f.dp.Link.Report(linkedID)
		return replica.LinkClient.Linked() && ok && r.UpdateState == "idle"
	}) {
		t.Fatal("replica never linked and reported")
	}

	rec := getRoster(t, f.mux, "manager")
	if rec.Code != http.StatusOK {
		t.Fatalf("roster = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	counter := rowOf(t, body, "Counter")
	for _, w := range []string{`data-link="up"`, "Linked", "Additional till", "TLS not pinned"} {
		if !strings.Contains(counter, w) {
			t.Errorf("Counter row missing %q: %s", w, counter)
		}
	}
	back := rowOf(t, body, "Back room")
	if !strings.Contains(back, `data-link="down"`) || !strings.Contains(back, "Not linked") {
		t.Errorf("Back room row should read not linked: %s", back)
	}

	if rec := getRoster(t, f.mux, "cashier"); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "Counter") {
		t.Fatal("a cashier read the till roster")
	}
}

// Independent review (Fable 5.1): the main till's Tills PAGE polls the
// roster — but not while the operator's focus is inside it (a keyboard user
// who tabbed onto Revoke would lose it at the next swap, ut-docs#826's
// lesson) — and ENROLLED / LAST SEEN share one locale-aware format:
// tills.enrolled_at defaults to SQLite's own "YYYY-MM-DD HH:MM:SS" layout,
// which used to render raw next to a localised last_seen_at.
func TestTillsPage_PollsRosterUnlessFocusedAndFormatsBothDates(t *testing.T) {
	f := newSyncLinkFixture(t)
	f.enrol(t, "Counter", "token-abc")
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/tills", nil), auth.User{ID: "u1", Role: "manager"})
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tills = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, w := range []string{`hx-get="/ui/tills/roster"`, `every 10s [`, `activeElement`} {
		if !strings.Contains(body, w) {
			t.Errorf("Tills page missing %q in its roster poll", w)
		}
	}
	if m := regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`).FindString(rowOf(t, body, "Counter")); m != "" {
		t.Errorf("enrolled_at rendered raw (%q), want the till's locale-aware datetime like LAST SEEN", m)
	}
}

// rowOf returns the <tr> holding name.
func rowOf(t *testing.T, body, name string) string {
	t.Helper()
	i := strings.Index(body, name)
	if i < 0 {
		t.Fatalf("no row for %q in %s", name, body)
	}
	start := strings.LastIndex(body[:i], "<tr")
	end := strings.Index(body[i:], "</tr>")
	if start < 0 || end < 0 {
		t.Fatalf("no <tr> around %q", name)
	}
	return body[start : i+end]
}

func TestTillPeerView_VersionAndUpdateState(t *testing.T) {
	now := time.Now()
	fresh := fleetlink.PeerInfo{TillID: "a", HasHello: true, Hello: fleetlink.Hello{Role: "replica", Version: "1.2.0"},
		LastFrame: now, HasReport: true, Report: fleetlink.Report{Version: "1.2.0", UpdateState: "failed:checksum", PushQueueDepth: 2}}
	v := tillPeerViewOf(fresh, "1.3.0", now)
	if !v.Linked || v.Role != "replica" || v.VersionCmp != "older" || v.UpdateState != "failed" || v.UpdateCode != "checksum" || v.QueueDepth != 2 {
		t.Fatalf("view = %+v", v)
	}
	stale := fresh
	stale.LastFrame = now.Add(-13 * time.Second)
	if tillPeerViewOf(stale, "1.3.0", now).Linked {
		t.Fatal("a peer silent for 13 s is not linked (ADR-0114 §4)")
	}
	if got := tillPeerViewOf(fresh, "1.2.0", now).VersionCmp; got != "same" {
		t.Fatalf("same version compared as %q", got)
	}
	if got := tillPeerViewOf(fresh, "dev", now).VersionCmp; got != "" {
		t.Fatalf("a dev build compared as %q", got)
	}
	weird := fresh
	weird.Report.UpdateState = "<script>"
	if got := tillPeerViewOf(weird, "1.2.0", now).UpdateState; got != "" {
		t.Fatalf("an unknown update state from a peer passed through as %q", got)
	}
}
