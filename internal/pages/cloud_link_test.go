package pages

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudlink"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/fleetlink"
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
	st := cloudLinkStatusOf("v9", []fleetlink.PeerInfo{
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
