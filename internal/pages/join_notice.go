package pages

import (
	"context"
	"fmt"
	"html/template"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerJoinNoticeUI wires the standalone till's half of ambient
// discovery (ADR-0033 amendment, ut-docs#2721) — the mirror of
// GET /ui/pairing-notice (pending_pairings.go), which is the MAIN till's
// "someone wants to pair" notice:
//
//   - GET /ui/join-notice: base.html polls it on every page. It renders
//     "Found <shop's main till> — link this till?" when d.JoinWatch (ticked
//     by the pull loop) holds a candidate, and nothing (200, empty body —
//     the nav fragments' "nothing to show") for a replica, a dismissed
//     notice, no candidate, a caller who can't manage sync, or a till that
//     already has enrolled replicas (it IS a main till — ut-docs#2721
//     Tester finding: an empty sync.primary_url alone doesn't mean
//     standalone).
//   - POST /ui/join-notice/dismiss: a manager says "not this till", for
//     good — sync.join_banner_dismissed, per-till and server-side, unlike
//     the pairing notice's per-session dismiss.
//
// "Link this till" posts the SAME /api/sync/pair-start the Tills page's
// discovery results use: verification code, manager approval on the main
// till. Nothing here enrols anything.
func registerJoinNoticeUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/join-notice", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !canPerform(d, r, "sync_management") || d.SyncPrimaryURL(ctx) != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if v, _, err := d.Settings.Get(ctx, discovery.JoinBannerDismissedSettingKey); err != nil || v != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		c, ok := d.JoinWatch.Candidate()
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		// A MAIN till also has an empty sync.primary_url, and every
		// standalone till on the LAN advertises itself (ADR-0033 §1) — so
		// "not a replica" is not "standalone". A till with enrolled
		// replicas is already a shop's main till: offering to link it to
		// some fresh till would turn it into that till's replica. Same
		// "has any replica ever joined" read as Settings' quarantine
		// section (settings_page.go). Checked last so the common
		// no-candidate poll stays query-free; fails closed on an error.
		if hasEnrolledTills(ctx, d) {
			w.WriteHeader(http.StatusOK)
			return
		}
		// The candidate's name sits in RTL copy on fa/ar; <bdi> isolates it
		// so a Latin name ("test-2721-main") isn't reordered mid-word. The
		// translated copy and the name are each escaped before the fixed
		// <bdi> markup goes in, so Banner is safe as template.HTML.
		banner := fmt.Sprintf(template.HTMLEscapeString(httpx.T(httpx.RequestLocale(r), "tills.discovery.ambient.banner")),
			`<bdi class="join-notice-name">`+template.HTMLEscapeString(c.Name)+"</bdi>")
		httpx.RenderPartial("ui/partials/join_notice.html", map[string]any{
			"Banner":  template.HTML(banner), //nolint:gosec // both parts HTML-escaped above; <bdi> is a fixed literal
			"Name":    c.Name,
			"BaseURL": c.BaseURL,
			"TillID":  c.TillID,
		})(w, r)
	})

	// Same gate as pair-start itself (managerGate, sync_management): only
	// someone who could link this till may decline linking it.
	gate := managerGate(d)
	mux.HandleFunc("POST /ui/join-notice/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		if err := d.Settings.Set(r.Context(), discovery.JoinBannerDismissedSettingKey, "1"); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "join-notice", err)
			return
		}
		logging.L().Infof("sync: join-a-main-till notice dismissed on this till")
		// Empty 200: the button targets #join-notice-mount, so this clears
		// the notice in place; the next poll stays empty.
		w.WriteHeader(http.StatusOK)
	})
}

// hasEnrolledTills reports whether this till has replicas enrolled — i.e.
// it is already a main till. An error (or no DB) counts as "yes" so the
// join notice fails closed rather than offering to demote a main till.
func hasEnrolledTills(ctx context.Context, d *common.Deps) bool {
	if d.Db == nil {
		return true
	}
	list, err := data.NewTillsRepo(d.Db).ListTills(ctx)
	if err != nil {
		logging.L().Errorf("join-notice: list tills: %v", err)
		return true
	}
	return len(list) > 0
}
