package pages

// Moving this till into another shop: the till-side approval (ut-docs#5127
// UT-1b; ut-docs reference/design-5127-till-approval-for-moves.md §5).
//
//   - GET /ui/move-approval-notice: a nav-level banner (base.html mount, like
//     the shop-name notice) for managers and admins only, never on the sale
//     screen, never a modal: "<shop> asked to move this till into their shop.
//     Approve or decline before <time>. Review". It also says once when a
//     move ended without this till (cancelled by the owner, expired, locked).
//   - GET /settings/cloud/move/{id}: the full approval page.
//   - POST /api/cloud/move/{id}: approve or decline. Always a FRESH manager
//     or admin PIN (checkStepUp: same-origin, a manager/admin session, the
//     shared lockout); approving also needs the target shop's name typed.
//     Only an elevated request reaches enroll.DecideMove, the one place that
//     signs. Three wrong PINs on one move decline it (enroll.LockMove).
//
// The cashier never sees any of it: every route refuses a session without
// the settings grant, server-side.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerMoveApproval(mux *http.ServeMux, d *common.Deps) {
	if d.Db != nil {
		repo := data.NewPOSRepo(d.Db)
		enroll.SetMoveHooks(enroll.MoveHooks{
			HasTradingHistory: repo.HasTradingHistory,
			ResetUploadLedger: repo.ResetSalesAggregateUploads,
		})
	}

	mux.HandleFunc("GET /ui/move-approval-notice", func(w http.ResponseWriter, r *http.Request) {
		if onSaleScreen(r) || !canPerform(d, r, "settings") || d.SyncPrimaryURL(r.Context()) != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		locale := httpx.RequestLocale(r)
		if list := enroll.PendingMoves(d.Cfg); len(list) > 0 {
			p := list[0]
			httpx.RenderPartial("ui/partials/move_approval_notice.html", map[string]any{
				"Fingerprint": "pending:" + p.MoveID,
				"Text": fmt.Sprintf(httpx.T(locale, "settings.move.notice_pending"),
					bidiIsolate(moveTargetLabel(p)), bidiIsolate(httpx.FormatDateTime(p.ExpiresAt.Local(), locale))),
				"Href": "/settings/cloud/move/" + p.MoveID,
			})(w, r)
			return
		}
		for _, e := range enroll.EndedMoves() {
			key := map[string]string{
				"cancelled": "settings.move.notice_cancelled",
				"expired":   "settings.move.notice_expired",
				"locked":    "settings.move.notice_locked",
			}[e.Status]
			if key == "" {
				continue
			}
			httpx.RenderPartial("ui/partials/move_approval_notice.html", map[string]any{
				"Fingerprint": e.Status + ":" + e.MoveID,
				"Text":        fmt.Sprintf(httpx.T(locale, key), bidiIsolate(endedTargetLabel(e))),
			})(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /settings/cloud/move/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		locale := httpx.RequestLocale(r)
		id := r.PathValue("id")
		page := map[string]any{
			"title": httpx.T(locale, "page.title.move_till"),
			"theme": d.ResolvedTheme(r),
		}
		p, ok := enroll.PendingMoveByID(d.Cfg, id)
		if !ok || d.SyncPrimaryURL(r.Context()) != "" {
			page["Gone"] = httpx.T(locale, "settings.move.gone")
			if e, ended := enroll.EndedMoveByID(id); ended {
				if key := endedResultKey(e.Status); key != "" {
					page["Gone"] = moveResultText(locale, key, endedTargetLabel(e))
				}
			}
			httpx.Render("ui/pages/move_approval.html", page)(w, r)
			return
		}
		from := kioskShopName(r.Context(), d)
		if from == "" {
			from = p.SourceStore
		}
		page["Move"] = map[string]any{
			"ID":      p.MoveID,
			"From":    from,
			"Into":    moveTargetLabel(p),
			"Expires": httpx.FormatDateTime(p.ExpiresAt.Local(), locale),
		}
		page["Offline"] = cloudKnownUnreachable(d)
		httpx.Render("ui/pages/move_approval.html", page)(w, r)
	})

	mux.HandleFunc("POST /api/cloud/move/{id}", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !canPerform(d, r, "settings") {
			w.WriteHeader(http.StatusForbidden)
			moveMsg(w, locale, "common.error.manager_or_admin_required", "", true)
			return
		}
		_ = r.ParseForm()
		id := r.PathValue("id")
		decision := r.Form.Get("decision")
		if _, err := uuid.Parse(id); err != nil || (decision != enroll.MoveApprove && decision != enroll.MoveDecline) {
			w.WriteHeader(http.StatusBadRequest)
			moveMsg(w, locale, "settings.move.gone", "", true)
			return
		}
		p, ok := enroll.PendingMoveByID(d.Cfg, id)
		if !ok || d.SyncPrimaryURL(r.Context()) != "" {
			moveDone(w, locale, "settings.move.gone", "")
			return
		}
		typed := r.Form.Get("target_name")
		if decision == enroll.MoveApprove && !strings.EqualFold(strings.TrimSpace(typed), strings.TrimSpace(moveTargetLabel(p))) {
			moveMsg(w, locale, "settings.move.name_mismatch", "", true)
			return
		}
		// Nothing is signed while the cloud is known to be out of reach,
		// and no PIN is asked for then either.
		if cloudKnownUnreachable(d) {
			moveMsg(w, locale, "settings.move.offline", "", true)
			return
		}
		elev := checkStepUp(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome != elevated {
			if errors.Is(elev.Err, auth.ErrInvalidPIN) && enroll.RecordWrongMovePIN(id) {
				enroll.LockMove(context.WithoutCancel(r.Context()), d.Cfg, d.Settings, id)
				settingsAudit(r, data.NewPOSRepo(d.Db), elev, "cloud", id, "move_locked", map[string]any{"target_store": p.TargetStore})
				moveDone(w, locale, "settings.move.locked", "")
				return
			}
			summary := "settings.move.summary_approve"
			if decision == enroll.MoveDecline {
				summary = "settings.move.summary_decline"
			}
			renderElevationPrompt(w, r, "/api/cloud/move/"+id, "#move-msg", httpx.T(locale, summary),
				[]elevationHiddenField{{Name: "decision", Value: decision}, {Name: "target_name", Value: typed}}, elev)
			return
		}
		res, err := enroll.DecideMove(r.Context(), d.Cfg, d.Settings, id, decision)
		if err != nil {
			key, terminal := moveDecisionErrorKey(err)
			arg := ""
			var refused *enroll.MoveRefusedError
			if errors.As(err, &refused) && refused.UsableFrom != "" {
				arg = refused.UsableFrom
				if t, perr := time.Parse(time.RFC3339, refused.UsableFrom); perr == nil {
					arg = httpx.FormatDateTime(t.Local(), locale)
				}
			}
			if terminal {
				moveDone(w, locale, key, arg)
			} else {
				moveMsg(w, locale, key, arg, true)
			}
			return
		}
		repo := data.NewPOSRepo(d.Db)
		if decision == enroll.MoveDecline {
			settingsAudit(r, repo, elev, "cloud", id, "move_declined", map[string]any{"target_store": p.TargetStore})
			moveDone(w, locale, "settings.move.declined", "")
			return
		}
		settingsAudit(r, repo, elev, "cloud", id, "move_approved", map[string]any{"target_store": p.TargetStore, "from_store": p.SourceStore})
		requestOperatorCheckin(d, operatorCheckinWindow) // the new shop's plan and data, now
		moveDone(w, locale, "settings.move.done", res.TargetName)
	})
}

// cloudKnownUnreachable: the status-bar probe last found the cloud out of
// reach. Unknown (no probe yet) is not "offline": the request then simply
// fails and says so.
func cloudKnownUnreachable(d *common.Deps) bool {
	if d == nil || d.NetReach == nil {
		return false
	}
	v := d.NetReach.Status().Reachable()
	return v != nil && !*v
}

// moveTargetLabel is the name the operator types and reads: the shop's
// name, or its id when the cloud sent none.
func moveTargetLabel(p enroll.PendingMove) string {
	if strings.TrimSpace(p.TargetName) != "" {
		return p.TargetName
	}
	return p.TargetStore
}

func endedTargetLabel(e enroll.EndedMove) string {
	if strings.TrimSpace(e.TargetName) != "" {
		return e.TargetName
	}
	return "—"
}

// endedResultKey is the approval page's text for a move that already ended.
func endedResultKey(status string) string {
	switch status {
	case "cancelled":
		return "settings.move.result_cancelled"
	case "expired":
		return "settings.move.result_expired"
	case "declined":
		return "settings.move.declined"
	case "locked":
		return "settings.move.locked"
	case "moved", "done":
		return "settings.move.result_done"
	case "failed", "unavailable":
		return "settings.move.result_failed"
	}
	return ""
}

// moveDecisionErrorKey maps DecideMove's error to its message and whether
// the move can no longer be decided (the buttons then go).
func moveDecisionErrorKey(err error) (string, bool) {
	var refused *enroll.MoveRefusedError
	switch {
	case errors.Is(err, enroll.ErrMoveOffline):
		return "settings.move.offline", false
	case errors.Is(err, enroll.ErrMoveUnknown), errors.Is(err, enroll.ErrMoveStale):
		return "settings.move.gone", true
	case errors.Is(err, enroll.ErrMoveNoKey):
		return "settings.move.result_key", false
	case errors.Is(err, enroll.ErrAttemptBusy):
		return "settings.enrol.busy", false
	case errors.As(err, &refused):
		switch refused.Code {
		case "move_expired":
			return "settings.move.result_expired", true
		case "move_cancelled":
			return "settings.move.result_cancelled", true
		case "move_declined":
			return "settings.move.declined", true
		case "move_done":
			return "settings.move.result_done", true
		case "move_failed", "move_unavailable":
			return "settings.move.result_failed", true
		case "move_key_quarantined":
			if refused.UsableFrom != "" {
				return "settings.move.result_quarantined", false
			}
			return "settings.move.result_key", false
		case "move_key_conflict", "move_key_missing":
			return "settings.move.result_key", false
		case "approval_invalid", "move_unauthorized":
			return "settings.move.result_invalid", false
		}
	}
	logging.L().Warnf("cloud move decision failed: %v", err)
	return "settings.move.result_error", false
}

func moveResultText(locale, key, arg string) string {
	msg := httpx.T(locale, key)
	if strings.Contains(msg, "%s") {
		msg = fmt.Sprintf(msg, bidiIsolate(arg))
	}
	return msg
}

// moveMsg writes one message into #move-msg.
func moveMsg(w http.ResponseWriter, locale, key, arg string, isErr bool) {
	cls, mark := "", "✅"
	if isErr {
		cls, mark = ` class="error"`, "❌"
	}
	fmt.Fprintf(w, `<span%s>%s %s</span>`, cls, mark, html.EscapeString(moveResultText(locale, key, arg)))
}

// moveDone writes a final message and removes the approve/decline actions:
// the move can no longer be decided here.
func moveDone(w http.ResponseWriter, locale, key, arg string) {
	isErr := key != "settings.move.done" && key != "settings.move.declined"
	moveMsg(w, locale, key, arg, isErr)
	fmt.Fprint(w, `<div id="move-actions" hx-swap-oob="true" hidden></div>`)
}

// moveRefusalKey is the Settings → Pair message for a move the cloud refused
// (ut-docs#5127 §4.1 refusals); nothing changed on the till in any case.
func moveRefusalKey(code string) string {
	switch code {
	case "move_needs_owner":
		return "settings.move.refused_needs_owner"
	case "move_other_owner":
		return "settings.move.refused_other_owner"
	case "move_shop_has_tills":
		return "settings.move.refused_shop_has_tills"
	case "move_currency_mismatch":
		return "settings.move.refused_currency"
	case "move_fiscal_device":
		return "settings.move.refused_fiscal_device"
	case "move_paid_licence":
		return "settings.move.refused_paid_licence"
	case "move_target_unavailable", "store_has_till", "store_merging":
		return "settings.move.refused_target_unavailable"
	case "move_source_unavailable":
		return "settings.move.refused_source_unavailable"
	case "move_pending":
		return "settings.move.refused_pending"
	case "move_declined_recently":
		return "settings.move.refused_declined_recently"
	case "move_rate_limited", "rate_limited":
		return "settings.move.refused_rate_limited"
	}
	return "settings.move.refused"
}
