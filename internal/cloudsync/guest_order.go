package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// The guest_order directive (ut-docs#4337, ADR-0159 §4/§5): an order a
// guest placed on the cloud table page (<web base>/t/<guest token>). It is
// the first directive whose content an anonymous party wrote, so the
// payload is untrusted input: this file decodes and bounds it, and the hook
// (pages.cloudGuestOrder) re-resolves every id against the till's own
// database and prices the order from the till's own catalogue. order_type
// and any price field in the payload are ignored, as is any unknown field.
//
// Main-till only (mainTillOnlyTypes). At most maxGuestOrdersPerTick are
// applied per check-in; the rest stay pending (no result post) and the
// cloud delivers them again on the next one.
//
// Results: a refusal is "failed" with exactly one of the three codes below
// as its message; a placed order is "applied" with the result's data
// object {display_no, total_minor, currency}. Every result is stored
// (data.GuestOrderResultsRepo) before it is posted, so a re-delivered
// directive is answered with the first result and never applied twice,
// and a post the cloud did not acknowledge is retried on each later tick
// until the cloud answers 200 or 404 (retryGuestOrderResults).

// The guest_order refusal codes (ADR-0159 §5), the result message of a
// "failed" guest_order — the cloud maps each to the guest page's "please
// order at the counter".
const (
	// GuestOrderUnknownTable: the table token names no enabled table here.
	GuestOrderUnknownTable = "unknown_table"
	// GuestOrderItemUnavailable: an item is missing, inactive, a gift
	// voucher or weighed, or a variant/option is no longer valid for it.
	GuestOrderItemUnavailable = "item_unavailable"
	// GuestOrderBadOrder: the order's shape or bounds, or a modifier
	// group's min/max rule, are violated.
	GuestOrderBadOrder = "bad_order"
)

// Bounds re-checked on the till (ADR-0159 §4, binding). maxGuestOrderIDLen
// covers the cloud's 32-hex id with room to spare; maxGuestOrderRefLen
// bounds every other id the payload carries.
const (
	maxGuestOrderLines      = 50
	maxGuestOrderQty        = 99
	maxGuestOrderOptions    = 20
	maxGuestOrderIDLen      = 64
	maxGuestOrderRefLen     = 128
	maxGuestOrdersPerTick   = 10
	maxGuestResultRetries   = 20
	guestOrderNotSupportedM = "guest_order is not supported on this till"
)

// GuestTablesKey is the device-report key carrying the main till's enabled
// tables as [{token, label}] (ADR-0159 §1). Contributed through
// Hooks.DeviceExtra; buildSyncRequest drops it from a replica's report so
// the cloud — which replaces the set only when a report carries the field
// — never has it cleared by one.
const GuestTablesKey = "guest_tables"

// GuestOrder is a decoded, bounds-checked guest_order payload. Nothing in
// it is trusted beyond its shape: the hook resolves every id itself.
type GuestOrder struct {
	ID          string // the cloud's GuestOrder id
	DirectiveID string
	TableToken  string
	Lines       []GuestOrderLine
}

// GuestOrderLine is one ordered line: an item, an optional variant, an
// integer quantity 1..99 and the chosen modifier option ids.
type GuestOrderLine struct {
	ItemID            string
	VariantID         string
	Qty               int
	ModifierOptionIDs []string
}

// GuestOrderOutcome is the hook's answer. Refusal is one of the codes
// above ("" = placed); on success DisplayNo, TotalMinor (integer minor
// units) and Currency become the result's data object.
type GuestOrderOutcome struct {
	Refusal    string
	DisplayNo  string
	TotalMinor int64
	Currency   string
}

// guestOrderIDOK accepts the cloud's GuestOrder id shape: 1..64 chars of
// [A-Za-z0-9_-] (the cloud mints 32 hex chars).
func guestOrderIDOK(id string) bool {
	if id == "" || len(id) > maxGuestOrderIDLen {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func guestRefOK(s string) bool { return s != "" && len(s) <= maxGuestOrderRefLen }

// decodeGuestOrder decodes and bounds-checks d's payload. The refusal is
// GuestOrderBadOrder for any shape or bounds violation, "" when o is good.
// lines may be a JSON array or a JSON array encoded in a string (the
// cloud's generic payload shape for arrays, see apply's strs).
func decodeGuestOrder(d directive) (o GuestOrder, refusal string) {
	id, _ := d.Payload["guest_order_id"].(string)
	id = strings.TrimSpace(id)
	if !guestOrderIDOK(id) {
		return GuestOrder{}, GuestOrderBadOrder
	}
	token, ok := d.Payload["table_token"].(string)
	token = strings.TrimSpace(token)
	if !ok || !guestRefOK(token) {
		return GuestOrder{}, GuestOrderBadOrder
	}
	var raw []byte
	switch v := d.Payload["lines"].(type) {
	case string:
		raw = []byte(v)
	case []any:
		raw, _ = json.Marshal(v)
	default:
		return GuestOrder{}, GuestOrderBadOrder
	}
	// Each field decodes into a pointer or RawMessage so a missing qty, a
	// string qty or a non-string id fails the order instead of silently
	// becoming a zero value. Unknown fields (prices among them) are ignored.
	var wire []struct {
		ItemID    *string          `json:"item_id"`
		VariantID *string          `json:"variant_id"`
		Qty       *json.RawMessage `json:"qty"`
		Options   []*string        `json:"modifier_option_ids"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return GuestOrder{}, GuestOrderBadOrder
	}
	if len(wire) < 1 || len(wire) > maxGuestOrderLines {
		return GuestOrder{}, GuestOrderBadOrder
	}
	o = GuestOrder{ID: id, DirectiveID: d.ID, TableToken: token, Lines: make([]GuestOrderLine, 0, len(wire))}
	for _, w := range wire {
		if w.ItemID == nil || !guestRefOK(strings.TrimSpace(*w.ItemID)) || w.Qty == nil {
			return GuestOrder{}, GuestOrderBadOrder
		}
		var qty float64
		if err := json.Unmarshal(*w.Qty, &qty); err != nil {
			return GuestOrder{}, GuestOrderBadOrder
		}
		if math.IsNaN(qty) || qty != math.Trunc(qty) || qty < 1 || qty > maxGuestOrderQty {
			return GuestOrder{}, GuestOrderBadOrder
		}
		line := GuestOrderLine{ItemID: strings.TrimSpace(*w.ItemID), Qty: int(qty)}
		if w.VariantID != nil {
			line.VariantID = strings.TrimSpace(*w.VariantID)
			if len(line.VariantID) > maxGuestOrderRefLen {
				return GuestOrder{}, GuestOrderBadOrder
			}
		}
		if len(w.Options) > maxGuestOrderOptions {
			return GuestOrder{}, GuestOrderBadOrder
		}
		seen := make(map[string]bool, len(w.Options))
		for _, p := range w.Options {
			if p == nil {
				return GuestOrder{}, GuestOrderBadOrder
			}
			opt := strings.TrimSpace(*p)
			if !guestRefOK(opt) || seen[opt] {
				return GuestOrder{}, GuestOrderBadOrder
			}
			seen[opt] = true
			line.ModifierOptionIDs = append(line.ModifierOptionIDs, opt)
		}
		o.Lines = append(o.Lines, line)
	}
	return o, ""
}

// guestOrderResult is one guest_order apply's outcome. deferred: the hook
// hit an internal error (a DB read failed) — no result is posted or
// stored, so the directive stays pending and the next tick tries again.
// guestOrderID is "" when the payload carried no usable id (nothing to key
// a stored result by).
type guestOrderResult struct {
	status, msg  string
	data         map[string]any
	deferred     bool
	guestOrderID string
}

// applyGuestOrder decodes d and runs the hook. Shared by apply (its case
// "guest_order") and tick's guest_order path, which also stores and
// retries the result.
func applyGuestOrder(ctx context.Context, d directive, hooks Hooks) guestOrderResult {
	if hooks.GuestOrder == nil {
		id, _ := d.Payload["guest_order_id"].(string)
		r := guestOrderResult{status: "failed", msg: guestOrderNotSupportedM}
		if id = strings.TrimSpace(id); guestOrderIDOK(id) {
			r.guestOrderID = id
		}
		return r
	}
	o, refusal := decodeGuestOrder(d)
	if refusal != "" {
		r := guestOrderResult{status: "failed", msg: refusal}
		if id, _ := d.Payload["guest_order_id"].(string); guestOrderIDOK(strings.TrimSpace(id)) {
			r.guestOrderID = strings.TrimSpace(id)
		}
		return r
	}
	out, err := hooks.GuestOrder(ctx, o)
	if err != nil {
		logging.L().Warnf("cloudsync: guest_order %s left pending (internal error, retried next tick): %v", o.ID, err)
		return guestOrderResult{deferred: true, guestOrderID: o.ID}
	}
	if out.Refusal != "" {
		msg := out.Refusal
		if msg != GuestOrderUnknownTable && msg != GuestOrderItemUnavailable && msg != GuestOrderBadOrder {
			msg = GuestOrderBadOrder
		}
		return guestOrderResult{status: "failed", msg: msg, guestOrderID: o.ID}
	}
	return guestOrderResult{
		status:       "applied",
		msg:          "order " + out.DisplayNo + " placed",
		data:         guestOrderData(out.DisplayNo, out.TotalMinor, out.Currency),
		guestOrderID: o.ID,
	}
}

func guestOrderData(displayNo string, totalMinor int64, currency string) map[string]any {
	return map[string]any{"display_no": displayNo, "total_minor": totalMinor, "currency": currency}
}

// handleGuestOrder is tick's path for one guest_order directive: a result
// already stored for its guest order id is re-posted as it is (a
// re-delivery, never a second apply); otherwise the order is applied, its
// result stored, then posted. An acknowledged post (200, or 404 for an
// order the cloud no longer holds) marks the stored row posted; anything
// else leaves it for retryGuestOrderResults.
func handleGuestOrder(ctx context.Context, cfg *config.Config, db *sql.DB, d directive, hooks Hooks) {
	repo := data.NewGuestOrderResultsRepo(db)
	if id, _ := d.Payload["guest_order_id"].(string); guestOrderIDOK(strings.TrimSpace(id)) {
		stored, ok, err := repo.Get(ctx, strings.TrimSpace(id))
		if err != nil {
			logging.L().Warnf("cloudsync: guest_order %s left pending: read stored result: %v", d.ID, err)
			return
		}
		if ok {
			postStoredGuestOrderResult(ctx, cfg, repo, stored, d.ID)
			return
		}
	}
	r := applyGuestOrder(ctx, d, hooks)
	if r.deferred {
		return
	}
	if r.guestOrderID == "" {
		// No usable id to key a stored result by: today's log-only post.
		if err := postResult(ctx, cfg, d.ID, r.status, r.msg, nil); err != nil {
			logging.L().Warnf("cloudsync: result for %s not delivered: %v", d.ID, err)
		}
		return
	}
	stored := data.GuestOrderResult{GuestOrderID: r.guestOrderID, DirectiveID: d.ID, Status: r.status, Message: r.msg}
	if r.data != nil {
		stored.DisplayNo, _ = r.data["display_no"].(string)
		stored.TotalMinor, _ = r.data["total_minor"].(int64)
		stored.Currency, _ = r.data["currency"].(string)
	}
	if err := repo.Save(ctx, stored); err != nil {
		// The order (if any) is parked and its counter row carries the
		// guest order id, so a re-delivery still finds it; only the retry
		// of an unacknowledged post is lost.
		logging.L().Errorf("cloudsync: guest_order %s result not stored (no retry if this post fails): %v", r.guestOrderID, err)
	}
	postStoredGuestOrderResult(ctx, cfg, repo, stored, d.ID)
}

// postStoredGuestOrderResult posts a stored guest_order result under
// directiveID and marks it posted once the cloud acknowledged it.
func postStoredGuestOrderResult(ctx context.Context, cfg *config.Config, repo *data.GuestOrderResultsRepo, g data.GuestOrderResult, directiveID string) bool {
	var dataObj map[string]any
	if g.Status == "applied" {
		dataObj = guestOrderData(g.DisplayNo, g.TotalMinor, g.Currency)
	}
	err := postResult(ctx, cfg, directiveID, g.Status, g.Message, dataObj)
	var se *statusError
	if err != nil && !(errors.As(err, &se) && guestResultStatusIsFinal(se.StatusCode)) {
		logging.L().Warnf("cloudsync: guest_order %s result not delivered (retried next tick): %v", g.GuestOrderID, err)
		return false
	}
	if err != nil && se.StatusCode == http.StatusNotFound {
		logging.L().Infof("cloudsync: guest_order %s result: the cloud no longer holds directive %s (404); not retried", g.GuestOrderID, directiveID)
	} else if err != nil {
		logging.L().Warnf("cloudsync: guest_order %s result refused by the cloud (%d); not retried: %v", g.GuestOrderID, se.StatusCode, err)
	} else {
		logging.L().Infof("cloudsync: directive %s (guest_order) %s: %s", directiveID, g.Status, g.Message)
	}
	if merr := repo.MarkPosted(ctx, g.GuestOrderID); merr != nil {
		logging.L().Warnf("cloudsync: guest_order %s result delivered but not marked (it will be re-sent): %v", g.GuestOrderID, merr)
	}
	return true
}

// guestResultStatusIsFinal reports whether a non-200 answer to a
// guest_order result post ends its retries. 404 (the cloud no longer holds
// the directive) is the ADR-0159 §4 case; any other 4xx except 401 (a
// credential being rotated), 408 and 429 is a permanent refusal too — retrying it could never succeed, and since
// retries run oldest first and stop at the first failure, one such row
// would otherwise starve every later result (review of ut-docs#4337).
func guestResultStatusIsFinal(code int) bool {
	return code >= 400 && code < 500 && code != http.StatusUnauthorized && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

// retryGuestOrderResults re-posts up to maxGuestResultRetries stored
// guest_order results the cloud has not acknowledged, oldest first. It
// stops at the first post that fails, so an unreachable cloud costs one
// attempt per tick, not one per row. skip holds the guest order ids this
// tick already posted.
func retryGuestOrderResults(ctx context.Context, cfg *config.Config, db *sql.DB, skip map[string]bool) {
	repo := data.NewGuestOrderResultsRepo(db)
	pending, err := repo.ListUnposted(ctx, maxGuestResultRetries)
	if err != nil {
		logging.L().Warnf("cloudsync: guest_order result retry: %v", err)
		return
	}
	for _, g := range pending {
		if skip[g.GuestOrderID] {
			continue
		}
		if !postStoredGuestOrderResult(ctx, cfg, repo, g, g.DirectiveID) {
			return
		}
	}
}
