package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The main till's Tills page, live (ADR-0114 §10, ut-docs#2742): each
// enrolled till's link up/down, role, version against the main till's,
// update state, push-queue depth and TLS pin, from the link hub's in-memory
// hellos and reports. The roster re-renders itself every 10 s through GET
// /ui/tills/roster — only that table is swapped, never the page. Bounded:
// one row per enrolled till, link data for at most MaxConns live links.

// tillPeerView is one till's link as the roster shows it.
type tillPeerView struct {
	Linked      bool
	Role        string // "replica" | "satellite" | "" (unknown)
	Version     string
	VersionCmp  string // "same" | "older" | "newer" | "" (not comparable)
	UpdateState string // "idle" | "waiting-safe-moment" | "downloading" | "failed" | ""
	UpdateCode  string // the failed:<code> detail, sanitised
	HasReport   bool
	QueueDepth  int
	TLSPinned   bool
}

// tillRosterRow is an enrolled till plus, on a main till, its link.
type tillRosterRow struct {
	data.TillRow
	Link *tillPeerView // nil where this till has no link hub to ask (a replica)
}

// tillPeerViewOf turns the hub's snapshot of one link into the roster's
// view. Everything a peer sent is treated as untrusted display data: role
// and update state are mapped onto known values, the failure code
// restricted to a short [a-z0-9_-] token.
func tillPeerViewOf(p fleetlink.PeerInfo, mainVersion string, now time.Time) tillPeerView {
	v := tillPeerView{
		Linked: p.HasHello && now.Sub(p.LastFrame) <= fleetlink.DefaultConfig().PeerTimeout,
	}
	if p.HasHello {
		switch p.Hello.Role {
		case "replica", "satellite":
			v.Role = p.Hello.Role
		}
		v.Version = p.Hello.Version
	}
	if p.HasReport {
		v.HasReport = true
		v.QueueDepth = p.Report.PushQueueDepth
		v.TLSPinned = p.Report.TLSPinned
		if p.Report.Version != "" {
			v.Version = p.Report.Version
		}
		state, code, _ := strings.Cut(p.Report.UpdateState, ":")
		switch state {
		case "idle", "waiting-safe-moment", "downloading":
			v.UpdateState = state
		case "failed":
			v.UpdateState, v.UpdateCode = state, safeToken(code, 32)
		}
	}
	if releaseVersion(v.Version) && releaseVersion(mainVersion) {
		switch linkUpdateNote(v.Version, mainVersion) {
		case linkUpdateWaiting:
			v.VersionCmp = "older"
		case linkMainUpdateWaiting:
			v.VersionCmp = "newer"
		default:
			v.VersionCmp = "same"
		}
	}
	return v
}

// safeToken keeps only [a-z0-9_-], at most n bytes.
func safeToken(s string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if b.Len() >= n {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tillsRosterData is what tills_roster.html renders, for both the page and
// the poll.
func tillsRosterData(ctx context.Context, d *common.Deps, w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	list, err := data.NewTillsRepo(d.Db).ListTills(ctx)
	if err != nil {
		return nil, err
	}
	// The primary's own name (ut-docs#396's till.name setting): shown on
	// this page regardless of role. till.name is not per-till, so on a
	// replica it is the main till's name synced down (ut-docs#405).
	primaryURL := d.SyncPrimaryURL(ctx)
	var thisTillID, primaryLastContact string
	if primaryURL != "" {
		thisTillID, _, _ = d.Settings.Get(ctx, "sync.till_id")
		primaryLastContact, _, _ = d.Settings.Get(ctx, "sync.last_contact_at")
	}
	// Link data lives on the main till's hub only; a replica's roster is a
	// synced copy with nothing live to say about its siblings.
	linkInfo := primaryURL == "" && d.Link != nil
	peers := map[string]fleetlink.PeerInfo{}
	if linkInfo {
		for _, p := range d.Link.Peers() {
			peers[p.TillID] = p
		}
	}
	now := time.Now()
	rows := make([]tillRosterRow, 0, len(list))
	for _, t := range list {
		row := tillRosterRow{TillRow: t}
		if linkInfo {
			v := tillPeerViewOf(peers[t.ID], buildinfo.Version, now)
			row.Link = &v
		}
		rows = append(rows, row)
	}
	return map[string]any{
		"Tills":              rows,
		"PrimaryTillName":    tillNameOrDefault(ctx, d, httpx.ResolveLocale(w, r)),
		"SyncPrimary":        primaryURL,
		"ThisTillID":         thisTillID,
		"PrimaryLastContact": primaryLastContact,
		"LinkInfo":           linkInfo,
		"MainVersion":        buildinfo.Version,
	}, nil
}

// registerTillsRoster: GET /ui/tills/roster, the Tills page's roster table,
// polled every 10 s on a main till. Same gate as the page.
func registerTillsRoster(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/tills/roster", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		m, err := tillsRosterData(r.Context(), d, w, r)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "tills_roster", err)
			return
		}
		httpx.RenderPartial("ui/partials/tills_roster.html", m)(w, r)
	})
}
