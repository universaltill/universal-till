package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/money"
)

// Channel orders (ADR-0169 §9, ut-docs#4768, migration 090): an order taken
// at a sales channel and queued to the main till as a `channel_order`
// directive. The till records it and lowers stock with a `channel_sale`
// movement. It is NOT a till sale: nothing here touches sales, payments, the
// drawer, the Z report or the fiscal journal. No customer data is stored.

// ChannelSaleMovementType is the stock_movements.type of a channel order.
const ChannelSaleMovementType = "channel_sale"

// Delivery orders (ADR-0215, ut-docs#6581): a channel order of kind
// "delivery" carries the delivery object below and walks new -> accepted |
// rejected -> preparing -> ready -> collected (+ cancelled / refund facts).
const (
	// ChannelKindWebshop is every order without a delivery object.
	ChannelKindWebshop = "webshop"
	// ChannelKindDelivery is an order whose payload carried `delivery`.
	ChannelKindDelivery = "delivery"
	// DeliveryUnmappedReject (the default) rejects a delivery order with a
	// line this till cannot match; DeliveryUnmappedPlaceholder records it and
	// flags the line for a manager.
	DeliveryUnmappedReject      = "reject"
	DeliveryUnmappedPlaceholder = "placeholder"
	// DeliveryRejectUnmapped is channel_orders.reject_reason of an order the
	// till rejected itself at intake; the reason sent to the platform is
	// DeliveryRejectReasonItemUnavailable.
	DeliveryRejectUnmapped = "unmapped_line"
	// DeliveryUnmappedPolicyKey is the settings key of the policy above.
	DeliveryUnmappedPolicyKey           = "delivery.unmapped_policy"
	DeliveryRejectReasonItemUnavailable = "item_unavailable"
	// DeliveryRejectWithdrawn is channel_orders.reject_reason of a live order
	// the till moved to a terminal state itself because the customer's data
	// was erased (legal review R8); staff see it as "order withdrawn".
	DeliveryRejectWithdrawn = "withdrawn"
)

// ChannelDeliveryFee is an informational customer-side fee (outside total_minor).
type ChannelDeliveryFee struct {
	Kind   string      `json:"kind"`
	Amount money.Money `json:"amount_minor"`
}

// ChannelLineModifier is one chosen option of a delivery line (display and
// kitchen ticket only; never priced into the till's totals).
type ChannelLineModifier struct {
	ExternalID string      `json:"external_id"`
	Name       string      `json:"name"`
	Qty        int64       `json:"qty"`
	PriceDelta money.Money `json:"price_delta_minor"`
}

// ChannelDeliveryInput is the contract's optional `delivery` object. Times are
// UTC RFC 3339 strings as received ("" when absent). Tip and fees are
// informational, outside total_minor. No customer data.
type ChannelDeliveryInput struct {
	Service          string
	AcceptBy         string
	ScheduledStartAt string
	ConfirmAt        string
	PrepMinutes      int64
	CourierRef       string
	PickupCode       string
	Tip              money.Money
	Fees             []ChannelDeliveryFee
}

// ChannelOrderLineInput is one line of a channel_order(_update) directive
// (reference/contracts/channel-order-v1.json). CloudItemID and CloudVariantID
// are the cloud catalogue ids, which on a cloud-master store are this till's
// item and variant ids (ADR-0162); SKU is the fallback; a line with neither
// ("" SKU, no ids) is unlinked and shown by Name. Discount is for the whole
// line. RefundedQuantity is cumulative.
type ChannelOrderLineInput struct {
	LineID            string
	CloudItemID       string
	CloudVariantID    string
	SKU               string
	Name              string
	Qty               int64
	UnitPrice         money.Money
	TaxBP             int64
	TaxIncluded       bool
	Discount          money.Money
	RefundedQuantity  int64
	RestockedQuantity int64
	// Delivery lines only: the line note and chosen options.
	Note      string
	Modifiers []ChannelLineModifier
}

// ChannelOrderInput is a decoded, validated directive payload. Revision is
// the cloud-issued ordering key.
type ChannelOrderInput struct {
	DirectiveID         string
	ConnectionID        string
	PluginKey           string
	ExternalOrderID     string
	ExternalVersion     string
	Revision            int64
	OrderNumber         string
	PlacedAt            string
	Currency            string
	Status              string
	PaymentState        string
	Shipping            money.Money
	ShippingTaxBP       int64
	ShippingTaxIncluded bool
	OrderDiscount       money.Money
	TaxAdded            money.Money
	RefundTotal         money.Money
	Total               money.Money
	CustomerCountry     string
	CurrencyMismatch    bool
	// TotalMismatch: total_minor disagrees with the lines (the decoder's
	// finding); recorded as received and shown, never refused.
	TotalMismatch bool
	// LocationID is the default stock location, used for a slot with no
	// inventory row yet (the caller resolves the shop's default); ActorID is
	// the audit actor.
	LocationID string
	ActorID    string
	Lines      []ChannelOrderLineInput
	// Delivery is non-nil for a delivery order; Note is the customer's
	// instruction text (never logged). UnmappedPolicy is the till setting
	// (DeliveryUnmapped*; "" = reject) the caller reads.
	Delivery       *ChannelDeliveryInput
	Note           string
	UnmappedPolicy string
}

// ChannelNegativeStock is a line whose sale movement left on-hand stock below zero.
type ChannelNegativeStock struct {
	LineID string
	SKU    string
	Name   string // the line's name, for a line with no SKU
	ItemID string
	OnHand float64
}

// ChannelOrderResult is the outcome of Apply. Duplicate is true when the
// revision is not higher than the one already applied: nothing is written
// and the existing OrderID is returned.
type ChannelOrderResult struct {
	OrderID       string
	Duplicate     bool
	Unmatched     []string
	NegativeStock []ChannelNegativeStock
	// Moves are the stock movements the order booked, for the caller to
	// publish as stock.adjusted once the transaction has committed.
	Moves []ChannelStockMove
	// AutoRejected: a delivery order with an unmatched line under the reject
	// policy was recorded without moving stock and a `reject` action queued.
	AutoRejected bool
	// LateCancel: the platform cancelled a delivery order at or after
	// preparing (food already made); the caller tells a manager.
	LateCancel bool
}

// ChannelStockMove is one booked movement: Delta is signed (negative for a
// channel_sale, positive for a channel_return).
type ChannelStockMove struct {
	ItemID, VariantID, SKU, LocationID string
	Delta                              float64
	Return                             bool
}

// ChannelOrderLine is a stored line.
type ChannelOrderLine struct {
	LineID            string
	ItemID            string
	VariantID         string
	SKU               string
	Name              string
	Qty               int64
	UnitPrice         money.Money
	TaxBP             int64
	TaxIncluded       bool
	Discount          money.Money
	RefundedQuantity  int64
	RestockedQuantity int64
	Unmatched         bool
	Note              string
	Modifiers         []ChannelLineModifier
}

// ChannelOrder is a stored order. LineCount is set by List and Get; Lines by Get.
type ChannelOrder struct {
	ID               string
	ConnectionID     string
	PluginKey        string
	ExternalOrderID  string
	ExternalVersion  string
	Revision         int64
	OrderNumber      string
	PlacedAt         string
	Currency         string
	Status           string
	PaymentState     string
	Shipping         money.Money
	OrderDiscount    money.Money
	TaxAdded         money.Money
	RefundTotal      money.Money
	Total            money.Money
	CustomerCountry  string
	CurrencyMismatch bool
	TotalMismatch    bool
	CreatedAt        string
	LineCount        int
	Lines            []ChannelOrderLine
	// FulfilmentRank is the furthest step reached (new 0 .. shipped 4); it
	// survives a partial refund. PendingAction is the newest queued action not
	// yet answered ("" when none) and RefusedAction says an answered refusal
	// is waiting to be dismissed (ut-docs#4768 slice D).
	FulfilmentRank int
	PendingAction  string
	RefusedAction  bool
	// Delivery fields (ADR-0215; zero for a webshop order). Kind is
	// ChannelKindWebshop or ChannelKindDelivery. AcceptBy is the platform's
	// deadline; the cloud rejects earlier (act_by), so a UI counts down to
	// the cloud's figure when it has one.
	Kind             string
	Service          string
	AcceptBy         string
	ScheduledStartAt string
	ConfirmAt        string
	PrepMinutes      int64
	CourierRef       string
	PickupCode       string
	Tip              money.Money
	Fees             []ChannelDeliveryFee
	Note             string
	LateCancel       bool
	RejectReason     string
	StatusChangedAt  string
}

// LineDiscountTotal is the sum of the lines' discount_line_minor (each is for
// the whole line, all units). Zero when Lines is not loaded (List). Shown on
// the order page so the lines add up to the total (ut-docs#4945).
func (o ChannelOrder) LineDiscountTotal() money.Money {
	var sum money.Money
	for _, l := range o.Lines {
		sum = sum.Add(l.Discount)
	}
	return sum
}

// ItemsSubtotal is the lines' quantity x unit price added up, before any
// discount. Zero when Lines is not loaded (ut-docs#4945).
func (o ChannelOrder) ItemsSubtotal() money.Money {
	var sum money.Money
	for _, l := range o.Lines {
		sum = sum.Add(l.UnitPrice.Scale(l.Qty))
	}
	return sum
}

// ChannelOrdersRepo owns the channel_orders and channel_order_lines tables.
type ChannelOrdersRepo struct{ db *sql.DB }

func NewChannelOrdersRepo(db *sql.DB) *ChannelOrdersRepo { return &ChannelOrdersRepo{db: db} }

type storedChannelLine struct {
	rowID             string
	itemID, variantID string
	locationID        string
	qtyApplied        int64
	restockedApplied  int64
}

// Apply records a channel_order or channel_order_update in ONE transaction.
// The order key is (connection, external order id); the revision is the only
// ordering key: higher is applied, equal or lower is ignored (Duplicate).
// A first sight of an order, whatever its revision, records it as new.
// Stock, per matched tracked line, moves by
// -(qty - qty_applied) + (restocked_quantity - restocked_applied), where the
// applied values are what earlier revisions already moved (0 for an order the
// till never saw, so a first delivery that is already refunded lands right):
// the sale part is a `channel_sale` movement, the restock part a
// `channel_return`. Lines never disappear from a payload (units leave an order
// only as refunded_quantity), so qty only grows. A line that matches no item (or an item with variants but no
// variant named) is stored unmatched with no movement and matched again on
// the next revision.
// Stock going negative is applied and reported, never refused (ADR-0036).
func (r *ChannelOrdersRepo) Apply(ctx context.Context, in ChannelOrderInput) (ChannelOrderResult, error) {
	var res ChannelOrderResult
	if err := validateChannelOrder(in); err != nil {
		return res, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	status := in.Status
	if status == "" {
		status = "new"
	}
	var storedRev int64
	var storedStatus, storedKind, storedReject, storedErased, storedNote string
	var storedRank int
	err = tx.QueryRowContext(ctx, `SELECT id, revision, order_kind, reject_reason, note_erased_at, note FROM channel_orders WHERE connection_id = ? AND external_order_id = ?`,
		in.ConnectionID, in.ExternalOrderID).Scan(&res.OrderID, &storedRev, &storedKind, &storedReject, &storedErased, &storedNote)
	isNew := errors.Is(err, sql.ErrNoRows)
	switch {
	case err != nil && !isNew:
		return res, fmt.Errorf("look up channel order: %w", err)
	case !isNew && in.Revision <= storedRev:
		return ChannelOrderResult{OrderID: res.OrderID, Duplicate: true}, nil
	}
	if storedErased != "" {
		// The note was erased on this till (retention or redact): a later
		// revision still carrying the text must never bring it back.
		in.Note = ""
		lines := make([]ChannelOrderLineInput, len(in.Lines))
		copy(lines, in.Lines)
		for i := range lines {
			lines[i].Note = ""
		}
		in.Lines = lines
	}
	isDelivery := in.Delivery != nil || storedKind == ChannelKindDelivery
	// A note-free revision is NOT a redact by itself: the cloud queues every
	// staff-action revision (Accept, Ready, Collected) with no note. For an order
	// that is live before and after the update the kitchen keeps the notes it is
	// cooking from; only a revision that ends the order blanks them (below).
	keptNotes := false
	var storedLineNotes int
	if !isNew && isDelivery && storedErased == "" && in.Note == "" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_order_lines WHERE order_id = ? AND note <> ''`, res.OrderID).Scan(&storedLineNotes); err != nil {
			return res, fmt.Errorf("read stored line notes: %w", err)
		}
	}
	if !isNew && isDelivery && storedErased == "" && in.Note == "" && (storedNote != "" || storedLineNotes > 0) &&
		!IsDeliveryTerminal(in.Status) && storedReject == "" {
		var storedStatusNow string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM channel_orders WHERE id = ?`, res.OrderID).Scan(&storedStatusNow); err != nil {
			return res, fmt.Errorf("read channel order status: %w", err)
		}
		if !IsDeliveryTerminal(storedStatusNow) {
			in.Note = storedNote
			lines := make([]ChannelOrderLineInput, len(in.Lines))
			copy(lines, in.Lines)
			for i := range lines {
				if lines[i].Note != "" {
					continue
				}
				// Same line id AND same item: ids are positional on some
				// platforms, so a renumbered line must not inherit a note.
				var ln string
				if err := tx.QueryRowContext(ctx, `SELECT note FROM channel_order_lines WHERE order_id = ? AND line_id = ? AND name = ? AND sku = ?`,
					res.OrderID, lines[i].LineID, lines[i].Name, lines[i].SKU).Scan(&ln); err == nil {
					lines[i].Note = ln
				}
			}
			in.Lines = lines
			keptNotes = true
		}
	}
	kind := ChannelKindWebshop
	if isDelivery {
		kind = ChannelKindDelivery
	}
	var d ChannelDeliveryInput
	if in.Delivery != nil {
		d = *in.Delivery
	}
	feesJSON := ""
	if len(d.Fees) > 0 {
		b, err := json.Marshal(d.Fees)
		if err != nil {
			return res, fmt.Errorf("encode delivery fees: %w", err)
		}
		feesJSON = string(b)
	}

	// The unmapped-line policy (ADR-0215 decision 2): a delivery order with a
	// line this till cannot match is rejected at intake under the default
	// policy, before any stock moves.
	rejectReason := storedReject
	if isNew && isDelivery && in.UnmappedPolicy != DeliveryUnmappedPlaceholder {
		for _, l := range in.Lines {
			_, _, _, matched, merr := r.matchLine(ctx, tx, l)
			if merr != nil {
				return res, merr
			}
			if !matched {
				rejectReason = DeliveryRejectUnmapped
				res.AutoRejected = true
				break
			}
		}
	}

	nextStatus, nextRank := status, 0
	if isNew {
		res.OrderID = uuid.NewString()
		nextRank = chainRankFor(isDelivery, status)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_orders (id, connection_id, plugin_key, external_order_id, external_version, revision, last_directive_id,
  order_number, placed_at, currency, status, payment_state, shipping_minor, shipping_tax_bp, shipping_tax_included,
  order_discount_minor, tax_added_minor, refund_total_minor, total_minor, customer_country, currency_mismatch, total_mismatch,
  fulfilment_rank, created_at, updated_at,
  order_kind, service, accept_by, scheduled_start_at, confirm_at, prep_minutes, courier_ref, pickup_code, tip_minor, fees_json,
  note, reject_reason, status_changed_at, note_present, note_chars)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			res.OrderID, in.ConnectionID, in.PluginKey, in.ExternalOrderID, in.ExternalVersion, in.Revision, in.DirectiveID,
			in.OrderNumber, in.PlacedAt, in.Currency, status, in.PaymentState, in.Shipping.Minor(), in.ShippingTaxBP,
			boolInt(in.ShippingTaxIncluded), in.OrderDiscount.Minor(), in.TaxAdded.Minor(), in.RefundTotal.Minor(),
			in.Total.Minor(), in.CustomerCountry, boolInt(in.CurrencyMismatch), boolInt(in.TotalMismatch),
			nextRank, now, now,
			kind, d.Service, d.AcceptBy, d.ScheduledStartAt, d.ConfirmAt, d.PrepMinutes, d.CourierRef, d.PickupCode,
			d.Tip.Minor(), feesJSON, in.Note, rejectReason, now, boolInt(in.Note != ""), len([]rune(in.Note))); err != nil {
			return res, fmt.Errorf("insert channel order: %w", err)
		}
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT status, fulfilment_rank FROM channel_orders WHERE id = ?`, res.OrderID).Scan(&storedStatus, &storedRank); err != nil {
			return res, fmt.Errorf("read channel order status: %w", err)
		}
		if isDelivery {
			nextStatus, nextRank = nextDeliveryStatus(storedStatus, status, storedRank)
			res.LateCancel = nextStatus == "cancelled" && storedStatus != "cancelled" && nextRank >= deliveryRank["preparing"]
		} else {
			nextStatus, nextRank = nextChannelStatus(storedStatus, status, storedRank)
		}
		changedAt := "status_changed_at"
		args := []any{}
		if nextStatus != storedStatus {
			changedAt = "?"
			args = append(args, now)
		}
		args = append(args, in.ExternalVersion, in.Revision, in.DirectiveID, in.OrderNumber, nextStatus, in.PaymentState,
			in.Shipping.Minor(), in.ShippingTaxBP, boolInt(in.ShippingTaxIncluded), in.OrderDiscount.Minor(),
			in.TaxAdded.Minor(), in.RefundTotal.Minor(), in.Total.Minor(), in.CustomerCountry, boolInt(in.CurrencyMismatch),
			boolInt(in.TotalMismatch), nextRank, now,
			kind, d.Service, d.AcceptBy, d.ScheduledStartAt, d.ConfirmAt, d.PrepMinutes, d.CourierRef, d.PickupCode,
			d.Tip.Minor(), feesJSON, in.Note, boolInt(res.LateCancel), boolInt(in.Note != ""), len([]rune(in.Note)), res.OrderID)
		if _, err := tx.ExecContext(ctx, `
UPDATE channel_orders SET status_changed_at=`+changedAt+`, external_version=?, revision=?, last_directive_id=?, order_number=?, status=?, payment_state=?,
  shipping_minor=?, shipping_tax_bp=?, shipping_tax_included=?, order_discount_minor=?, tax_added_minor=?,
  refund_total_minor=?, total_minor=?, customer_country=?, currency_mismatch=?, total_mismatch=?, fulfilment_rank=?, updated_at=?,
  order_kind=?, service=?, accept_by=?, scheduled_start_at=?, confirm_at=?, prep_minutes=?, courier_ref=?, pickup_code=?,
  tip_minor=?, fees_json=?, note=?, late_cancel=MAX(late_cancel, ?),
  note_present=MAX(note_present, ?), note_chars=MAX(note_chars, ?) WHERE id=?`, args...); err != nil {
			return res, fmt.Errorf("update channel order: %w", err)
		}
	}
	// The retention clock starts once, when the order first reaches a terminal
	// state (or the till rejected it at intake); a later refund fact never
	// restarts it.
	if isDelivery && (IsDeliveryTerminal(nextStatus) || rejectReason != "") {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_orders SET terminal_at = ? WHERE id = ? AND terminal_at = ''`, now, res.OrderID); err != nil {
			return res, fmt.Errorf("mark channel order terminal: %w", err)
		}
	}
	// The cloud's update for an order that ended carries no note where the till
	// held one (a redact, or a plain staff or platform end: no marker tells them
	// apart). The update above already blanked the order note and the lines are
	// written with their own (empty) notes: stamp the audit columns. The order
	// status is whatever Apply recorded; nothing here relabels it.
	if !isNew && isDelivery && !keptNotes && (storedNote != "" || storedLineNotes > 0) && in.Note == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_orders SET note_erased_at = ?, note_erased_by = ? WHERE id = ? AND note_erased_at = ''`, now, NoteErasedByCloudEnd, res.OrderID); err != nil {
			return res, fmt.Errorf("mark note erased: %w", err)
		}
	}
	// A delivery order the till rejected, or the platform rejected, never
	// stays out of stock: nothing is moved for the first, everything applied
	// is put back for the second.
	noMove := rejectReason != ""
	putAllBack := isDelivery && nextStatus == "rejected"

	pos := NewPOSRepo(r.db)
	for _, l := range in.Lines {
		var st storedChannelLine
		var itemID, variantID, locID sql.NullString
		lerr := tx.QueryRowContext(ctx, `SELECT id, item_id, variant_id, location_id, qty_applied, restocked_applied
FROM channel_order_lines WHERE order_id = ? AND line_id = ?`, res.OrderID, l.LineID).
			Scan(&st.rowID, &itemID, &variantID, &locID, &st.qtyApplied, &st.restockedApplied)
		lineIsNew := errors.Is(lerr, sql.ErrNoRows)
		if lerr != nil && !lineIsNew {
			return res, fmt.Errorf("look up channel order line: %w", lerr)
		}
		st.itemID, st.variantID, st.locationID = itemID.String, variantID.String, locID.String
		modsJSON := ""
		if len(l.Modifiers) > 0 {
			b, merr := json.Marshal(l.Modifiers)
			if merr != nil {
				return res, fmt.Errorf("encode line modifiers: %w", merr)
			}
			modsJSON = string(b)
		}

		// A line already moved keeps the slot it moved, whatever happened to
		// the variant since (deactivated, re-SKUed): its restock must land
		// where the sale left. Anything else is matched (again) now.
		var itemID2, variantID2 string
		var tracked, matched bool
		if st.qtyApplied > 0 && st.itemID != "" {
			itemID2, variantID2 = st.itemID, st.variantID
			var untracked int
			switch e := tx.QueryRowContext(ctx, `SELECT stock_untracked FROM items WHERE id = ?`, st.itemID).Scan(&untracked); {
			case e == nil:
				tracked, matched = untracked == 0, true
			case !errors.Is(e, sql.ErrNoRows):
				return res, fmt.Errorf("read stored channel line item: %w", e)
			}
		} else if itemID2, variantID2, tracked, matched, err = r.matchLine(ctx, tx, l); err != nil {
			return res, err
		}
		if !matched {
			res.Unmatched = append(res.Unmatched, l.LineID)
		}
		loc := st.locationID
		if loc == "" {
			loc = in.LocationID
			if matched {
				if best, ok := r.bestLocation(ctx, tx, itemID2, variantID2); ok {
					loc = best
				}
			}
		}
		qtyApplied, restockedApplied := st.qtyApplied, st.restockedApplied
		// Delivery: no movement while the till's own reject stands, and a
		// rejected order that never booked stock books nothing; one that did
		// gets every unit put back (restock target = qty).
		restockTarget := l.RestockedQuantity
		skip := noMove || (putAllBack && st.qtyApplied == 0)
		if putAllBack && st.qtyApplied > 0 {
			restockTarget = st.qtyApplied
		}
		if matched && !skip {
			_, neg, err := r.moveStock(ctx, tx, pos, in, loc, itemID2, variantID2, tracked,
				-(l.Qty - st.qtyApplied), restockTarget-st.restockedApplied, l.SKU, &res.Moves)
			if err != nil {
				return res, err
			}
			qtyApplied, restockedApplied = l.Qty, restockTarget
			if neg != nil {
				neg.LineID, neg.SKU, neg.Name, neg.ItemID = l.LineID, l.SKU, l.Name, itemID2
				res.NegativeStock = append(res.NegativeStock, *neg)
			}
		}
		locVal := any(nil)
		if st.locationID != "" || (matched && tracked) {
			locVal = loc
		}
		if lineIsNew {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_order_lines (id, order_id, line_id, item_id, variant_id, location_id, sku, name, qty, unit_price_minor,
  tax_bp, tax_included, discount_line_minor, refunded_quantity, restocked_quantity, unmatched, qty_applied, restocked_applied,
  note, modifiers_json)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				uuid.NewString(), res.OrderID, l.LineID, nullIfEmpty(itemID2), nullIfEmpty(variantID2), locVal, l.SKU, l.Name, l.Qty,
				l.UnitPrice.Minor(), l.TaxBP, boolInt(l.TaxIncluded), l.Discount.Minor(), l.RefundedQuantity,
				l.RestockedQuantity, boolInt(!matched), qtyApplied, restockedApplied, l.Note, modsJSON); err != nil {
				return res, fmt.Errorf("insert channel order line: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `
UPDATE channel_order_lines SET item_id=COALESCE(?, item_id), variant_id=COALESCE(?, variant_id), location_id=COALESCE(location_id, ?), sku=?, name=?, qty=?, unit_price_minor=?,
  tax_bp=?, tax_included=?, discount_line_minor=?, refunded_quantity=?, restocked_quantity=?, unmatched=?, qty_applied=?, restocked_applied=?,
  note=?, modifiers_json=?
WHERE id=?`,
			nullIfEmpty(itemID2), nullIfEmpty(variantID2), locVal, l.SKU, l.Name, l.Qty, l.UnitPrice.Minor(), l.TaxBP,
			boolInt(l.TaxIncluded), l.Discount.Minor(), l.RefundedQuantity, l.RestockedQuantity, boolInt(!matched),
			qtyApplied, restockedApplied, l.Note, modsJSON, st.rowID); err != nil {
			return res, fmt.Errorf("update channel order line: %w", err)
		}
	}

	// The till's own reject at intake: queue it once, in this transaction, as
	// a system action (no staff id) so it replays like a staff tap.
	if res.AutoRejected {
		if _, err := insertChannelActionTx(ctx, tx, channelActionRow{
			OrderID: res.OrderID, ConnectionID: in.ConnectionID, ExternalOrderID: in.ExternalOrderID, Action: "reject",
			BaseRevision: in.Revision, StaffID: "", Now: now, RejectReason: DeliveryRejectReasonItemUnavailable,
		}); err != nil {
			return res, err
		}
	}

	if err := tx.Commit(); err != nil {
		return ChannelOrderResult{}, fmt.Errorf("commit channel order: %w", err)
	}
	return res, nil
}

func setSlot(mv *StockMovementInput, itemID, variantID string) {
	if variantID != "" {
		mv.VariantID = variantID
	} else {
		mv.ItemID = itemID
	}
}

// bestLocation is the active location holding the most of a slot: a line is sold
// from where its stock is.
func (r *ChannelOrdersRepo) bestLocation(ctx context.Context, tx *sql.Tx, itemID, variantID string) (string, bool) {
	var best string
	err := tx.QueryRowContext(ctx, `SELECT i.location_id FROM inventory i
JOIN stock_locations sl ON sl.id = i.location_id AND sl.is_active = 1
WHERE (i.item_id = ? AND i.variant_id IS NULL) OR (i.variant_id = ? AND i.item_id IS NULL)
ORDER BY i.quantity DESC, i.rowid LIMIT 1`, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&best)
	return best, err == nil
}

// moveStock books the two parts of a line's stock change as movements:
// saleDelta is -(qty_new - qty_last) (negative: units sold; positive: units
// the channel removed from the order) and returnDelta is
// restocked_new - restocked_last (positive: units back on the shelf).
// Nothing moves for an untracked item. It reports a non-nil
// ChannelNegativeStock when the slot ends below zero (applied, never refused).
func (r *ChannelOrdersRepo) moveStock(ctx context.Context, tx *sql.Tx, pos *POSRepo, in ChannelOrderInput, loc, itemID, variantID string, tracked bool, saleDelta, returnDelta int64, sku string, moves *[]ChannelStockMove) (bool, *ChannelNegativeStock, error) {
	if !tracked || (saleDelta == 0 && returnDelta == 0) {
		return false, nil, nil
	}
	book := func(delta int64) error {
		if delta == 0 {
			return nil
		}
		typ := "channel_return"
		if delta < 0 {
			typ = ChannelSaleMovementType
		}
		mv := StockMovementInput{LocationID: loc, Type: typ, Quantity: float64(delta),
			Reason: "channel order " + in.OrderNumber, ActorID: in.ActorID}
		setSlot(&mv, itemID, variantID)
		if _, err := pos.RecordStockMovement(ctx, tx, mv); err != nil {
			return err
		}
		*moves = append(*moves, ChannelStockMove{ItemID: itemID, VariantID: variantID, SKU: sku, LocationID: loc,
			Delta: float64(delta), Return: delta > 0})
		return nil
	}
	if err := book(saleDelta); err != nil {
		return false, nil, err
	}
	if err := book(returnDelta); err != nil {
		return false, nil, err
	}
	var onHand float64
	if err := tx.QueryRowContext(ctx, `SELECT quantity FROM inventory WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))`,
		loc, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&onHand); err != nil {
		return true, nil, fmt.Errorf("read on-hand: %w", err)
	}
	if onHand < 0 && (saleDelta < 0 || returnDelta < 0) {
		return true, &ChannelNegativeStock{OnHand: onHand}, nil
	}
	return true, nil, nil
}

func validateChannelOrder(in ChannelOrderInput) error {
	switch {
	case strings.TrimSpace(in.DirectiveID) == "":
		return errors.New("channel order: directive id required")
	case strings.TrimSpace(in.ConnectionID) == "":
		return errors.New("channel order: connection id required")
	case strings.TrimSpace(in.ExternalOrderID) == "":
		return errors.New("channel order: external order id required")
	case in.Revision < 1:
		return errors.New("channel order: revision must be at least 1")
	case in.LocationID == "":
		return errors.New("channel order: stock location required")
	case len(in.Lines) == 0:
		return errors.New("channel order: at least one line required")
	}
	for _, l := range in.Lines {
		if strings.TrimSpace(l.LineID) == "" {
			return errors.New("channel order: line id required")
		}
		if l.Qty <= 0 {
			return fmt.Errorf("channel order: line %s quantity must be above zero", l.LineID)
		}
		if l.RefundedQuantity < 0 || l.RefundedQuantity > l.Qty {
			return fmt.Errorf("channel order: line %s refunded quantity out of range", l.LineID)
		}
		if l.RestockedQuantity < 0 || l.RestockedQuantity > l.RefundedQuantity {
			return fmt.Errorf("channel order: line %s restocked quantity out of range", l.LineID)
		}
	}
	return nil
}

// matchLine resolves a line to a stocked slot: the named variant, else the
// named item, else the SKU (variant SKU first). An item with active variants
// needs a variant (ADR-0043), so it is unmatched when none resolves.
func (r *ChannelOrdersRepo) matchLine(ctx context.Context, tx *sql.Tx, l ChannelOrderLineInput) (itemID, variantID string, tracked, matched bool, err error) {
	findVariant := func(q string, arg string) (bool, error) {
		e := tx.QueryRowContext(ctx, q, arg).Scan(&variantID, &itemID)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		return e == nil, e
	}
	findItem := func(q string, arg string) (bool, error) {
		e := tx.QueryRowContext(ctx, q, arg).Scan(&itemID)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		return e == nil, e
	}
	const byVariantID = `SELECT id, item_id FROM item_variants WHERE id = ? AND is_active = 1`
	const byVariantSKU = `SELECT id, item_id FROM item_variants WHERE sku = ? AND is_active = 1`
	const byItemID = `SELECT id FROM items WHERE id = ?`
	const byItemSKU = `SELECT id FROM items WHERE sku = ?`

	found := false
	if l.CloudVariantID != "" {
		if found, err = findVariant(byVariantID, l.CloudVariantID); err != nil {
			return
		}
	}
	if !found && l.SKU != "" {
		if found, err = findVariant(byVariantSKU, l.SKU); err != nil {
			return
		}
	}
	if !found {
		variantID, itemID = "", ""
		if l.CloudItemID != "" {
			if found, err = findItem(byItemID, l.CloudItemID); err != nil {
				return
			}
		}
		if !found && l.SKU != "" {
			if found, err = findItem(byItemSKU, l.SKU); err != nil {
				return
			}
		}
	}
	if !found {
		return "", "", false, false, nil
	}
	var untracked int
	if err = tx.QueryRowContext(ctx, `SELECT stock_untracked FROM items WHERE id = ?`, itemID).Scan(&untracked); err != nil {
		return
	}
	if variantID == "" {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_variants WHERE item_id = ? AND is_active = 1`, itemID).Scan(&n); err != nil {
			return
		}
		if n > 0 {
			return itemID, "", false, false, nil
		}
	}
	return itemID, variantID, untracked == 0, true, nil
}

// List returns the newest orders first (by placed_at), with line counts.
func (r *ChannelOrdersRepo) List(ctx context.Context, limit int) ([]ChannelOrder, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, channelOrderSelect+` ORDER BY o.placed_at DESC, o.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list channel orders: %w", err)
	}
	defer rows.Close()
	var out []ChannelOrder
	for rows.Next() {
		o, err := scanChannelOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeliveryOpenStatuses are the delivery statuses still being worked.
var DeliveryOpenStatuses = []string{"new", "accepted", "preparing", "ready"}

// ListDelivery returns the delivery orders the inbox board shows, with their
// lines: every order still being worked, plus any that reached a final status
// at or after doneSince (UTC RFC 3339). Soonest accept deadline first among
// the open ones, then the newest. Webshop orders are never listed.
func (r *ChannelOrdersRepo) ListDelivery(ctx context.Context, doneSince string, limit int) ([]ChannelOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, channelOrderSelect+`
WHERE o.order_kind = 'delivery' AND (o.status IN ('new','accepted','preparing','ready') OR o.status_changed_at >= ?)
ORDER BY CASE WHEN o.status IN ('new','accepted','preparing','ready') THEN 0 ELSE 1 END,
  CASE WHEN o.accept_by = '' THEN 1 ELSE 0 END, o.accept_by, o.placed_at DESC LIMIT ?`, doneSince, limit)
	if err != nil {
		return nil, fmt.Errorf("list delivery orders: %w", err)
	}
	var out []ChannelOrder
	for rows.Next() {
		o, err := scanChannelOrder(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		full, ok, err := r.Get(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		if ok {
			out[i].Lines = full.Lines
		}
	}
	return out, nil
}

// DeliveryWaiting counts the delivery orders waiting for staff to accept or
// reject (status new, not already rejected by the till itself) and returns
// the nearest accept deadline ("" when none carries one).
func (r *ChannelOrdersRepo) DeliveryWaiting(ctx context.Context) (count int, nearestAcceptBy string, err error) {
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MIN(CASE WHEN accept_by <> '' THEN accept_by END), '')
FROM channel_orders WHERE order_kind = 'delivery' AND status = 'new' AND reject_reason = ''`).Scan(&count, &nearestAcceptBy)
	return count, nearestAcceptBy, err
}

// channelOrderSelect is the column list of every order read.
const channelOrderSelect = `
SELECT o.id, o.connection_id, o.plugin_key, o.external_order_id, o.external_version, o.revision, o.order_number, o.placed_at,
  o.currency, o.status, o.payment_state, o.shipping_minor, o.order_discount_minor, o.tax_added_minor, o.refund_total_minor,
  o.total_minor, o.customer_country, o.currency_mismatch, o.total_mismatch, o.created_at,
  (SELECT COUNT(*) FROM channel_order_lines l WHERE l.order_id = o.id),
  o.fulfilment_rank,
  COALESCE((SELECT a.action FROM channel_order_actions a WHERE a.order_id = o.id AND a.state = 'pending' ORDER BY a.seq DESC LIMIT 1), ''),
  EXISTS(SELECT 1 FROM channel_order_actions a WHERE a.order_id = o.id AND a.state = 'refused'),
  o.order_kind, o.service, o.accept_by, o.scheduled_start_at, o.confirm_at, o.prep_minutes, o.courier_ref, o.pickup_code,
  o.tip_minor, o.fees_json, o.note, o.late_cancel, o.reject_reason, o.status_changed_at
FROM channel_orders o`

// Get returns one order with its lines.
func (r *ChannelOrdersRepo) Get(ctx context.Context, id string) (ChannelOrder, bool, error) {
	o, err := scanChannelOrder(r.db.QueryRowContext(ctx, channelOrderSelect+` WHERE o.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT line_id, COALESCE(item_id,''), COALESCE(variant_id,''), sku, name, qty, unit_price_minor, tax_bp, tax_included,
  discount_line_minor, refunded_quantity, restocked_quantity, unmatched, note, modifiers_json
FROM channel_order_lines WHERE order_id = ? ORDER BY rowid`, id)
	if err != nil {
		return o, false, fmt.Errorf("channel order lines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l ChannelOrderLine
		var price, disc int64
		var taxInc, un int
		var mods string
		if err := rows.Scan(&l.LineID, &l.ItemID, &l.VariantID, &l.SKU, &l.Name, &l.Qty, &price, &l.TaxBP, &taxInc,
			&disc, &l.RefundedQuantity, &l.RestockedQuantity, &un, &l.Note, &mods); err != nil {
			return o, false, err
		}
		if mods != "" {
			if err := json.Unmarshal([]byte(mods), &l.Modifiers); err != nil {
				return o, false, fmt.Errorf("channel order line modifiers: %w", err)
			}
		}
		l.UnitPrice, l.Discount = money.FromMinor(price), money.FromMinor(disc)
		l.TaxIncluded, l.Unmatched = taxInc != 0, un != 0
		o.Lines = append(o.Lines, l)
	}
	return o, true, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanChannelOrder(s rowScanner) (ChannelOrder, error) {
	var o ChannelOrder
	var ship, disc, tax, refund, total int64
	var mismatch, totalMismatch, refused, late int
	var tip int64
	var fees string
	if err := s.Scan(&o.ID, &o.ConnectionID, &o.PluginKey, &o.ExternalOrderID, &o.ExternalVersion, &o.Revision,
		&o.OrderNumber, &o.PlacedAt, &o.Currency, &o.Status, &o.PaymentState, &ship, &disc, &tax, &refund, &total,
		&o.CustomerCountry, &mismatch, &totalMismatch, &o.CreatedAt, &o.LineCount, &o.FulfilmentRank, &o.PendingAction, &refused,
		&o.Kind, &o.Service, &o.AcceptBy, &o.ScheduledStartAt, &o.ConfirmAt, &o.PrepMinutes, &o.CourierRef, &o.PickupCode,
		&tip, &fees, &o.Note, &late, &o.RejectReason, &o.StatusChangedAt); err != nil {
		return o, err
	}
	o.Tip, o.LateCancel = money.FromMinor(tip), late != 0
	if fees != "" {
		if err := json.Unmarshal([]byte(fees), &o.Fees); err != nil {
			return o, fmt.Errorf("channel order fees: %w", err)
		}
	}
	o.Shipping, o.OrderDiscount, o.TaxAdded = money.FromMinor(ship), money.FromMinor(disc), money.FromMinor(tax)
	o.RefundTotal, o.Total, o.CurrencyMismatch = money.FromMinor(refund), money.FromMinor(total), mismatch != 0
	o.TotalMismatch = totalMismatch != 0
	o.RefusedAction = refused != 0
	return o, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var channelChainRank = map[string]int{"new": 0, "accepted": 1, "picked": 2, "packed": 3, "shipped": 4}

// chainRank is a status's place on the fulfilment chain (0 off it).
func chainRank(status string) int { return channelChainRank[status] }

// nextChannelStatus applies sales-channel-v1.md §5.1 to a stored status:
// along new -> accepted -> picked -> packed -> shipped only forward;
// cancelled and refunded are terminal (a later refund does not turn a
// cancelled order into a refunded one); partially_refunded continues to
// picked, packed or shipped (never back to new or accepted, and never to a
// step at or below the furthest the order reached before the refund,
// maxRank), or becomes refunded or cancelled. Refund and stock fields still
// apply from the channel whatever happens to the status. It returns the new
// status and the furthest fulfilment rank reached, which the caller stores.
func nextChannelStatus(from, to string, maxRank int) (string, int) {
	fr, fromChain := channelChainRank[from]
	if fromChain && fr > maxRank {
		maxRank = fr
	}
	tr, toChain := channelChainRank[to]
	switch from {
	case "cancelled", "refunded":
		return from, maxRank
	case "partially_refunded":
		switch {
		case to == "new" || to == "accepted":
			return from, maxRank
		case toChain && tr <= maxRank:
			return from, maxRank
		case toChain:
			return to, tr
		}
		return to, maxRank
	}
	if fromChain && toChain && tr < fr {
		return from, maxRank
	}
	if toChain && tr > maxRank {
		maxRank = tr
	}
	return to, maxRank
}

// deliveryRank is a delivery status's place on its chain (fulfilment_rank):
// new 0, accepted 1, preparing 2, ready 3, collected 4 (ADR-0215).
var deliveryRank = map[string]int{"new": 0, "accepted": 1, "preparing": 2, "ready": 3, "collected": 4}

// chainRankFor is the stored fulfilment_rank of a status for the order's kind.
func chainRankFor(delivery bool, status string) int {
	if delivery {
		return deliveryRank[status]
	}
	return chainRank(status)
}

// nextDeliveryStatus applies ADR-0215's delivery state machine to a stored
// status (cloud is master; a platform cancel is a fact and never refused):
// along new -> accepted -> preparing -> ready -> collected only forward
// (accepted may skip to ready); rejected, cancelled and refunded are
// terminal; collected is terminal except that a later refund fact is
// recorded; partially_refunded continues forward only. It returns the new
// status and the furthest chain rank reached.
func nextDeliveryStatus(from, to string, maxRank int) (string, int) {
	fr, fromChain := deliveryRank[from]
	if fromChain && fr > maxRank {
		maxRank = fr
	}
	tr, toChain := deliveryRank[to]
	switch from {
	case "rejected", "cancelled", "refunded":
		return from, maxRank
	case "collected":
		if to == "refunded" || to == "partially_refunded" {
			return to, maxRank
		}
		return from, maxRank
	case "partially_refunded":
		if toChain {
			if tr <= maxRank {
				return from, maxRank
			}
			return to, tr
		}
		return to, maxRank
	}
	if toChain {
		if tr < fr {
			return from, maxRank
		}
		if tr > maxRank {
			maxRank = tr
		}
		return to, maxRank
	}
	return to, maxRank
}
