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
	"github.com/universaltill/universal-till/internal/httpx"
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
// the existing change-point nudges (admin-bundle watch, plugin installs),
// and once that check-in reached the cloud the nudge itself is relayed as a
// cloud_checkin frame so each replica checks in now too (ut-docs#2893).
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
		Relay: relayCloudCheckinToReplicas(d),
		Status: func(ctx context.Context) cloudlink.Status {
			var peers []fleetlink.PeerInfo
			if d.Link != nil {
				peers = d.Link.Peers()
			}
			return cloudLinkStatusOf(ctx, d, buildinfo.Version, peers)
		},
		Version:  buildinfo.Version,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	})
}

// relayCloudCheckinToReplicas passes a cloud nudge on to every linked
// replica as a fleetlink cloud_checkin (ADR-0117 §4, ut-docs#2893): each
// kicks its own cloudsync check-in. Non-blocking; a no-op with no hub or
// no replicas linked.
func relayCloudCheckinToReplicas(d *common.Deps) func(scopes []string, linkVersion int64) {
	return func(scopes []string, linkVersion int64) {
		if d.Link != nil {
			d.Link.RelayCloudCheckin(scopes, linkVersion)
		}
	}
}

// cloudLinkGateReason classifies why this till may or may not hold the
// cloud link, for the status surfaces (ut-docs#2895) — cloudLinkTarget
// below decides whether the client dials at all; this must classify the
// exact same conditions, in the same order, so the reason always matches
// what the client is actually doing.
type cloudLinkGateReason string

const (
	gateEligible     cloudLinkGateReason = ""              // dials, when enrolled
	gateNotMainTill  cloudLinkGateReason = "not_main_till" // sync.primary_url set: a replica
	gateTierPeriodic cloudLinkGateReason = "tier_periodic" // cached tier/mode isn't realtime/always
	gateUnenrolled   cloudLinkGateReason = "unenrolled"    // no cloud enrolment at all
)

// cloudLinkGate is cloudLinkTarget's decision, plus why: an enrolled cloud
// (checked first — the overwhelmingly common case is no cloud relationship
// at all, and that must read as "unenrolled" for the status row, never as
// "tier_periodic": a never-enrolled till also has no cached realtime tier,
// so checking tier before enrolment would misreport it as an everyday
// periodic plan instead of no cloud at all), the main till (no
// sync.primary_url — a replica never dials, whatever tier it has cached;
// its role is then primary or backoffice, both main-till roles to the
// cloud), and the cached cloud_link tier realtime in "always" mode
// (on_demand needs the check-in's link_wanted_for_s, not built — ADR-0117
// §2). cloudLinkTarget's own boolean result is the same whatever order
// these three run in; only the reason (for the status row) depends on it.
func cloudLinkGate(ctx context.Context, cfg *config.Config, s entitlement.Reader, now time.Time) (cloudlink.Target, cloudLinkGateReason) {
	m := enroll.Effective(cfg).Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return cloudlink.Target{}, gateUnenrolled
	}
	if v, _, _ := s.Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		return cloudlink.Target{}, gateNotMainTill
	}
	if tier, mode := entitlement.CloudLink(ctx, s, now); tier != "realtime" || mode != "always" {
		return cloudlink.Target{}, gateTierPeriodic
	}
	return cloudlink.Target{
		BaseURL:  m.EndpointURL,
		StoreID:  m.StoreID,
		Bearer:   m.MerchantToken,
		DeviceID: enroll.CurrentStatus().DeviceID,
	}, gateEligible
}

// cloudLinkTarget is the dial gate cloudlink.Client.Run reads: cloudLinkGate
// without the reason, which only the status surfaces need.
func cloudLinkTarget(ctx context.Context, cfg *config.Config, s entitlement.Reader, now time.Time) (cloudlink.Target, bool) {
	t, reason := cloudLinkGate(ctx, cfg, s, now)
	return t, reason == gateEligible
}

// maxCloudLinkStatusPeers caps the status frame's peer list: ut-cloud keeps
// only the first 64 (tilllink.maxStatusPeers), and ADR-0117 §7 bounds a
// message at 16 KiB. Live peers go first, so the cap drops down tills.
const maxCloudLinkStatusPeers = 64

// cloudLinkStatusOf is the status frame: this till plus every enrolled LAN
// till's link — live ones from the hub (which only ever tracks currently-
// connected peers), any other enrolled till listed as down (ut-docs#2895):
// one extra indexed SELECT against the same tills table tillsRosterData
// already queries every 10s for the Tills page, so it's cheap on the
// StatusEvery cadence (≥ 30 s, and only while linked) this runs on.
func cloudLinkStatusOf(ctx context.Context, d *common.Deps, version string, peers []fleetlink.PeerInfo) cloudlink.Status {
	st := cloudlink.Status{Version: version, UpdateState: updateStateIdle}
	live := make(map[string]bool, len(peers))
	for _, p := range peers {
		if len(st.Peers) >= maxCloudLinkStatusPeers {
			break // the hub caps links well below this (ADR-0114); defensive
		}
		live[p.TillID] = true
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
	if d.Db == nil {
		return st
	}
	tills, err := data.NewTillsRepo(d.Db).ListTills(ctx)
	if err != nil {
		return st
	}
	for _, t := range tills {
		if len(st.Peers) >= maxCloudLinkStatusPeers {
			break
		}
		if !live[t.ID] {
			st.Peers = append(st.Peers, cloudlink.PeerStatus{TillID: t.ID, Link: "down"})
		}
	}
	return st
}

// publishCloudLinkSale sends one sale frame for the cloud link's live view
// (ADR-0117 §4/§8, ut-docs#2894): non-blocking and nil-safe on every axis a
// caller might hit — d.CloudLink is nil only when no cloud link was built
// (tests); on a replica or a periodic-tier till it exists but never dials
// (cloudLinkTarget), and cloudlink.Client.Sale is
// itself a documented no-op when live_view is off or the socket is down
// (dropped, never queued). Every completion path that inserts a sale row —
// the tender path, a refund/return, and the primary's ingest of a
// replica's journaled sale — funnels through this one function so they
// can never drift on shape or on the nil/non-blocking guarantee.
//
// tillID is "" for a sale/refund completed on THIS till (cloudlink.Sale's
// own "" = this device convention) or the reporting replica's till id for
// a journal-ingested sale (ut-docs#2894 AC2) — the cloud still attributes
// the FRAME (the authenticated socket) to this, the main, device; till_id
// is purely the my. live panel's "which physical till" label.
func publishCloudLinkSale(d *common.Deps, detail data.SaleDetail, tillID string) {
	s := cloudLinkSaleOf(detail, time.Now())
	s.TillID = tillID
	d.CloudLink.Sale(s)
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

// Cloud link status row, till side (ut-docs#2895): the Tills page's
// "Cloud link" row — what d.CloudLink's own State (what actually happened
// on the wire) is doing right now, plus, only while idle, cloudLinkGate's
// reason this till never tries at all. The two never overlap: a dial/close
// refusal from the cloud always means the till DID try (StateWaiting), so
// "Paused" only ever describes a real refusal; a till whose own settings
// already say it shouldn't hold the link (a replica, or the plan's tier is
// periodic) reads as the everyday "Periodic" state instead of an alarming
// "Paused" one. Hidden entirely when the shop has no cloud enrolment at
// all — the status bar's "Marketplace: not connected" chip (status.
// register_till) already covers that.

// cloudLinkRowView is what web/ui/partials/tills_roster.html renders for
// the row. LabelKey/HintKey are full web/locales keys so the template does
// no state-to-copy mapping of its own (ui/partials/main_till_status.html
// took the same shape for the LAN chip's state/hint pair).
type cloudLinkRowView struct {
	Show bool
	// Code is a short, stable machine value for a data attribute (never
	// translated copy) — same role as the LAN roster's data-link="up|down"
	// a few lines below this row in tills_roster.html.
	Code        string
	LabelKey    string
	HintKey     string
	NextAttempt string // localized date-time; HintKey's printf arg when set
}

func (v cloudLinkRowView) reconnectingNow() cloudLinkRowView {
	v.HintKey = "tills.cloud_link.hint_reconnecting_now"
	return v
}

// cloudLinkRowViewOf derives the row purely from its inputs — kept
// separate from cloudLinkRowFor so every state (and the hidden/unenrolled
// case) is a plain table test, same as link_status.go's deriveLinkView.
func cloudLinkRowViewOf(gateReason cloudLinkGateReason, state cloudlink.State, reason cloudlink.WaitReason, nextAttempt time.Time, locale string) cloudLinkRowView {
	if gateReason == gateUnenrolled {
		return cloudLinkRowView{}
	}
	switch state {
	case cloudlink.StateLinked:
		return cloudLinkRowView{Show: true, Code: "live", LabelKey: "tills.cloud_link.state_live", HintKey: "tills.cloud_link.hint_live"}
	case cloudlink.StateConnecting:
		v := cloudLinkRowView{Show: true, Code: "reconnecting", LabelKey: "tills.cloud_link.state_reconnecting"}
		if nextAttempt.IsZero() {
			return v.reconnectingNow()
		}
		v.HintKey = "tills.cloud_link.hint_reconnecting"
		v.NextAttempt = httpx.FormatDateTime(nextAttempt.Local(), locale)
		return v
	case cloudlink.StateRevoked:
		return cloudLinkRowView{Show: true, Code: "stopped", LabelKey: "tills.cloud_link.state_stopped", HintKey: "tills.cloud_link.hint_stopped"}
	case cloudlink.StateWaiting:
		switch reason {
		case cloudlink.WaitReasonNotMainTill:
			return cloudLinkRowView{Show: true, Code: "paused_not_main", LabelKey: "tills.cloud_link.state_paused_not_main", HintKey: "tills.cloud_link.hint_paused_not_main"}
		case cloudlink.WaitReasonTierChanged:
			return cloudLinkRowView{Show: true, Code: "paused_tier", LabelKey: "tills.cloud_link.state_paused_tier", HintKey: "tills.cloud_link.hint_paused_tier"}
		case cloudlink.WaitReasonRetryAfter:
			// The cloud said when: Run redials by itself at that time, no
			// check-in needed.
			v := cloudLinkRowView{Show: true, Code: "paused_busy", LabelKey: "tills.cloud_link.state_paused_busy"}
			if nextAttempt.IsZero() {
				return v.reconnectingNow()
			}
			v.HintKey = "tills.cloud_link.hint_paused_busy_retry"
			v.NextAttempt = httpx.FormatDateTime(nextAttempt.Local(), locale)
			return v
		default:
			return cloudLinkRowView{Show: true, Code: "paused_busy", LabelKey: "tills.cloud_link.state_paused_busy", HintKey: "tills.cloud_link.hint_paused_busy"}
		}
	default: // cloudlink.StateIdle
		switch gateReason {
		case gateEligible:
			// The gate says dial but the client hasn't yet (boot, or the
			// gate just opened): about to connect, not a periodic plan.
			return cloudLinkRowView{Show: true, Code: "connecting", LabelKey: "tills.cloud_link.state_connecting", HintKey: "tills.cloud_link.hint_connecting"}
		case gateNotMainTill:
			return cloudLinkRowView{Show: true, Code: "periodic", LabelKey: "tills.cloud_link.state_periodic", HintKey: "tills.cloud_link.hint_periodic_not_main"}
		}
		return cloudLinkRowView{Show: true, Code: "periodic", LabelKey: "tills.cloud_link.state_periodic", HintKey: "tills.cloud_link.hint_periodic_tier"}
	}
}

// cloudLinkRowFor is the row's view for this till now; nil-safe (d.CloudLink
// is nil only in tests — see cloud_link.go's own doc comment on newCloudLinkClient).
func cloudLinkRowFor(ctx context.Context, d *common.Deps, locale string) cloudLinkRowView {
	if d.CloudLink == nil || d.Cfg == nil || d.Settings == nil {
		return cloudLinkRowView{}
	}
	_, gateReason := cloudLinkGate(ctx, d.Cfg, d.Settings, time.Now())
	return cloudLinkRowViewOf(gateReason, d.CloudLink.State(), d.CloudLink.Reason(), d.CloudLink.NextAttempt(), locale)
}
