package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudlink"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

type cloudLinkSettings map[string]string

func (m cloudLinkSettings) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}

// ADR-0117 §1/§2 (ut-docs#2824 AC1): the till dials only as the main till
// on the realtime tier ("always"; on_demand is not built), enrolled — and a
// replica with a cached realtime tier never dials.
func TestCloudLinkTargetGate(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	realtime := func(extra map[string]string) cloudLinkSettings {
		s := cloudLinkSettings{
			entitlement.KeyCloudLinkTier:   "realtime",
			entitlement.KeyCloudLinkMode:   "always",
			entitlement.KeyLastConfirmedAt: fresh,
		}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	cfg := func(token string) *config.Config {
		c := &config.Config{}
		c.Marketplace.EndpointURL = "https://cloud.example.test/api"
		c.Marketplace.StoreID = "store-1"
		c.Marketplace.MerchantToken = token
		return c
	}
	cases := []struct {
		name string
		cfg  *config.Config
		s    cloudLinkSettings
		want bool
	}{
		{"main till, realtime always", cfg("cred-1"), realtime(nil), true},
		// cloudsync reports role "backoffice" whenever display.mode is
		// backoffice — even on a replica, and the cloud counts "backoffice"
		// as a main-till role — so the gate must key on sync.primary_url,
		// never on the display mode: a back-office replica never dials,
		// a back-office main till does.
		{"backoffice display on the main till", cfg("cred-1"), realtime(map[string]string{"display.mode": "backoffice"}), true},
		{"backoffice display on a replica", cfg("cred-1"), realtime(map[string]string{"display.mode": "backoffice", "sync.primary_url": "http://192.168.1.5:8080"}), false},
		{"replica with a cached realtime tier", cfg("cred-1"), realtime(map[string]string{"sync.primary_url": "http://192.168.1.5:8080"}), false},
		{"on_demand is not built", cfg("cred-1"), realtime(map[string]string{entitlement.KeyCloudLinkMode: "on_demand"}), false},
		{"periodic", cfg("cred-1"), realtime(map[string]string{entitlement.KeyCloudLinkTier: "periodic"}), false},
		{"stale cache", cfg("cred-1"), realtime(map[string]string{entitlement.KeyLastConfirmedAt: now.Add(-entitlement.Grace - time.Hour).Format(time.RFC3339)}), false},
		{"not enrolled", cfg(""), realtime(nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg, ok := cloudLinkTarget(context.Background(), tc.cfg, tc.s, now)
			if ok != tc.want {
				t.Fatalf("dial = %v, want %v", ok, tc.want)
			}
			if ok && (tg.BaseURL != "https://cloud.example.test/api" || tg.StoreID != "store-1" || tg.Bearer != "cred-1") {
				t.Fatalf("target = %+v", tg)
			}
		})
	}
}

// AC8: the sale frame is the §4 summary of the persisted sale, timed at
// the sale's own completion (its created_at, which InsertSale also writes
// as completed_at) — not whenever the frame happened to be built.
func TestCloudLinkSaleOf(t *testing.T) {
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	got := cloudLinkSaleOf(data.SaleDetail{
		ID: "sale-1", SaleType: "return", TenderType: "cash", Currency: "EUR", Total: -450,
		Lines: []data.SaleDetailLine{{}, {}}, CreatedAt: "2026-09-26T11:00:00+01:00",
	}, at.Add(time.Hour))
	want := cloudlink.Sale{ID: "sale-1", Time: at, TotalMinor: -450, Currency: "EUR", TenderKind: "cash", ItemCount: 2, Refund: true}
	if got != want {
		t.Fatalf("sale = %+v, want %+v", got, want)
	}
}

// The my. live panel (#2826) reads tender_kind as cash / card / voucher /
// split, anything else as Other; a refund is a negative total whatever sign
// the return row stores.
func TestCloudLinkSaleOfTenderAndRefundSign(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"cash", "cash"}, {" Card ", "card"}, {"voucher", "voucher"}, {"split", "split"}, {"qr_pay", "qr_pay"}, {"", "unknown"},
	} {
		if got := cloudLinkSaleOf(data.SaleDetail{TenderType: tc.in}, time.Time{}).TenderKind; got != tc.want {
			t.Fatalf("tender %q -> %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := cloudLinkSaleOf(data.SaleDetail{SaleType: "return", Total: 450}, time.Time{}).TotalMinor; got != -450 {
		t.Fatalf("return total = %d, want -450", got)
	}
	if got := cloudLinkSaleOf(data.SaleDetail{SaleType: "sale", Total: 450}, time.Time{}).TotalMinor; got != 450 {
		t.Fatalf("sale total = %d, want 450", got)
	}
}

// AC8: status carries this till's version and each linked LAN peer.
func TestCloudLinkStatusFromHub(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	d := &common.Deps{Db: db}
	st := cloudLinkStatusOf(context.Background(), d, "v9", []fleetlink.PeerInfo{
		{TillID: "t2", HasHello: true, Hello: fleetlink.Hello{Version: "v8"}},
		{TillID: "t3", HasHello: true, Hello: fleetlink.Hello{Version: "v8"}, HasReport: true, Report: fleetlink.Report{Version: "v9", UpdateState: "downloading"}},
	})
	if st.Version != "v9" || len(st.Peers) != 2 {
		t.Fatalf("status = %+v", st)
	}
	if p := st.Peers[0]; p.TillID != "t2" || p.Link != "up" || p.Version != "v8" {
		t.Fatalf("peer 0 = %+v", p)
	}
	if p := st.Peers[1]; p.Version != "v9" || p.UpdateState != "downloading" {
		t.Fatalf("peer 1 = %+v, want the report's version and update state", p)
	}
}

// ut-docs#2897: a live peer's cloud device id (carried on its fleetlink
// hello, ut-docs#2730's per-till identity) rides alongside till_id on the
// status frame, so my.'s Tills rows and Live panel (keyed by cloud device
// id) can name it instead of showing the raw LAN pairing id. An older
// replica's hello has no such field: its peer status carries an empty
// device id, till_id still present — never a stale or wrong value.
func TestCloudLinkStatusCarriesPeerCloudDeviceID(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	d := &common.Deps{Db: db}
	st := cloudLinkStatusOf(context.Background(), d, "v9", []fleetlink.PeerInfo{
		{TillID: "t2", HasHello: true, Hello: fleetlink.Hello{Version: "v8", CloudDeviceID: "till-cloud-2"}},
		{TillID: "t3", HasHello: true, Hello: fleetlink.Hello{Version: "v8"}}, // older replica: no cloud device id
	})
	if len(st.Peers) != 2 {
		t.Fatalf("status = %+v", st)
	}
	var t2, t3 *cloudlink.PeerStatus
	for i := range st.Peers {
		switch st.Peers[i].TillID {
		case "t2":
			t2 = &st.Peers[i]
		case "t3":
			t3 = &st.Peers[i]
		}
	}
	if t2 == nil || t2.DeviceID != "till-cloud-2" {
		t.Fatalf("t2 = %+v, want device id till-cloud-2", t2)
	}
	if t3 == nil || t3.DeviceID != "" {
		t.Fatalf("t3 = %+v, want an empty device id (older replica)", t3)
	}
}

// ut-docs#2897: a down till (from ListTills, ut-docs#2895) never carries a
// device id — the tills table has no such column, so leaving it empty is
// the honest answer rather than guessing.
func TestCloudLinkStatusDownTillsHaveNoDeviceID(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('t3','Register 3','h3')`); err != nil {
		t.Fatalf("seed tills: %v", err)
	}
	d := &common.Deps{Db: db}
	st := cloudLinkStatusOf(context.Background(), d, "v9", nil)
	if len(st.Peers) != 1 || st.Peers[0].TillID != "t3" || st.Peers[0].Link != "down" || st.Peers[0].DeviceID != "" {
		t.Fatalf("status = %+v, want t3 down with an empty device id", st.Peers)
	}
}

// ut-docs#2895: an enrolled till with no live link is listed as down —
// cheap enough (one indexed SELECT) to do on every status frame, same as
// the Tills page's own 10s roster poll already does.
func TestCloudLinkStatusListsDownTills(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('t2','Register 2','h2'), ('t3','Register 3','h3')`); err != nil {
		t.Fatalf("seed tills: %v", err)
	}
	d := &common.Deps{Db: db}
	st := cloudLinkStatusOf(context.Background(), d, "v9", []fleetlink.PeerInfo{
		{TillID: "t2", HasHello: true, Hello: fleetlink.Hello{Version: "v8"}},
	})
	if len(st.Peers) != 2 {
		t.Fatalf("peers = %+v, want t2 up and t3 down", st.Peers)
	}
	var t2, t3 *cloudlink.PeerStatus
	for i := range st.Peers {
		switch st.Peers[i].TillID {
		case "t2":
			t2 = &st.Peers[i]
		case "t3":
			t3 = &st.Peers[i]
		}
	}
	if t2 == nil || t2.Link != "up" {
		t.Fatalf("t2 = %+v, want up", t2)
	}
	if t3 == nil || t3.Link != "down" {
		t.Fatalf("t3 = %+v, want down", t3)
	}
}

// ut-docs#2895: the status frame's peer list is capped at 64 (ut-cloud's
// maxStatusPeers; ADR-0117 §7's 16 KiB message limit), live peers first —
// a store with 200 enrolled tills must not grow the frame without bound.
func TestCloudLinkStatusCapsPeers(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	for i := range 200 {
		if _, err := db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES (?, ?, ?)`,
			fmt.Sprintf("t%03d", i), fmt.Sprintf("Register %d", i), fmt.Sprintf("h%d", i)); err != nil {
			t.Fatalf("seed tills: %v", err)
		}
	}
	var peers []fleetlink.PeerInfo
	for i := 190; i < 200; i++ { // the live ones sort last in the table
		peers = append(peers, fleetlink.PeerInfo{TillID: fmt.Sprintf("t%03d", i), HasHello: true})
	}
	st := cloudLinkStatusOf(context.Background(), &common.Deps{Db: db}, "v9", peers)
	if len(st.Peers) != maxCloudLinkStatusPeers {
		t.Fatalf("peers = %d, want the cap %d", len(st.Peers), maxCloudLinkStatusPeers)
	}
	for i := range 10 {
		if st.Peers[i].Link != "up" {
			t.Fatalf("peer %d = %+v, want the live peers first", i, st.Peers[i])
		}
	}
}

// ut-docs#2895: the Tills page's Cloud link row maps the client's own
// State/Reason/NextAttempt, plus (only while idle) the gate's reason this
// till never tries at all, onto exactly one translated label+hint pair —
// pure and table-driven, same shape as link_status.go's deriveLinkView.
func TestCloudLinkRowViewOf(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name        string
		gateReason  cloudLinkGateReason
		state       cloudlink.State
		reason      cloudlink.WaitReason
		nextAttempt time.Time
		wantShow    bool
		wantCode    string
		wantLabel   string
		wantHint    string
		wantNext    bool // NextAttempt should be non-empty
	}{
		{"unenrolled hides the row even if somehow linked", gateUnenrolled, cloudlink.StateLinked, cloudlink.WaitReasonNone, time.Time{}, false, "", "", "", false},
		{"linked is live", gateEligible, cloudlink.StateLinked, cloudlink.WaitReasonNone, time.Time{}, true, "live", "tills.cloud_link.state_live", "tills.cloud_link.hint_live", false},
		{"connecting with a next attempt", gateEligible, cloudlink.StateConnecting, cloudlink.WaitReasonNone, at, true, "reconnecting", "tills.cloud_link.state_reconnecting", "tills.cloud_link.hint_reconnecting", true},
		{"connecting with no next attempt yet (dialling now)", gateEligible, cloudlink.StateConnecting, cloudlink.WaitReasonNone, time.Time{}, true, "reconnecting", "tills.cloud_link.state_reconnecting", "tills.cloud_link.hint_reconnecting_now", false},
		{"revoked is stopped", gateEligible, cloudlink.StateRevoked, cloudlink.WaitReasonNone, time.Time{}, true, "stopped", "tills.cloud_link.state_stopped", "tills.cloud_link.hint_stopped", false},
		{"waiting: not main till", gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonNotMainTill, time.Time{}, true, "paused_not_main", "tills.cloud_link.state_paused_not_main", "tills.cloud_link.hint_paused_not_main", false},
		{"waiting: tier changed", gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonTierChanged, time.Time{}, true, "paused_tier", "tills.cloud_link.state_paused_tier", "tills.cloud_link.hint_paused_tier", false},
		{"waiting: needs its own credential (ut-docs#2769)", gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonCredentialRequired, time.Time{}, true, "paused_credential", "tills.cloud_link.state_paused_credential", "tills.cloud_link.hint_paused_credential", false},
		{"waiting: busy", gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonBusy, time.Time{}, true, "paused_busy", "tills.cloud_link.state_paused_busy", "tills.cloud_link.hint_paused_busy", false},
		{"waiting: Retry-After with its retry time", gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonRetryAfter, at, true, "paused_busy", "tills.cloud_link.state_paused_busy", "tills.cloud_link.hint_paused_busy_retry", true},
		{"waiting: Retry-After, dialling now", gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonRetryAfter, time.Time{}, true, "paused_busy", "tills.cloud_link.state_paused_busy", "tills.cloud_link.hint_reconnecting_now", false},
		{"idle but eligible: connecting at boot", gateEligible, cloudlink.StateIdle, cloudlink.WaitReasonNone, time.Time{}, true, "connecting", "tills.cloud_link.state_connecting", "tills.cloud_link.hint_connecting", false},
		{"idle: this till is not the main till", gateNotMainTill, cloudlink.StateIdle, cloudlink.WaitReasonNone, time.Time{}, true, "periodic", "tills.cloud_link.state_periodic", "tills.cloud_link.hint_periodic_not_main", false},
		{"idle: the plan's tier is periodic", gateTierPeriodic, cloudlink.StateIdle, cloudlink.WaitReasonNone, time.Time{}, true, "periodic", "tills.cloud_link.state_periodic", "tills.cloud_link.hint_periodic_tier", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := cloudLinkRowViewOf(tc.gateReason, tc.state, tc.reason, tc.nextAttempt, "en")
			if v.Show != tc.wantShow || v.Code != tc.wantCode || v.LabelKey != tc.wantLabel || v.HintKey != tc.wantHint {
				t.Fatalf("view = %+v, want show=%v code=%q label=%q hint=%q", v, tc.wantShow, tc.wantCode, tc.wantLabel, tc.wantHint)
			}
			if got := v.NextAttempt != ""; got != tc.wantNext {
				t.Fatalf("NextAttempt = %q, want non-empty=%v", v.NextAttempt, tc.wantNext)
			}
		})
	}
}

// ut-docs#2895: the roster partial actually renders the Cloud link row's
// translated label and hint for every state (a handler-level check on top
// of TestCloudLinkRowViewOf's pure mapping) — real i18n, real template,
// same tills_roster.html the Tills page's 10s poll re-renders.
func TestTillsRosterRendersCloudLinkStates(t *testing.T) {
	chdirRoot(t)
	initAuthTestI18n(t)
	cases := []struct {
		name       string
		view       cloudLinkRowView
		wantHidden bool
		wantText   []string
	}{
		{"hidden when unenrolled", cloudLinkRowView{}, true, nil},
		{"live", cloudLinkRowViewOf(gateEligible, cloudlink.StateLinked, cloudlink.WaitReasonNone, time.Time{}, "en"), false,
			[]string{"Cloud link", "Live", "Connected in real time"}},
		{"reconnecting with a next attempt", cloudLinkRowViewOf(gateEligible, cloudlink.StateConnecting, cloudlink.WaitReasonNone, time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC), "en"), false,
			[]string{"Reconnecting", "Retrying"}},
		{"paused: not the main till", cloudLinkRowViewOf(gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonNotMainTill, time.Time{}, "en"), false,
			[]string{"Paused: not the main till"}},
		{"paused: tier changed", cloudLinkRowViewOf(gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonTierChanged, time.Time{}, "en"), false,
			[]string{"Paused: tier changed"}},
		{"paused: needs its own credential", cloudLinkRowViewOf(gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonCredentialRequired, time.Time{}, "en"), false,
			[]string{"Paused: needs its own credential", "own cloud credential", "contact support"}},
		{"paused: cloud busy", cloudLinkRowViewOf(gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonBusy, time.Time{}, "en"), false,
			[]string{"Paused: cloud busy"}},
		{"paused: Retry-After", cloudLinkRowViewOf(gateEligible, cloudlink.StateWaiting, cloudlink.WaitReasonRetryAfter, time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC), "en"), false,
			[]string{"Paused: cloud busy", "tries again by itself"}},
		{"connecting at boot", cloudLinkRowViewOf(gateEligible, cloudlink.StateIdle, cloudlink.WaitReasonNone, time.Time{}, "en"), false,
			[]string{"Connecting…"}},
		{"stopped: credential revoked", cloudLinkRowViewOf(gateEligible, cloudlink.StateRevoked, cloudlink.WaitReasonNone, time.Time{}, "en"), false,
			[]string{"Stopped: credential revoked"}},
		{"periodic: not the main till", cloudLinkRowViewOf(gateNotMainTill, cloudlink.StateIdle, cloudlink.WaitReasonNone, time.Time{}, "en"), false,
			[]string{"Periodic", "main till holds the live link"}},
		{"periodic: plan's tier", cloudLinkRowViewOf(gateTierPeriodic, cloudlink.StateIdle, cloudlink.WaitReasonNone, time.Time{}, "en"), false,
			[]string{"Periodic", "checks in every 2 minutes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/ui/tills/roster", nil)
			httpx.RenderPartial("ui/partials/tills_roster.html", map[string]any{"CloudLink": tc.view})(rec, req)
			body := rec.Body.String()
			if strings.Contains(body, `data-testid="cloud-link-row"`) == tc.wantHidden {
				t.Fatalf("cloud-link-row present = %v, want hidden=%v; body=%s", !tc.wantHidden, tc.wantHidden, body)
			}
			for _, want := range tc.wantText {
				if !strings.Contains(body, want) {
					t.Fatalf("body missing %q; body=%s", want, body)
				}
			}
		})
	}
}

// A sale row without a parseable completion time falls back to the time
// the frame is built.
func TestCloudLinkSaleOfTimeFallback(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, created := range []string{"", "not a time"} {
		if got := cloudLinkSaleOf(data.SaleDetail{CreatedAt: created}, now).Time; !got.Equal(now) {
			t.Fatalf("created_at %q -> time %v, want the fallback %v", created, got, now)
		}
	}
}

// ut-docs#2897 review: at the peer cap with every field at its 64-byte
// maximum, the encoded status frame must stay under ADR-0117 §7's 16 KiB
// message limit, or the cloud closes the link and the till redials in a loop.
func TestCloudLinkStatusWorstCaseFitsTheMessageLimit(t *testing.T) {
	long := strings.Repeat("x", 64)
	st := cloudlink.Status{Version: long, UpdateState: long}
	for range maxCloudLinkStatusPeers {
		st.Peers = append(st.Peers, cloudlink.PeerStatus{TillID: long, DeviceID: long, Link: "down", Version: long, UpdateState: long})
	}
	b, err := json.Marshal(map[string]any{"v": 1, "id": long, "type": "status", "payload": st})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 16*1024 {
		t.Fatalf("worst-case status frame = %d bytes, over the 16 KiB limit", len(b))
	}
}
