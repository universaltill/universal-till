package data

import (
	"context"
	"database/sql"
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
	var storedStatus string
	var storedRank int
	err = tx.QueryRowContext(ctx, `SELECT id, revision FROM channel_orders WHERE connection_id = ? AND external_order_id = ?`,
		in.ConnectionID, in.ExternalOrderID).Scan(&res.OrderID, &storedRev)
	isNew := errors.Is(err, sql.ErrNoRows)
	switch {
	case err != nil && !isNew:
		return res, fmt.Errorf("look up channel order: %w", err)
	case !isNew && in.Revision <= storedRev:
		return ChannelOrderResult{OrderID: res.OrderID, Duplicate: true}, nil
	}

	if isNew {
		res.OrderID = uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_orders (id, connection_id, plugin_key, external_order_id, external_version, revision, last_directive_id,
  order_number, placed_at, currency, status, payment_state, shipping_minor, shipping_tax_bp, shipping_tax_included,
  order_discount_minor, tax_added_minor, refund_total_minor, total_minor, customer_country, currency_mismatch, total_mismatch,
  fulfilment_rank, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			res.OrderID, in.ConnectionID, in.PluginKey, in.ExternalOrderID, in.ExternalVersion, in.Revision, in.DirectiveID,
			in.OrderNumber, in.PlacedAt, in.Currency, status, in.PaymentState, in.Shipping.Minor(), in.ShippingTaxBP,
			boolInt(in.ShippingTaxIncluded), in.OrderDiscount.Minor(), in.TaxAdded.Minor(), in.RefundTotal.Minor(),
			in.Total.Minor(), in.CustomerCountry, boolInt(in.CurrencyMismatch), boolInt(in.TotalMismatch),
			chainRank(status), now, now); err != nil {
			return res, fmt.Errorf("insert channel order: %w", err)
		}
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT status, fulfilment_rank FROM channel_orders WHERE id = ?`, res.OrderID).Scan(&storedStatus, &storedRank); err != nil {
			return res, fmt.Errorf("read channel order status: %w", err)
		}
		nextStatus, nextRank := nextChannelStatus(storedStatus, status, storedRank)
		if _, err := tx.ExecContext(ctx, `
UPDATE channel_orders SET external_version=?, revision=?, last_directive_id=?, order_number=?, status=?, payment_state=?,
  shipping_minor=?, shipping_tax_bp=?, shipping_tax_included=?, order_discount_minor=?, tax_added_minor=?,
  refund_total_minor=?, total_minor=?, customer_country=?, currency_mismatch=?, total_mismatch=?, fulfilment_rank=?, updated_at=? WHERE id=?`,
			in.ExternalVersion, in.Revision, in.DirectiveID, in.OrderNumber, nextStatus, in.PaymentState,
			in.Shipping.Minor(), in.ShippingTaxBP, boolInt(in.ShippingTaxIncluded), in.OrderDiscount.Minor(),
			in.TaxAdded.Minor(), in.RefundTotal.Minor(), in.Total.Minor(), in.CustomerCountry, boolInt(in.CurrencyMismatch),
			boolInt(in.TotalMismatch), nextRank, now, res.OrderID); err != nil {
			return res, fmt.Errorf("update channel order: %w", err)
		}
	}

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
		if matched {
			_, neg, err := r.moveStock(ctx, tx, pos, in, loc, itemID2, variantID2, tracked,
				-(l.Qty - st.qtyApplied), l.RestockedQuantity-st.restockedApplied, l.SKU, &res.Moves)
			if err != nil {
				return res, err
			}
			qtyApplied, restockedApplied = l.Qty, l.RestockedQuantity
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
  tax_bp, tax_included, discount_line_minor, refunded_quantity, restocked_quantity, unmatched, qty_applied, restocked_applied)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				uuid.NewString(), res.OrderID, l.LineID, nullIfEmpty(itemID2), nullIfEmpty(variantID2), locVal, l.SKU, l.Name, l.Qty,
				l.UnitPrice.Minor(), l.TaxBP, boolInt(l.TaxIncluded), l.Discount.Minor(), l.RefundedQuantity,
				l.RestockedQuantity, boolInt(!matched), qtyApplied, restockedApplied); err != nil {
				return res, fmt.Errorf("insert channel order line: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `
UPDATE channel_order_lines SET item_id=COALESCE(?, item_id), variant_id=COALESCE(?, variant_id), location_id=COALESCE(location_id, ?), sku=?, name=?, qty=?, unit_price_minor=?,
  tax_bp=?, tax_included=?, discount_line_minor=?, refunded_quantity=?, restocked_quantity=?, unmatched=?, qty_applied=?, restocked_applied=?
WHERE id=?`,
			nullIfEmpty(itemID2), nullIfEmpty(variantID2), locVal, l.SKU, l.Name, l.Qty, l.UnitPrice.Minor(), l.TaxBP,
			boolInt(l.TaxIncluded), l.Discount.Minor(), l.RefundedQuantity, l.RestockedQuantity, boolInt(!matched),
			qtyApplied, restockedApplied, st.rowID); err != nil {
			return res, fmt.Errorf("update channel order line: %w", err)
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

const channelOrderSelect = `
SELECT o.id, o.connection_id, o.plugin_key, o.external_order_id, o.external_version, o.revision, o.order_number, o.placed_at,
  o.currency, o.status, o.payment_state, o.shipping_minor, o.order_discount_minor, o.tax_added_minor, o.refund_total_minor,
  o.total_minor, o.customer_country, o.currency_mismatch, o.total_mismatch, o.created_at,
  (SELECT COUNT(*) FROM channel_order_lines l WHERE l.order_id = o.id)
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
  discount_line_minor, refunded_quantity, restocked_quantity, unmatched
FROM channel_order_lines WHERE order_id = ? ORDER BY rowid`, id)
	if err != nil {
		return o, false, fmt.Errorf("channel order lines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l ChannelOrderLine
		var price, disc int64
		var taxInc, un int
		if err := rows.Scan(&l.LineID, &l.ItemID, &l.VariantID, &l.SKU, &l.Name, &l.Qty, &price, &l.TaxBP, &taxInc,
			&disc, &l.RefundedQuantity, &l.RestockedQuantity, &un); err != nil {
			return o, false, err
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
	var mismatch, totalMismatch int
	if err := s.Scan(&o.ID, &o.ConnectionID, &o.PluginKey, &o.ExternalOrderID, &o.ExternalVersion, &o.Revision,
		&o.OrderNumber, &o.PlacedAt, &o.Currency, &o.Status, &o.PaymentState, &ship, &disc, &tax, &refund, &total,
		&o.CustomerCountry, &mismatch, &totalMismatch, &o.CreatedAt, &o.LineCount); err != nil {
		return o, err
	}
	o.Shipping, o.OrderDiscount, o.TaxAdded = money.FromMinor(ship), money.FromMinor(disc), money.FromMinor(tax)
	o.RefundTotal, o.Total, o.CurrencyMismatch = money.FromMinor(refund), money.FromMinor(total), mismatch != 0
	o.TotalMismatch = totalMismatch != 0
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
