package pages

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/channelactions"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Staff actions on an online order (ADR-0169 §9, ut-docs#4768 slice D). A tap
// writes the till's outbox row and an audit row in one transaction and returns:
// no network call, no change to the order's status, no stock movement. The
// cloudsync tick uploads the queue (internal/channelactions); the order moves
// only when the cloud's answer or a directive is applied.
//
// Who may act (D1): a user allowed both "reports" (the view) and
// "stock_management" (cancel can put items back into stock), on the main till
// (no sync.primary_url) that is linked to the cloud. Every POST is same-origin
// only. The dismiss routes need no main-till or enrolment check: a former main
// till must be able to clear its blocked rows.

// channelFormValues are the submitted values, echoed back on an error.
type channelFormValues struct {
	Carrier, TrackingNumber, TrackingURL, Reason string
	Restock                                      bool
}

// channelActionView is a stored action with the locale key of its outcome.
type channelActionView struct {
	data.ChannelOrderAction
	ReasonKey string
}

// channelRefusedKeys maps the contract's refusal codes to locale keys. A code
// this till does not know (the contract allows new ones) shows the generic text;
// the template never builds a key from a code.
var channelRefusedKeys = map[string]string{
	"invalid_transition": "channel_orders.refused.invalid_transition",
	"order_cancelled":    "channel_orders.refused.order_cancelled",
	"order_refunded":     "channel_orders.refused.order_refunded",
	"already_shipped":    "channel_orders.refused.already_shipped",
	"stale":              "channel_orders.refused.stale",
	"plan_required":      "channel_orders.refused.plan_required",
	"connection_revoked": "channel_orders.refused.connection_revoked",
	"order_changed":      "channel_orders.refused.order_changed",
	"unknown_order":      "channel_orders.refused.unknown_order",
	"invalid_action":     "channel_orders.refused.invalid_action",
}

func channelRefusedKey(code string) string {
	if k, ok := channelRefusedKeys[code]; ok {
		return k
	}
	return "channel_orders.refused.generic"
}

// channelBlockedKey is the banner text for the code the cloud refused with.
func channelBlockedKey(code string) string {
	switch code {
	case "main_till_required", "store_mismatch", "device_credential_required":
		return code
	}
	return "generic"
}

// channelActionsHere reports whether this till may take actions at all: the
// main till (no sync.primary_url), linked to the cloud.
func channelActionsHere(ctx context.Context, d *common.Deps) bool {
	if v, _, _ := d.Settings.Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		return false
	}
	return enroll.CredentialsComplete(enroll.Effective(d.Cfg).Marketplace)
}

// channelActionPermitted is the shared per-request gate: same origin, and the
// operator may see Reports and manage stock (D1). It answers the 403 itself.
func channelActionPermitted(d *common.Deps, w http.ResponseWriter, r *http.Request) bool {
	if !sameOriginRequest(r) {
		httpx.RenderError(w, r, http.StatusForbidden, "channel_orders.error.actions_not_here", nil)
		return false
	}
	if !canPerform(d, r, "reports") || !canPerform(d, r, "stock_management") {
		httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
		return false
	}
	return true
}

// channelOrderDetailData builds the data of the detail page and of the actions
// block, so the GET and the POST routes cannot drift. formErr/formField/
// formAction/form are set only when a POST is re-rendered with an error.
func channelOrderDetailData(ctx context.Context, d *common.Deps, r *http.Request, o data.ChannelOrder, formErr, formField, formAction string, form channelFormValues) (map[string]any, error) {
	repo := data.NewChannelOrderActionsRepo(d.Db)
	rows, err := repo.ForOrder(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	blockedCount, blockedCode, err := repo.Blocked(ctx)
	if err != nil {
		return nil, err
	}
	var pending, blocked, outcomes []channelActionView
	var pendingActions []string
	for _, a := range rows {
		v := channelActionView{ChannelOrderAction: a}
		switch a.State {
		case data.ChannelActionPending:
			pending = append(pending, v)
			pendingActions = append(pendingActions, a.Action)
		case data.ChannelActionBlocked:
			blocked = append(blocked, v)
		case data.ChannelActionRefused:
			v.ReasonKey = channelRefusedKey(a.ResultCode)
			outcomes = append(outcomes, v)
		case data.ChannelActionNoted:
			outcomes = append(outcomes, v)
		}
	}
	here := channelActionsHere(ctx, d)
	mayAct := canPerform(d, r, "reports") && canPerform(d, r, "stock_management")
	canAct := here && mayAct && blockedCount == 0
	var allowed, forward []string
	next := ""
	canShip, canCancel := false, false
	if canAct {
		allowed = data.ChannelOrderAllowedActions(o.Status, o.FulfilmentRank, pendingActions)
		for _, a := range allowed {
			switch a {
			case "ship":
				canShip = true
			case "cancel":
				canCancel = true
			default:
				forward = append(forward, a)
			}
			if next == "" && a != "cancel" {
				next = a
			}
		}
	}
	if formAction != "cancel" {
		form.Restock = true // D2: restock is ticked by default
	}
	return map[string]any{
		"title":         httpx.T(httpx.RequestLocale(r), "page.title.channel_orders"),
		"theme":         d.CurrentState().Theme,
		"menuItems":     d.MenuSnapshot(),
		"Order":         o,
		"Pending":       pending,
		"Blocked":       blocked,
		"Outcomes":      outcomes,
		"BlockedCount":  blockedCount,
		"BlockedKey":    channelBlockedKey(blockedCode),
		"CanAct":        canAct,
		"CanDismiss":    mayAct,
		"Forward":       forward,
		"NextStep":      next,
		"CanShip":       canShip,
		"CanCancel":     canCancel,
		"CancelReasons": channelactions.CancelReasons,
		"FormError":     formErr,
		"FormField":     formField,
		"FormAction":    formAction,
		"Form":          form,
	}, nil
}

// registerChannelOrderActions adds the POST routes of the actions block.
func registerChannelOrderActions(mux *http.ServeMux, d *common.Deps, orders *data.ChannelOrdersRepo) {
	actions := data.NewChannelOrderActionsRepo(d.Db)

	// respond re-renders the actions block for an htmx swap, or redirects /
	// re-renders the whole page for a plain form post.
	respond := func(w http.ResponseWriter, r *http.Request, orderID string, ok bool, formErr, field, action string, form channelFormValues) {
		o, found, err := orders.Get(r.Context(), orderID)
		if err != nil || !found {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		}
		if ok && !httpx.IsFragmentSwap(w, r) {
			http.Redirect(w, r, "/channel-orders/"+orderID, http.StatusSeeOther)
			return
		}
		view, err := channelOrderDetailData(r.Context(), d, r, o, formErr, field, action, form)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		}
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderBlock("ui/pages/channel_order_detail.html", "channel_order_actions", view)(w, r)
			return
		}
		httpx.Render("ui/pages/channel_order_detail.html", view)(w, r)
	}

	mux.HandleFunc("POST /channel-orders/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		if !channelActionPermitted(d, w, r) {
			return
		}
		id := r.PathValue("id")
		if _, found, err := orders.Get(r.Context(), id); err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		} else if !found {
			httpx.RenderError(w, r, http.StatusNotFound, "channel_orders.error.not_found", nil)
			return
		}
		if err := r.ParseForm(); err != nil {
			httpx.RenderError(w, r, http.StatusBadRequest, "channel_orders.error.save_failed", err)
			return
		}
		// The page was shown when this till could act and no longer can (it
		// became an additional till, or lost its cloud link): say why, once.
		if !channelActionsHere(r.Context(), d) {
			if !httpx.IsFragmentSwap(w, r) {
				httpx.RenderError(w, r, http.StatusForbidden, "channel_orders.error.actions_not_here", nil)
				return
			}
			respond(w, r, id, false, "channel_orders.error.actions_not_here", "", "", channelFormValues{})
			return
		}
		act := r.PostForm.Get("action")
		form := channelFormValues{
			Carrier:        strings.TrimSpace(r.PostForm.Get("carrier")),
			TrackingNumber: strings.TrimSpace(r.PostForm.Get("tracking_number")),
			TrackingURL:    strings.TrimSpace(r.PostForm.Get("tracking_url")),
			Reason:         r.PostForm.Get("reason"),
			Restock:        r.PostForm.Get("restock") == "1",
		}
		in := data.ChannelOrderActionInput{OrderID: id, Action: act, StaffID: actorIDFor(r), Now: time.Now()}
		switch act {
		case "accept", "pick", "pack":
		case "ship":
			sh := data.ChannelShipment{Carrier: form.Carrier, TrackingNumber: form.TrackingNumber, TrackingURL: form.TrackingURL}
			if field, key := channelactions.ValidateShipment(sh); field != "" {
				respond(w, r, id, false, key, field, "ship", form)
				return
			}
			in.Shipment = &sh
		case "cancel":
			if !channelactions.ValidCancelReason(form.Reason) {
				respond(w, r, id, false, "channel_orders.error.reason_invalid", "reason", "cancel", form)
				return
			}
			in.Cancel = &data.ChannelCancel{Reason: form.Reason, Restock: form.Restock}
		default:
			respond(w, r, id, false, "channel_orders.error.action_not_allowed", "", "", channelFormValues{})
			return
		}
		_, _, err := actions.Enqueue(r.Context(), in)
		switch {
		case errors.Is(err, data.ErrChannelActionNotAllowed):
			respond(w, r, id, false, "channel_orders.error.action_not_allowed", "", "", channelFormValues{})
			return
		case errors.Is(err, data.ErrChannelActionsBlocked):
			respond(w, r, id, false, "", "", "", channelFormValues{}) // the banner explains
			return
		case errors.Is(err, data.ErrChannelOrderNotFound):
			httpx.RenderError(w, r, http.StatusNotFound, "channel_orders.error.not_found", nil)
			return
		case err != nil:
			logging.L().Warnf("channel order action %s on %s not saved: %v", act, id, err)
			respond(w, r, id, false, "channel_orders.error.save_failed", "", "", channelFormValues{})
			return
		}
		// An online till does not wait for the next tick: ask the sync loop for
		// a check-in now (non-blocking; nil-safe). The upload is part of it.
		select {
		case d.CloudSyncNow <- struct{}{}:
		default:
		}
		respond(w, r, id, true, "", "", "", channelFormValues{})
	})

	mux.HandleFunc("POST /channel-orders/{id}/actions/{action_id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !channelActionPermitted(d, w, r) {
			return
		}
		id := r.PathValue("id")
		if _, found, err := orders.Get(r.Context(), id); err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
			return
		} else if !found {
			httpx.RenderError(w, r, http.StatusNotFound, "channel_orders.error.not_found", nil)
			return
		}
		if _, err := actions.Dismiss(r.Context(), id, r.PathValue("action_id"), actorIDFor(r)); err != nil {
			logging.L().Warnf("channel order action dismiss on %s failed: %v", id, err)
			respond(w, r, id, false, "channel_orders.error.save_failed", "", "", channelFormValues{})
			return
		}
		respond(w, r, id, true, "", "", "", channelFormValues{})
	})

	mux.HandleFunc("POST /channel-orders/blocked/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !channelActionPermitted(d, w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			httpx.RenderError(w, r, http.StatusBadRequest, "channel_orders.error.save_failed", err)
			return
		}
		id := r.PostForm.Get("order_id")
		if _, found, err := orders.Get(r.Context(), id); err != nil || !found {
			if err != nil {
				httpx.RenderError(w, r, http.StatusInternalServerError, "channel_orders.error.load_failed", err)
				return
			}
			httpx.RenderError(w, r, http.StatusNotFound, "channel_orders.error.not_found", nil)
			return
		}
		if _, err := actions.DismissBlocked(r.Context(), actorIDFor(r)); err != nil {
			logging.L().Warnf("channel order actions: dismissing the blocked actions failed: %v", err)
			respond(w, r, id, false, "channel_orders.error.save_failed", "", "", channelFormValues{})
			return
		}
		respond(w, r, id, true, "", "", "", channelFormValues{})
	})
}
