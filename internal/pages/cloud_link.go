package pages

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/cloudlink"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cloud link, till side (ADR-0117 §1/§4/§7/§8, ut-docs#2824).
//
// The store's main till on the realtime tier holds one socket to the cloud.
// A nudge on it — or a cloud hello with a new link_version — kicks the
// ordinary cloudsync check-in (d.CloudSyncNow), which stays the source of
// truth; the check-in's outcome re-reads the gate (tier, role) through
// d.CloudLink.CheckedIn. Replicas never dial: what the kicked check-in
// changes on the main till reaches them over the ADR-0114 LAN link through
// the existing change-point nudges (admin-bundle watch, plugin installs).
//
// Offline-first: the socket has its own goroutine; the sale path only calls
// the non-blocking, drop-when-down d.CloudLink.Sale.

// updateStateIdle is the update state until the fleet-update states land
// (#2726) — the same placeholder the LAN link's report carries.
const updateStateIdle = "idle"

// newCloudLinkClient builds the client over d; every callback reads the
// settings and enrolment at call time.
func newCloudLinkClient(d *common.Deps) *cloudlink.Client {
	return cloudlink.New(cloudlink.Options{
		Target: func(ctx context.Context) (cloudlink.Target, bool) {
			return cloudLinkTarget(ctx, d.Cfg, d.Settings, time.Now())
		},
		Kick: func() {
			select {
			case d.CloudSyncNow <- struct{}{}:
			default: // a check-in is already pending
			}
		},
		Status: func(context.Context) cloudlink.Status {
			var peers []fleetlink.PeerInfo
			if d.Link != nil {
				peers = d.Link.Peers()
			}
			return cloudLinkStatusOf(buildinfo.Version, peers)
		},
		Version:  buildinfo.Version,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	})
}

// cloudLinkTarget is the dial gate: the main till (no sync.primary_url — a
// replica never dials, whatever tier it has cached; its role is then
// primary or backoffice, both main-till roles to the cloud), the cached
// cloud_link tier realtime in "always" mode (on_demand needs the check-in's
// link_wanted_for_s, not built — ADR-0117 §2), and an enrolled cloud.
func cloudLinkTarget(ctx context.Context, cfg *config.Config, s entitlement.Reader, now time.Time) (cloudlink.Target, bool) {
	if v, _, _ := s.Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		return cloudlink.Target{}, false
	}
	if tier, mode := entitlement.CloudLink(ctx, s, now); tier != "realtime" || mode != "always" {
		return cloudlink.Target{}, false
	}
	m := enroll.Effective(cfg).Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return cloudlink.Target{}, false
	}
	return cloudlink.Target{
		BaseURL:  m.EndpointURL,
		StoreID:  m.StoreID,
		Bearer:   m.MerchantToken,
		DeviceID: enroll.CurrentStatus().DeviceID,
	}, true
}

// cloudLinkStatusOf is the status frame: this till plus each live LAN link
// (a till whose link is down is simply absent — the hub lists live links).
func cloudLinkStatusOf(version string, peers []fleetlink.PeerInfo) cloudlink.Status {
	st := cloudlink.Status{Version: version, UpdateState: updateStateIdle}
	for _, p := range peers {
		ps := cloudlink.PeerStatus{TillID: p.TillID, Link: "up", Version: p.Hello.Version, UpdateState: updateStateIdle}
		if p.HasReport {
			if p.Report.Version != "" {
				ps.Version = p.Report.Version
			}
			if p.Report.UpdateState != "" {
				ps.UpdateState = p.Report.UpdateState
			}
		}
		st.Peers = append(st.Peers, ps)
	}
	return st
}

// cloudLinkSaleOf maps a completed sale to the §4 summary (no lines, no
// customer or card data). tender_kind is the sale's tender type as
// pos.deriveTenderType stores it — the lowercase method key, "split" for
// more than one — which is what the my. live panel (#2826) reads: cash /
// card / voucher / split, any other key shown as Other. A refund's total is
// negative on the wire whatever sign the return row stores. Time is the
// sale's completion: its created_at, which InsertSale writes as
// completed_at too (one RFC3339 UTC instant); now only when that doesn't
// parse. Void stays false — this runs at completion, where no sale is
// void yet.
func cloudLinkSaleOf(s data.SaleDetail, now time.Time) cloudlink.Sale {
	at := now
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s.CreatedAt)); err == nil {
		at = t.UTC()
	}
	tender := strings.ToLower(strings.TrimSpace(s.TenderType))
	if tender == "" {
		tender = "unknown"
	}
	refund := s.SaleType == "return"
	total := s.Total
	if refund && total > 0 {
		total = -total
	}
	return cloudlink.Sale{
		ID:         s.ID,
		Time:       at,
		TotalMinor: total,
		Currency:   s.Currency,
		TenderKind: tender,
		ItemCount:  len(s.Lines),
		Refund:     refund,
	}
}

// StartCloudLink runs d.CloudLink until ctx ends, joined by app.Run's
// drain; on shutdown it says bye{shutdown} before closing.
func StartCloudLink(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	if d.CloudLink == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.CloudLink.Run(ctx)
	}()
}
