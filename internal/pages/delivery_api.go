package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The delivery order state-machine API (ADR-0215, ut-docs#6581 / #6583): the
// JSON the delivery inbox UI (S4) and the kitchen screen (S5) are built on. It
// carries NO markup. Responses use the till envelope {"data": ..., "error":
// null}; an error is {"data": null, "error": {"code": "..."}} with a stable
// snake_case code.
//
// A tap only queues the till's outbox row and an audit row (the same
// channel_order_actions mechanism as the webshop actions): no network call, no
// status change, no stock movement. The order moves when the cloud's answer or
// a channel_order_update is applied, so the UI shows "sent / not sent yet /
// refused" from pending_action / refused, never a made-up status.
//
// Who may act: every signed-in role on the MAIN till (accept, reject,
// preparing, ready, collected, prep_time: floor work like the kitchen
// screen's Ready tap; the auth middleware has already required a session).
// Any other till answers 403 main_till_required. Availability and busy mode
// need "stock_management". Reads are open to every signed-in role on every
// till. Every POST is same-origin only.

const maxDeliveryBody = 4 << 10

// deliveryDoneWindow is how long a finished order stays on the board.
const deliveryDoneWindow = 2 * time.Hour

type deliveryErr struct {
	Status int
	Code   string
}

func writeDeliveryJSON(w http.ResponseWriter, status int, data any, e *deliveryErr) {
	var errv any
	if e != nil {
		errv = map[string]string{"code": e.Code}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": errv})
}

func deliveryFail(w http.ResponseWriter, status int, code string) {
	writeDeliveryJSON(w, status, nil, &deliveryErr{Status: status, Code: code})
}

// decodeDeliveryBody reads one JSON object, size-capped, unknown fields and
// trailing data refused.
func decodeDeliveryBody(r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDeliveryBody+1))
	if err != nil || len(raw) == 0 || len(raw) > maxDeliveryBody {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil {
		return false
	}
	var extra json.RawMessage
	return errors.Is(dec.Decode(&extra), io.EOF) // nothing may follow the object
}

type deliveryLineView struct {
	Name      string                     `json:"name"`
	Qty       int64                      `json:"qty"`
	Note      string                     `json:"note,omitempty"`
	Modifiers []deliveryLineModifierView `json:"modifiers,omitempty"`
	Unmatched bool                       `json:"unmatched,omitempty"`
}

type deliveryLineModifierView struct {
	Name string `json:"name"`
	Qty  int64  `json:"qty"`
}

type deliveryRefusedView struct {
	Action string `json:"action"`
	Code   string `json:"code"`
}

// deliveryOrderView is one order as the UI draws it. By design it has no
// prices, totals, fees or tip (the kitchen and the inbox show none) and no
// customer data (the order has none).
type deliveryOrderView struct {
	ID               string               `json:"id"`
	OrderNumber      string               `json:"order_number"`
	PluginKey        string               `json:"plugin_key"`
	ConnectionID     string               `json:"connection_id"`
	Status           string               `json:"status"`
	Service          string               `json:"service"`
	PlacedAt         string               `json:"placed_at"`
	AcceptBy         string               `json:"accept_by,omitempty"`
	ScheduledStartAt string               `json:"scheduled_start_at,omitempty"`
	PrepMinutes      int64                `json:"prep_minutes"`
	PickupCode       string               `json:"pickup_code,omitempty"`
	Note             string               `json:"note,omitempty"`
	LateCancel       bool                 `json:"late_cancel"`
	RejectReason     string               `json:"reject_reason,omitempty"`
	StatusChangedAt  string               `json:"status_changed_at,omitempty"`
	Revision         int64                `json:"revision"`
	Lines            []deliveryLineView   `json:"lines"`
	PendingAction    string               `json:"pending_action,omitempty"`
	Refused          *deliveryRefusedView `json:"refused,omitempty"`
	AllowedActions   []string             `json:"allowed_actions"`
}

func deliveryViewOf(ctx context.Context, d *common.Deps, o data.ChannelOrder, canAct bool) (deliveryOrderView, error) {
	v := deliveryOrderView{
		ID: o.ID, OrderNumber: o.OrderNumber, PluginKey: o.PluginKey, ConnectionID: o.ConnectionID, Status: o.Status,
		Service: o.Service, PlacedAt: o.PlacedAt, AcceptBy: o.AcceptBy, ScheduledStartAt: o.ScheduledStartAt,
		PrepMinutes: o.PrepMinutes, PickupCode: o.PickupCode, Note: o.Note, LateCancel: o.LateCancel,
		RejectReason: o.RejectReason, StatusChangedAt: o.StatusChangedAt, Revision: o.Revision,
		Lines: []deliveryLineView{}, AllowedActions: []string{},
	}
	for _, l := range o.Lines {
		lv := deliveryLineView{Name: l.Name, Qty: l.Qty, Note: l.Note, Unmatched: l.Unmatched}
		for _, m := range l.Modifiers {
			lv.Modifiers = append(lv.Modifiers, deliveryLineModifierView{Name: m.Name, Qty: m.Qty})
		}
		v.Lines = append(v.Lines, lv)
	}
	rows, err := data.NewChannelOrderActionsRepo(d.Db).ForOrder(ctx, o.ID)
	if err != nil {
		return v, err
	}
	var pending []string
	for _, a := range rows {
		switch a.State {
		case data.ChannelActionPending:
			pending = append(pending, a.Action)
			v.PendingAction = a.Action
		case data.ChannelActionRefused:
			v.Refused = &deliveryRefusedView{Action: a.Action, Code: data.SanitizeActionCode(a.ResultCode)}
		}
	}
	if canAct {
		v.AllowedActions = data.DeliveryOrderAllowedActions(o.Status, o.FulfilmentRank, pending)
		if v.AllowedActions == nil {
			v.AllowedActions = []string{}
		}
	}
	return v, nil
}

// deliveryMainTill reports whether this is the main till (no primary).
func deliveryMainTill(ctx context.Context, d *common.Deps) bool {
	v, _, _ := d.Settings.Get(ctx, "sync.primary_url")
	return strings.TrimSpace(v) == ""
}

// registerDeliveryAPI adds the delivery state-machine routes.
//
//	GET  /api/delivery/orders                    the board: open orders + finished within 2 h
//	GET  /api/delivery/summary                   the chip: waiting count, nearest accept_by, flags
//	POST /api/delivery/orders/{id}/actions       one tap: accept | reject | preparing | ready | collected | prep_time
//	GET  /api/delivery/service                   store service state (open | paused, prep minutes)
//	POST /api/delivery/service                   set it (stock_management)
//	POST /api/delivery/availability              item 86 / clear (stock_management)
func registerDeliveryAPI(mux *http.ServeMux, d *common.Deps) {
	orders := data.NewChannelOrdersRepo(d.Db)
	actions := data.NewChannelOrderActionsRepo(d.Db)
	avail := data.NewDeliveryAvailabilityRepo(d.Db)

	mux.HandleFunc("GET /api/delivery/orders", func(w http.ResponseWriter, r *http.Request) {
		list, err := orders.ListDelivery(r.Context(), time.Now().Add(-deliveryDoneWindow).UTC().Format(time.RFC3339), 100)
		if err != nil {
			logging.L().Warnf("delivery api: list failed: %v", err)
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		canAct := channelActionsHere(r.Context(), d)
		blocked, _, _ := actions.Blocked(r.Context())
		views := make([]deliveryOrderView, 0, len(list))
		for _, o := range list {
			v, err := deliveryViewOf(r.Context(), d, o, canAct && blocked == 0)
			if err != nil {
				deliveryFail(w, http.StatusInternalServerError, "load_failed")
				return
			}
			views = append(views, v)
		}
		writeDeliveryJSON(w, http.StatusOK, map[string]any{
			"orders": views, "main_till": deliveryMainTill(r.Context(), d), "blocked_actions": blocked,
			"now": time.Now().UTC().Format(time.RFC3339),
		}, nil)
	})

	mux.HandleFunc("GET /api/delivery/summary", func(w http.ResponseWriter, r *http.Request) {
		n, nearest, err := orders.DeliveryWaiting(r.Context())
		if err != nil {
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		blocked, _, _ := actions.Blocked(r.Context())
		unsent := 0
		if has, _ := actions.HasSendable(r.Context()); has {
			unsent = 1
		}
		writeDeliveryJSON(w, http.StatusOK, map[string]any{
			"waiting": n, "nearest_accept_by": nearest, "main_till": deliveryMainTill(r.Context(), d),
			"blocked_actions": blocked, "unsent_actions": unsent, "now": time.Now().UTC().Format(time.RFC3339),
		}, nil)
	})

	mux.HandleFunc("POST /api/delivery/orders/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			deliveryFail(w, http.StatusForbidden, "cross_origin")
			return
		}
		if !deliveryMainTill(r.Context(), d) {
			deliveryFail(w, http.StatusForbidden, "main_till_required")
			return
		}
		if !channelActionsHere(r.Context(), d) {
			deliveryFail(w, http.StatusConflict, "cloud_link_required")
			return
		}
		var body struct {
			Action      string `json:"action"`
			Reason      string `json:"reason"`
			PrepMinutes int64  `json:"prep_minutes"`
		}
		if !decodeDeliveryBody(r, &body) {
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		switch body.Action {
		case "accept", "reject", "preparing", "ready", "collected", "prep_time":
		default:
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		id := r.PathValue("id")
		o, found, err := orders.Get(r.Context(), id)
		if err != nil {
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		if !found || o.Kind != data.ChannelKindDelivery {
			deliveryFail(w, http.StatusNotFound, "order_not_found")
			return
		}
		in := data.ChannelOrderActionInput{OrderID: id, Action: body.Action, StaffID: actorIDFor(r), Now: time.Now(),
			RejectReason: body.Reason, PrepMinutes: body.PrepMinutes}
		actionID, dup, err := actions.Enqueue(r.Context(), in)
		switch {
		case errors.Is(err, data.ErrChannelActionNotAllowed):
			// Shape errors (a reason on accept, no reason on reject, minutes out of range)
			// are the caller's bug: 400. A step the order cannot take now is 409.
			if deliveryShapeBad(in) {
				deliveryFail(w, http.StatusBadRequest, "invalid_request")
			} else {
				deliveryFail(w, http.StatusConflict, "invalid_transition")
			}
			return
		case errors.Is(err, data.ErrChannelActionsBlocked):
			deliveryFail(w, http.StatusConflict, "actions_blocked")
			return
		case errors.Is(err, data.ErrChannelOrderNotFound):
			deliveryFail(w, http.StatusNotFound, "order_not_found")
			return
		case err != nil:
			logging.L().Warnf("delivery api: action %s on %s not saved: %v", body.Action, id, err)
			deliveryFail(w, http.StatusInternalServerError, "save_failed")
			return
		}
		// An online till does not wait for the next tick (non-blocking, nil-safe).
		select {
		case d.CloudSyncNow <- struct{}{}:
		default:
		}
		fresh, _, err := orders.Get(r.Context(), id)
		if err != nil {
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		view, err := deliveryViewOf(r.Context(), d, fresh, true)
		if err != nil {
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		writeDeliveryJSON(w, http.StatusOK, map[string]any{"action_id": actionID, "duplicate": dup, "order": view}, nil)
	})

	mux.HandleFunc("GET /api/delivery/service", func(w http.ResponseWriter, r *http.Request) {
		st, err := avail.Service(r.Context())
		if err != nil {
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		writeDeliveryJSON(w, http.StatusOK, serviceJSON(st), nil)
	})

	mux.HandleFunc("POST /api/delivery/service", func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			deliveryFail(w, http.StatusForbidden, "cross_origin")
			return
		}
		if !canPerform(d, r, "stock_management") {
			deliveryFail(w, http.StatusForbidden, "permission_required")
			return
		}
		if !deliveryMainTill(r.Context(), d) {
			deliveryFail(w, http.StatusForbidden, "main_till_required")
			return
		}
		var body struct {
			State       string `json:"state"`
			Reason      string `json:"reason"`
			Until       string `json:"until"`
			PrepMinutes int64  `json:"prep_minutes"`
		}
		if !decodeDeliveryBody(r, &body) {
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		st := data.ServiceState{State: body.State, Reason: body.Reason, Until: body.Until, PrepMinutes: body.PrepMinutes}
		if err := avail.SetService(r.Context(), st, actorIDFor(r), time.Now()); err != nil {
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		select {
		case d.CloudSyncNow <- struct{}{}:
		default:
		}
		writeDeliveryJSON(w, http.StatusOK, serviceJSON(st), nil)
	})

	mux.HandleFunc("GET /api/delivery/availability", func(w http.ResponseWriter, r *http.Request) {
		snap, err := avail.Snapshot(r.Context(), time.Now())
		if err != nil {
			deliveryFail(w, http.StatusInternalServerError, "load_failed")
			return
		}
		items := make([]map[string]string, 0, len(snap.Unavailable))
		for _, u := range snap.Unavailable {
			items = append(items, map[string]string{"item_id": u.ItemID, "sku": u.SKU, "unavailable_until": u.Until})
		}
		writeDeliveryJSON(w, http.StatusOK, map[string]any{"unavailable": items, "service": serviceJSON(snap.Service)}, nil)
	})

	mux.HandleFunc("POST /api/delivery/availability", func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			deliveryFail(w, http.StatusForbidden, "cross_origin")
			return
		}
		if !canPerform(d, r, "stock_management") {
			deliveryFail(w, http.StatusForbidden, "permission_required")
			return
		}
		if !deliveryMainTill(r.Context(), d) {
			deliveryFail(w, http.StatusForbidden, "main_till_required")
			return
		}
		var body struct {
			ItemID string `json:"item_id"`
			Mode   string `json:"mode"` // today | until | indefinite | clear
			Until  string `json:"until"`
		}
		if !decodeDeliveryBody(r, &body) || body.ItemID == "" {
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		now := time.Now()
		var err error
		value := ""
		switch body.Mode {
		case "clear":
			err = avail.ClearItemUnavailable(r.Context(), body.ItemID, actorIDFor(r), now)
		case "indefinite":
			value = data.UnavailableIndefinitely
			err = avail.SetItemUnavailable(r.Context(), body.ItemID, time.Time{}, actorIDFor(r), now)
		case "today":
			// Until the end of the shop's local day.
			y, m, dd := now.Date()
			end := time.Date(y, m, dd+1, 0, 0, 0, 0, now.Location())
			value = end.UTC().Format(time.RFC3339)
			err = avail.SetItemUnavailable(r.Context(), body.ItemID, end, actorIDFor(r), now)
		case "until":
			t, perr := time.Parse(time.RFC3339, body.Until)
			if perr != nil {
				deliveryFail(w, http.StatusBadRequest, "invalid_request")
				return
			}
			value = t.UTC().Format(time.RFC3339)
			err = avail.SetItemUnavailable(r.Context(), body.ItemID, t, actorIDFor(r), now)
		default:
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				deliveryFail(w, http.StatusNotFound, "item_not_found")
				return
			}
			deliveryFail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		select {
		case d.CloudSyncNow <- struct{}{}:
		default:
		}
		writeDeliveryJSON(w, http.StatusOK, map[string]any{"item_id": body.ItemID, "unavailable_until": value}, nil)
	})
}

func serviceJSON(st data.ServiceState) map[string]any {
	return map[string]any{"state": st.State, "reason": st.Reason, "until": st.Until, "prep_minutes": st.PrepMinutes}
}

// deliveryShapeBad reports a request whose data does not fit its action (as
// opposed to a valid request the order's state cannot take).
func deliveryShapeBad(in data.ChannelOrderActionInput) bool {
	okMinutes := in.PrepMinutes >= 1 && in.PrepMinutes <= data.DeliveryMaxPrepMinutes
	switch in.Action {
	case "reject":
		return !data.ValidDeliveryRejectReason(in.RejectReason) || in.PrepMinutes != 0
	case "prep_time":
		return !okMinutes || in.RejectReason != ""
	case "accept":
		return in.RejectReason != "" || (in.PrepMinutes != 0 && !okMinutes)
	default:
		return in.RejectReason != "" || in.PrepMinutes != 0
	}
}
