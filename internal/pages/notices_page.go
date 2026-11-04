package pages

// Notices from Universal Till and partners on the till (ADR-0176,
// ut-docs#5366). The cloud client (internal/cloudsync/notices.go) mirrors the
// notices into cloud_notices; this file is the bell, the inbox, the one
// non-modal toast and the critical strip. Everything here reads local data
// only and sits beside the sale path, never on it: a failed render is an
// empty fragment (ADR-0176 §6), and no handler touches the basket or tender.
//
// Gates (ADR-0176 §7, cashier is sale-only):
//   - the setting notices.enabled (default off) hides all of it;
//   - the permission notices.view gates the bell, the inbox and the
//     read/dismiss/acknowledge writes, server-side first;
//   - the strip fragment is the one exception: an unacknowledged critical
//     platform notice shows to every signed-in role, read-only for a role
//     without notices.view.

import (
	"context"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudnotice"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// noticesViewAction is the permission gating the bell, the inbox and the
// notice writes (migration 096).
const noticesViewAction = "notices.view"

// noticesNow is the page layer's clock; tests pin it.
var noticesNow = time.Now

// noticesToastMS is how long an important toast stays (ADR-0176 §6: at least
// 8 s; the UX notes choose 10 s, paused while hovered, focused or touched).
const noticesToastMS = 10000

// noticeRowView is one inbox row.
type noticeRowView struct {
	N            data.CloudNotice
	Body         template.HTML
	Link         *cloudnotice.Link
	LinkExternal bool
	LinkLabelKey bool // no label from the sender: use the generic "Open"
	Open         bool
	Unread       bool
	Partner      bool
	CanAck       bool // a manager/admin may acknowledge a critical notice
	Acked        bool
	Tab          string
}

func noticesEnabled(ctx context.Context, d *common.Deps) bool {
	if d == nil || d.Db == nil {
		return false
	}
	v, _, err := data.NewSettingsRepo(d.Db).Get(ctx, cloudnotice.SettingEnabled)
	return err == nil && cloudnotice.Truthy(v)
}

// noticesCanAcknowledge: a manager or admin (ADR-0176 §6, D4 default); with
// auth switched off (dev tooling) everyone, as canPerform does.
func noticesCanAcknowledge(d *common.Deps, r *http.Request) bool {
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return true
	}
	u, ok := auth.FromContext(r.Context())
	return ok && u.IsManager() && canPerform(d, r, noticesViewAction)
}

func newNoticeRow(n data.CloudNotice, open bool, canAck bool, tab string) noticeRowView {
	v := noticeRowView{
		N: n, Body: cloudnotice.RenderBody(n.Body), Open: open, Unread: n.Unread(),
		Partner: n.SenderKind == cloudnotice.SenderPartner, CanAck: canAck && n.Severity == cloudnotice.Critical && n.AckedAt == nil,
		Acked: n.AckedAt != nil, Tab: tab,
	}
	if l := cloudnotice.ParseLink(n.LinkJSON); l != nil {
		v.Link = l
		v.LinkExternal = l.Kind == "https"
		v.LinkLabelKey = strings.TrimSpace(l.Label) == ""
	}
	return v
}

func registerNoticesPages(mux *http.ServeMux, d *common.Deps) {
	repo := func() *data.CloudNoticeRepo { return data.NewCloudNoticeRepo(d.Db) }
	locale := func(r *http.Request) string { return httpx.RequestLocale(r) }
	empty := func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

	// gate is the server-side check of every inbox route and write: flag on,
	// then notices.view. 404 with the flag off (the routes do not exist for
	// the operator), 403 without the permission.
	gate := func(w http.ResponseWriter, r *http.Request, page bool) bool {
		if !noticesEnabled(r.Context(), d) {
			http.NotFound(w, r)
			return false
		}
		if canPerform(d, r, noticesViewAction) {
			return true
		}
		if page {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
		} else {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
		}
		return false
	}

	// The per-till switch (ADR-0176 §11), default off. Manager-gated like the
	// rest of Settings. Turning it off erases the held notices (local
	// erasure, §3) and forgets the pull cursor, so a later turn-on starts
	// from a clean pull.
	mux.HandleFunc("POST /api/settings/notices", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		_ = r.ParseForm()
		on := r.Form.Get("enabled") == "on" || r.Form.Get("enabled") == "1"
		val := "false"
		if on {
			val = "true"
		}
		sr := data.NewSettingsRepo(d.Db)
		if err := sr.SetMany(r.Context(), map[string]string{cloudnotice.SettingEnabled: val}); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings_notices", err)
			return
		}
		if !on {
			if err := repo().Reset(r.Context()); err != nil {
				logging.L().Warnf("notices: reset: %v", err)
			}
			_ = sr.SetMany(r.Context(), map[string]string{cloudnotice.SettingRev: "", cloudnotice.SettingLang: ""})
		}
		settingsAudit(r, data.NewPOSRepo(d.Db), elevationCheck{ActorID: settingsActorID(r)}, "settings", cloudnotice.SettingEnabled, "notices_enabled_changed", map[string]any{"enabled": on})
		w.Header().Set("HX-Trigger", "notices-changed")
		w.WriteHeader(http.StatusNoContent)
	})

	// The inbox page.
	mux.HandleFunc("GET /notices", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r, true) {
			return
		}
		tab := data.NoticeUnread
		tabName := "unread"
		if r.URL.Query().Get("tab") == "all" {
			tab, tabName = data.NoticeAll, "all"
		}
		openID := r.URL.Query().Get("open")
		now := noticesNow()
		list, err := repo().List(r.Context(), now, tab)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		canAck := noticesCanAcknowledge(d, r)
		rows := make([]noticeRowView, 0, len(list))
		for _, n := range list {
			open := n.ID == openID
			if open && n.ReadAt == nil {
				_ = repo().MarkRead(r.Context(), n.ID, now)
				n.ReadAt = &now
			}
			rows = append(rows, newNoticeRow(n, open, canAck, tabName))
		}
		// After the open-by-link above, so the tab's count matches the bell.
		unread, _, _ := repo().UnreadCount(r.Context(), now)
		httpx.Render("ui/pages/notices.html", map[string]any{
			"title":     httpx.T(locale(r), "page.title.notices"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Rows":      rows,
			"Tab":       tabName,
			"Unread":    unread,
		})(w, r)
	})

	rowFragment := func(w http.ResponseWriter, r *http.Request, id string, open bool) {
		now := noticesNow()
		n, ok, err := repo().Get(r.Context(), id)
		if err != nil || !ok || !n.ExpiresAt.After(now) {
			// Gone (expired or withdrawn) between render and tap: an empty row.
			w.Header().Set("HX-Trigger", "notices-changed")
			empty(w)
			return
		}
		tab := r.URL.Query().Get("tab")
		w.Header().Set("HX-Trigger", "notices-changed")
		httpx.RenderBlock("ui/pages/notices.html", "notice_row", newNoticeRow(n, open, noticesCanAcknowledge(d, r), tab))(w, r)
	}

	// Expand (marks read) / collapse a row.
	mux.HandleFunc("POST /notices/{id}/read", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r, false) {
			return
		}
		id := r.PathValue("id")
		if err := repo().MarkRead(r.Context(), id, noticesNow()); err != nil {
			logging.L().Warnf("notices: mark read: %v", err)
		}
		rowFragment(w, r, id, true)
	})
	mux.HandleFunc("GET /notices/{id}/row", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r, false) {
			return
		}
		rowFragment(w, r, r.PathValue("id"), false)
	})
	mux.HandleFunc("POST /notices/{id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r, false) {
			return
		}
		id := r.PathValue("id")
		// A critical notice is never dismissed, only acknowledged (D4).
		if n, ok, _ := repo().Get(r.Context(), id); ok && n.Severity != cloudnotice.Critical {
			if err := repo().Dismiss(r.Context(), id, noticesNow()); err != nil {
				logging.L().Warnf("notices: dismiss: %v", err)
			}
		}
		rowFragment(w, r, id, false)
	})
	mux.HandleFunc("POST /notices/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r, false) {
			return
		}
		if !noticesCanAcknowledge(d, r) {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		id := r.PathValue("id")
		if err := repo().Acknowledge(r.Context(), id, noticesNow()); err != nil {
			common.LocalizedError(w, r, http.StatusNotFound, "common.error.server")
			return
		}
		// The strip fragment polls too; this response is whichever surface
		// asked: the strip (hx-target #notices-strip) gets the now-empty strip.
		if r.URL.Query().Get("from") == "strip" {
			w.Header().Set("HX-Trigger", "notices-changed")
			stripFragment(w, r, d)
			return
		}
		rowFragment(w, r, id, true)
	})

	// Bell + badges. Empty body (nothing in the nav) with the flag off or
	// without notices.view. The phone burger badge and its count-aware label
	// ride along out of band.
	mux.HandleFunc("GET /notices/bell", func(w http.ResponseWriter, r *http.Request) {
		if !noticesEnabled(r.Context(), d) || !canPerform(d, r, noticesViewAction) {
			empty(w)
			return
		}
		n, crit, err := repo().UnreadCount(r.Context(), noticesNow())
		if err != nil {
			empty(w)
			return
		}
		count := ""
		if n > 9 {
			count = "9+"
		} else if n > 0 {
			count = string(rune('0' + n))
		}
		httpx.RenderBlock("ui/pages/notices.html", "notices_bell", map[string]any{
			"Unread": n, "Count": count, "Critical": crit,
		})(w, r)
	})

	// The critical strip: every role, read-only without notices.view.
	mux.HandleFunc("GET /notices/strip", func(w http.ResponseWriter, r *http.Request) {
		stripFragment(w, r, d)
	})

	// The important toast: at most one, never twice for the same id+rev,
	// notices.view only. The client skips the poll while the payment overlay
	// is open, so a held toast stays queued (this handler only marks it
	// shown when it is actually rendered).
	mux.HandleFunc("GET /notices/toast", func(w http.ResponseWriter, r *http.Request) {
		if !noticesEnabled(r.Context(), d) || !canPerform(d, r, noticesViewAction) {
			empty(w)
			return
		}
		now := noticesNow()
		n, ok, err := repo().NextToast(r.Context(), now)
		if err != nil || !ok {
			empty(w)
			return
		}
		if err := repo().MarkToasted(r.Context(), n.ID, now); err != nil {
			empty(w)
			return
		}
		httpx.RenderBlock("ui/pages/notices.html", "notices_toast", map[string]any{
			"N": n, "Body": cloudnotice.RenderBody(n.Body), "DismissMS": noticesToastMS,
		})(w, r)
	})
}

func stripFragment(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	if !noticesEnabled(r.Context(), d) {
		w.WriteHeader(http.StatusOK)
		return
	}
	list, err := data.NewCloudNoticeRepo(d.Db).Strip(r.Context(), noticesNow())
	if err != nil || len(list) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	canView := canPerform(d, r, noticesViewAction)
	httpx.RenderBlock("ui/pages/notices.html", "notices_strip", map[string]any{
		"N": list[0], "More": len(list) - 1, "CanView": canView,
		"CanAck": canView && noticesCanAcknowledge(d, r),
	})(w, r)
}
