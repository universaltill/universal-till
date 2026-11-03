package data

// SalesMirrorRepo is the ONE place that reads or writes the sales copy
// (mirror_*), its local state (mirror_state) and the main till's change log
// (sales_mirror_log, sales_mirror_acks) — ut-docs#5074, ADR-0011 and
// ADR-0114 "Amendment 2026-10-26", wire contract ut-docs
// reference/contracts/pos-lan-sales-mirror.md.
//
// The copy is never the sales table, and nothing here returns a type any
// report, Z report, stock, voucher, receipt-number, cloud-upload or journal
// function accepts: MirrorSale is its own type. That is how "a copied sale
// never counts" holds by construction. scripts/ci/guard-sales-mirror-isolation.sh
// fails CI when any other non-test file names one of these tables.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Settings keys (design §3.3). sales_mirror.* are shop-wide (the admin
// bundle carries them from the main till); sync.sales_mirror_off is per till
// (the "sync." family of PerTillSettingPrefixes).
const (
	SalesMirrorEnabledKey    = "sales_mirror.enabled"
	SalesMirrorEpochKey      = "sales_mirror.epoch"
	SalesMirrorWindowDaysKey = "sales_mirror.window_days"
	SalesMirrorMaxMBKey      = "sales_mirror.max_mb"
	SalesMirrorLogMaxDaysKey = "sales_mirror.log_max_days"
	SalesMirrorLogMaxRowsKey = "sales_mirror.log_max_rows"
	SalesMirrorTillOffKey    = "sync.sales_mirror_off"
)

// Defaults and bounds (design §6, D1, D6).
const (
	SalesMirrorDefaultWindowDays = 400
	SalesMirrorMinWindowDays     = 31
	SalesMirrorMaxWindowDays     = 3660
	SalesMirrorDefaultMaxMB      = 1024
	SalesMirrorDefaultLogMaxDays = 30
	SalesMirrorDefaultLogMaxRows = 250000
	// salesMirrorPruneChunk bounds one prune transaction (design §11) so the
	// write lock a sale may be waiting on is held only briefly.
	salesMirrorPruneChunk = 5000
)

// Change-log ops.
const (
	MirrorOpSale  = "sale"
	MirrorOpClose = "close"
)

// MirrorSale is one copied sale on the wire and in the copy (contract
// "Record shapes / Sale"). Amounts are integer minor units: this is a
// transport DTO, never fed to money arithmetic on this till.
type MirrorSale struct {
	ID                      string               `json:"id"`
	OriginTillID            string               `json:"origin_till_id"`
	OriginTillName          string               `json:"origin_till_name"`
	ReceiptNo               string               `json:"receipt_no"`
	DisplayNo               *string              `json:"display_no"`
	Status                  string               `json:"status"`
	SaleType                string               `json:"sale_type"`
	TenderType              string               `json:"tender_type"`
	OrderType               string               `json:"order_type"`
	RegisterID              *string              `json:"register_id"`
	CashierID               *string              `json:"cashier_id"`
	CustomerID              *string              `json:"customer_id"`
	TableID                 *string              `json:"table_id"`
	Currency                string               `json:"currency"`
	Subtotal                int64                `json:"subtotal"`
	DiscountTotal           int64                `json:"discount_total"`
	TaxTotal                int64                `json:"tax_total"`
	Total                   int64                `json:"total"`
	Rounding                int64                `json:"rounding"`
	ServiceChargeAmount     int64                `json:"service_charge_amount"`
	ServiceChargeTaxBasisBP int64                `json:"service_charge_tax_basis_bp"`
	VoucherIssueTotal       int64                `json:"voucher_issue_total"`
	Note                    *string              `json:"note"`
	Offline                 bool                 `json:"offline"`
	CreatedAt               string               `json:"created_at"`
	CompletedAt             *string              `json:"completed_at"`
	VoidedAt                *string              `json:"voided_at"`
	LocalDate               string               `json:"local_date"`
	VoidedLocalDate         *string              `json:"voided_local_date"`
	Lines                   []MirrorSaleLine     `json:"lines"`
	Payments                []MirrorPayment      `json:"payments"`
	Charges                 []MirrorSaleCharge   `json:"charges"`
	Discounts               []MirrorSaleDiscount `json:"discounts"`
	Links                   []MirrorSaleLink     `json:"links"`
}

// MirrorSaleLine is one copied sale line.
type MirrorSaleLine struct {
	ID              string               `json:"id"`
	LineNo          int64                `json:"line_no"`
	ItemID          *string              `json:"item_id"`
	VariantID       *string              `json:"variant_id"`
	NameSnapshot    string               `json:"name_snapshot"`
	SKUSnapshot     *string              `json:"sku_snapshot"`
	BarcodeSnapshot *string              `json:"barcode_snapshot"`
	Quantity        float64              `json:"quantity"`
	UnitPrice       int64                `json:"unit_price"`
	LineDiscount    int64                `json:"line_discount"`
	TaxRateBP       int64                `json:"tax_rate_bp"`
	TaxAmount       int64                `json:"tax_amount"`
	TotalBeforeTax  int64                `json:"total_before_tax"`
	TotalAfterTax   int64                `json:"total_after_tax"`
	OrderType       string               `json:"order_type"`
	RefundOfLineID  *string              `json:"refund_of_line_id"`
	Modifiers       []MirrorLineModifier `json:"modifiers"`
}

// MirrorLineModifier is one copied line modifier.
type MirrorLineModifier struct {
	ID                 string  `json:"id"`
	GroupID            *string `json:"group_id"`
	OptionID           *string `json:"option_id"`
	GroupNameSnapshot  string  `json:"group_name_snapshot"`
	OptionNameSnapshot string  `json:"option_name_snapshot"`
	PriceDeltaMinor    int64   `json:"price_delta_minor"`
}

// MirrorPayment is one copied payment. MaskedPAN is scheme + last 4 only
// (ADR-0127, pos.validateMaskedPAN on the selling till).
type MirrorPayment struct {
	ID           string  `json:"id"`
	MethodID     string  `json:"method_id"`
	Amount       int64   `json:"amount"`
	Currency     string  `json:"currency"`
	ChangeGiven  int64   `json:"change_given"`
	TipAmount    int64   `json:"tip_amount"`
	TipRecipient string  `json:"tip_recipient"`
	PaidAt       string  `json:"paid_at"`
	LocalDate    string  `json:"local_date"`
	VoucherID    *string `json:"voucher_id"`
	Reference    *string `json:"reference"`
	MaskedPAN    *string `json:"masked_pan"`
	AuthCode     *string `json:"auth_code"`
	TerminalID   *string `json:"terminal_id"`
	TraceID      *string `json:"trace_id"`
}

// MirrorSaleCharge is one copied additive charge.
type MirrorSaleCharge struct {
	Seq         int64  `json:"seq"`
	Key         string `json:"key"`
	Label       string `json:"label"`
	AmountMinor int64  `json:"amount_minor"`
	TaxBasisBP  int64  `json:"tax_basis_bp"`
	Base        string `json:"base"`
}

// MirrorSaleDiscount is one copied discount.
type MirrorSaleDiscount struct {
	ID     string  `json:"id"`
	LineID *string `json:"line_id"`
	Type   string  `json:"type"`
	Value  int64   `json:"value"`
	Amount int64   `json:"amount"`
	Reason *string `json:"reason"`
}

// MirrorSaleLink is one copied refund link.
type MirrorSaleLink struct {
	ID             string  `json:"id"`
	OriginalSaleID string  `json:"original_sale_id"`
	Reason         *string `json:"reason"`
}

// MirrorClose is one copied day close (contract "Record shapes / Close").
// ContentJSON is the archived report byte for byte, as a string.
type MirrorClose struct {
	ID             string  `json:"id"`
	OriginTillID   string  `json:"origin_till_id"`
	OriginTillName string  `json:"origin_till_name"`
	Kind           string  `json:"kind"`
	Period         string  `json:"period"`
	ZNumber        *int64  `json:"z_number"`
	PrevZNumber    *int64  `json:"prev_z_number"`
	PrevClosedAt   *string `json:"prev_closed_at"`
	FirstReceipt   *string `json:"first_receipt"`
	LastReceipt    *string `json:"last_receipt"`
	ContentJSON    string  `json:"content_json"`
	CreatedAt      string  `json:"created_at"`
}

// MirrorRecord is one feed/frame record: a sale, a close, or a "gone"
// pointer (the main till no longer holds the record; deletes never
// propagate, so the receiver keeps what it has).
type MirrorRecord struct {
	Seq   int64        `json:"seq"`
	Op    string       `json:"op"`
	Sale  *MirrorSale  `json:"sale,omitempty"`
	Close *MirrorClose `json:"close,omitempty"`
	ID    string       `json:"id,omitempty"`
	Gone  bool         `json:"gone,omitempty"`
}

// SalesMirrorRepo reads and writes the sales copy and the change log.
type SalesMirrorRepo struct{ db *sql.DB }

// NewSalesMirrorRepo returns the repo over db.
func NewSalesMirrorRepo(db *sql.DB) *SalesMirrorRepo { return &SalesMirrorRepo{db: db} }

// ---------------------------------------------------------------------
// Main till: the change log, acknowledgements, pruning.
// ---------------------------------------------------------------------

// MirrorLogEntry is one change-log row.
type MirrorLogEntry struct {
	Seq          int64
	Op           string
	RefID        string
	OriginTillID string
	LoggedAt     string
}

// LogHead is the highest seq ever logged (sqlite_sequence survives a prune
// that empties the table), 0 when nothing was ever logged.
func (r *SalesMirrorRepo) LogHead(ctx context.Context) (int64, error) {
	var head sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'sales_mirror_log'`).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sales mirror log head: %w", err)
	}
	return head.Int64, nil
}

// LogHorizon is the highest pruned seq: a cursor below it cannot continue.
// With entries it is MIN(seq)-1; an empty log has pruned (or never logged)
// everything up to the head.
func (r *SalesMirrorRepo) LogHorizon(ctx context.Context) (int64, error) {
	var lo sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT MIN(seq) FROM sales_mirror_log`).Scan(&lo); err != nil {
		return 0, fmt.Errorf("sales mirror log horizon: %w", err)
	}
	if lo.Valid {
		return lo.Int64 - 1, nil
	}
	return r.LogHead(ctx)
}

// ReadLog returns up to limit entries with seq > after, in seq order.
func (r *SalesMirrorRepo) ReadLog(ctx context.Context, after int64, limit int) ([]MirrorLogEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT seq, op, ref_id, origin_till_id, logged_at FROM sales_mirror_log WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("sales mirror read log: %w", err)
	}
	defer rows.Close()
	var out []MirrorLogEntry
	for rows.Next() {
		var e MirrorLogEntry
		if err := rows.Scan(&e.Seq, &e.Op, &e.RefID, &e.OriginTillID, &e.LoggedAt); err != nil {
			return nil, fmt.Errorf("sales mirror scan log: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LogCount is the number of change-log rows.
func (r *SalesMirrorRepo) LogCount(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales_mirror_log`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sales mirror log count: %w", err)
	}
	return n, nil
}

// MirrorAck is one additional till's acknowledgement.
type MirrorAck struct {
	TillID   string
	Epoch    string
	AckedSeq int64
	AckedAt  string
}

// UpsertAck records that tillID holds everything up to seq in epoch. An ack
// never moves backwards within an epoch (a stale, reordered request).
func (r *SalesMirrorRepo) UpsertAck(ctx context.Context, tillID, epoch string, seq int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO sales_mirror_acks (till_id, epoch, acked_seq, acked_at) VALUES (?, ?, ?, ?)
ON CONFLICT (till_id) DO UPDATE SET
  acked_seq = CASE WHEN sales_mirror_acks.epoch = excluded.epoch AND sales_mirror_acks.acked_seq > excluded.acked_seq
                   THEN sales_mirror_acks.acked_seq ELSE excluded.acked_seq END,
  epoch = excluded.epoch, acked_at = excluded.acked_at`,
		tillID, epoch, seq, at.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("sales mirror ack %s: %w", tillID, err)
	}
	return nil
}

// Acks lists every acknowledgement (diagnostics).
func (r *SalesMirrorRepo) Acks(ctx context.Context) ([]MirrorAck, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT till_id, epoch, acked_seq, acked_at FROM sales_mirror_acks ORDER BY till_id`)
	if err != nil {
		return nil, fmt.Errorf("sales mirror acks: %w", err)
	}
	defer rows.Close()
	var out []MirrorAck
	for rows.Next() {
		var a MirrorAck
		if err := rows.Scan(&a.TillID, &a.Epoch, &a.AckedSeq, &a.AckedAt); err != nil {
			return nil, fmt.Errorf("sales mirror scan ack: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MirrorLogPruneOpts are the prune job's inputs (design §4.3).
type MirrorLogPruneOpts struct {
	Epoch   string
	MaxDays int
	MaxRows int
	Now     time.Time
}

// MirrorLogPruneResult says what a prune did.
type MirrorLogPruneResult struct {
	AckHorizon int64 // MIN(ack) over registered tills, or the head with none
	Deleted    int64
	// CapPassed lists tills whose acknowledgement a cap overrode: an entry
	// they still needed was deleted, so they will get 410 and re-sync by
	// state. The caller logs a WARN per till.
	CapPassed []string
}

// PruneLog deletes change-log entries every registered till has
// acknowledged, then enforces the hard caps. "Registered" = an ack row in
// the current epoch whose till is still enrolled and not a satellite. With
// none, everything is pruned (a later first pull re-syncs by state).
func (r *SalesMirrorRepo) PruneLog(ctx context.Context, o MirrorLogPruneOpts) (MirrorLogPruneResult, error) {
	var res MirrorLogPruneResult
	// No ack row has an empty epoch, so an empty one would prune the whole
	// log and send every till into a state re-sync (M1 review finding 3).
	if strings.TrimSpace(o.Epoch) == "" {
		return res, errors.New("sales mirror prune: no epoch")
	}
	var minAck sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `
SELECT MIN(a.acked_seq) FROM sales_mirror_acks a
JOIN tills t ON t.id = a.till_id AND t.role <> ?
WHERE a.epoch = ?`, TillRoleSatellite, o.Epoch).Scan(&minAck); err != nil {
		return res, fmt.Errorf("sales mirror prune: min ack: %w", err)
	}
	horizon := minAck.Int64
	if !minAck.Valid {
		h, err := r.LogHead(ctx)
		if err != nil {
			return res, err
		}
		horizon = h
	}
	res.AckHorizon = horizon
	n, err := r.deleteLogUpTo(ctx, horizon)
	res.Deleted += n
	if err != nil {
		return res, err
	}
	// Caps: oldest by age, then by count. Each only when still over.
	var capSeq int64
	if o.MaxDays > 0 {
		cutoff := o.Now.UTC().AddDate(0, 0, -o.MaxDays).Format("2006-01-02T15:04:05Z")
		var s sql.NullInt64
		if err := r.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM sales_mirror_log WHERE logged_at < ?`, cutoff).Scan(&s); err != nil {
			return res, fmt.Errorf("sales mirror prune: age cap: %w", err)
		}
		if s.Valid && s.Int64 > capSeq {
			capSeq = s.Int64
		}
	}
	if o.MaxRows > 0 {
		var cnt int64
		var hi sql.NullInt64
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(seq) FROM sales_mirror_log`).Scan(&cnt, &hi); err != nil {
			return res, fmt.Errorf("sales mirror prune: row cap: %w", err)
		}
		if cnt > int64(o.MaxRows) && hi.Valid {
			if s := hi.Int64 - int64(o.MaxRows); s > capSeq {
				capSeq = s
			}
		}
	}
	if capSeq > horizon {
		rows, err := r.db.QueryContext(ctx, `
SELECT a.till_id FROM sales_mirror_acks a
JOIN tills t ON t.id = a.till_id AND t.role <> ?
WHERE a.epoch = ? AND a.acked_seq < ? ORDER BY a.till_id`, TillRoleSatellite, o.Epoch, capSeq)
		if err != nil {
			return res, fmt.Errorf("sales mirror prune: cap tills: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return res, err
			}
			res.CapPassed = append(res.CapPassed, id)
		}
		rows.Close()
		n, err := r.deleteLogUpTo(ctx, capSeq)
		res.Deleted += n
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func (r *SalesMirrorRepo) deleteLogUpTo(ctx context.Context, seq int64) (int64, error) {
	var total int64
	for {
		res, err := r.db.ExecContext(ctx, `
DELETE FROM sales_mirror_log WHERE seq IN (
  SELECT seq FROM sales_mirror_log WHERE seq <= ? ORDER BY seq LIMIT ?)`, seq, salesMirrorPruneChunk)
		if err != nil {
			return total, fmt.Errorf("sales mirror prune: delete: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < salesMirrorPruneChunk {
			return total, nil
		}
	}
}

// ---------------------------------------------------------------------
// Main till: building feed records from the live tables (design §11).
// ---------------------------------------------------------------------

// mirrorInChunk bounds one IN (...) list well under SQLite's variable limit.
const mirrorInChunk = 500

// MirrorSource names the main till for records it serves: its own sales
// (sales.till_id = ”) and closes carry MainTillID / MainTillName.
type MirrorSource struct {
	MainTillID   string
	MainTillName string
}

func mirrorPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func mirrorArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// eachMirrorChunk calls fn for consecutive slices of ids of at most
// mirrorInChunk entries.
func eachMirrorChunk(ids []string, fn func([]string) error) error {
	for start := 0; start < len(ids); start += mirrorInChunk {
		end := min(start+mirrorInChunk, len(ids))
		if err := fn(ids[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// The serve-side reads, one per table, each over an IN list of ids (%s).
// TestSalesMirrorServeReadsUseIndexes pins that none scans its table.
const (
	mirrorLoadSalesSQL = `
SELECT s.id, s.till_id, COALESCE(t.name, ''), s.receipt_no, s.display_no, s.status, s.sale_type, s.tender_type,
  s.order_type, s.register_id, s.cashier_id, s.customer_id, s.table_id, s.currency, s.subtotal, s.discount_total,
  s.tax_total, s.total, s.rounding, s.service_charge_amount, s.service_charge_tax_basis_bp, s.voucher_issue_total,
  s.note, s.offline, s.created_at, s.completed_at, s.voided_at, s.local_date, s.voided_local_date
FROM sales s LEFT JOIN tills t ON t.id = s.till_id
WHERE s.id IN (%s) AND s.status IN ('completed', 'voided')`
	mirrorLoadLinesSQL = `
SELECT sale_id, id, line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price,
  line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type, refund_of_line_id
FROM sale_lines WHERE sale_id IN (%s) ORDER BY sale_id, line_no`
	mirrorLoadModifiersSQL = `
SELECT sale_line_id, id, group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor
FROM sale_line_modifiers WHERE sale_line_id IN (%s) ORDER BY sale_line_id, id`
	mirrorLoadPaymentsSQL = `
SELECT sale_id, id, method_id, amount, currency, change_given, tip_amount, tip_recipient, paid_at, local_date,
  voucher_id, reference, masked_pan, auth_code, terminal_id, trace_id
FROM payments WHERE sale_id IN (%s) ORDER BY sale_id, paid_at, id`
	mirrorLoadChargesSQL = `
SELECT sale_id, seq, key, label, amount_minor, tax_basis_bp, base
FROM sale_charges WHERE sale_id IN (%s) ORDER BY sale_id, seq`
	mirrorLoadDiscountsSQL = `
SELECT sale_id, id, line_id, type, value, amount, reason
FROM sale_discounts WHERE sale_id IN (%s) ORDER BY sale_id, id`
	mirrorLoadLinksSQL = `
SELECT sale_id, id, original_sale_id, reason
FROM sale_links WHERE sale_id IN (%s) ORDER BY sale_id, id`
	mirrorLoadClosesSQL = `
SELECT id, kind, period, z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt, content_json, created_at
FROM report_archive WHERE id IN (%s)`
)

// LoadMirrorSales reads the CURRENT state of the given sales (completed or
// voided only) with every child, in batched reads: one query per table per
// chunk of 500 ids, never one per sale. Ids not found (moved by a history
// reset, or not completed/voided) are absent from the map.
func (r *SalesMirrorRepo) LoadMirrorSales(ctx context.Context, ids []string, src MirrorSource) (map[string]*MirrorSale, error) {
	out := make(map[string]*MirrorSale, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	err := eachMirrorChunk(ids, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadSalesSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load sales: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			s := &MirrorSale{}
			var tillID, tillName string
			var offline int64
			if err := rows.Scan(&s.ID, &tillID, &tillName, &s.ReceiptNo, &s.DisplayNo, &s.Status, &s.SaleType, &s.TenderType,
				&s.OrderType, &s.RegisterID, &s.CashierID, &s.CustomerID, &s.TableID, &s.Currency, &s.Subtotal, &s.DiscountTotal,
				&s.TaxTotal, &s.Total, &s.Rounding, &s.ServiceChargeAmount, &s.ServiceChargeTaxBasisBP, &s.VoucherIssueTotal,
				&s.Note, &offline, &s.CreatedAt, &s.CompletedAt, &s.VoidedAt, &s.LocalDate, &s.VoidedLocalDate); err != nil {
				return fmt.Errorf("sales mirror: scan sale: %w", err)
			}
			s.Offline = offline != 0
			if tillID == "" {
				s.OriginTillID, s.OriginTillName = src.MainTillID, src.MainTillName
			} else {
				s.OriginTillID, s.OriginTillName = tillID, tillName
			}
			s.Lines, s.Payments, s.Charges, s.Discounts, s.Links = []MirrorSaleLine{}, []MirrorPayment{}, []MirrorSaleCharge{}, []MirrorSaleDiscount{}, []MirrorSaleLink{}
			out[s.ID] = s
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	found := make([]string, 0, len(out))
	for _, id := range ids {
		if _, ok := out[id]; ok {
			found = append(found, id)
		}
	}
	found = dedupeStrings(found)
	if len(found) == 0 {
		return out, nil
	}
	lineOwner := map[string]*MirrorSale{}
	var lineIDs []string
	if err := eachMirrorChunk(found, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadLinesSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load lines: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var saleID string
			l := MirrorSaleLine{Modifiers: []MirrorLineModifier{}}
			if err := rows.Scan(&saleID, &l.ID, &l.LineNo, &l.ItemID, &l.VariantID, &l.NameSnapshot, &l.SKUSnapshot, &l.BarcodeSnapshot,
				&l.Quantity, &l.UnitPrice, &l.LineDiscount, &l.TaxRateBP, &l.TaxAmount, &l.TotalBeforeTax, &l.TotalAfterTax, &l.OrderType, &l.RefundOfLineID); err != nil {
				return fmt.Errorf("sales mirror: scan line: %w", err)
			}
			if s := out[saleID]; s != nil {
				s.Lines = append(s.Lines, l)
				lineOwner[l.ID] = s
				lineIDs = append(lineIDs, l.ID)
			}
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	if err := eachMirrorChunk(lineIDs, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadModifiersSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load modifiers: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var lineID string
			var m MirrorLineModifier
			if err := rows.Scan(&lineID, &m.ID, &m.GroupID, &m.OptionID, &m.GroupNameSnapshot, &m.OptionNameSnapshot, &m.PriceDeltaMinor); err != nil {
				return fmt.Errorf("sales mirror: scan modifier: %w", err)
			}
			if s := lineOwner[lineID]; s != nil {
				for i := range s.Lines {
					if s.Lines[i].ID == lineID {
						s.Lines[i].Modifiers = append(s.Lines[i].Modifiers, m)
						break
					}
				}
			}
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	if err := eachMirrorChunk(found, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadPaymentsSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load payments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var saleID string
			var p MirrorPayment
			if err := rows.Scan(&saleID, &p.ID, &p.MethodID, &p.Amount, &p.Currency, &p.ChangeGiven, &p.TipAmount, &p.TipRecipient,
				&p.PaidAt, &p.LocalDate, &p.VoucherID, &p.Reference, &p.MaskedPAN, &p.AuthCode, &p.TerminalID, &p.TraceID); err != nil {
				return fmt.Errorf("sales mirror: scan payment: %w", err)
			}
			if s := out[saleID]; s != nil {
				s.Payments = append(s.Payments, p)
			}
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	if err := eachMirrorChunk(found, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadChargesSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load charges: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var saleID string
			var c MirrorSaleCharge
			if err := rows.Scan(&saleID, &c.Seq, &c.Key, &c.Label, &c.AmountMinor, &c.TaxBasisBP, &c.Base); err != nil {
				return fmt.Errorf("sales mirror: scan charge: %w", err)
			}
			if s := out[saleID]; s != nil {
				s.Charges = append(s.Charges, c)
			}
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	if err := eachMirrorChunk(found, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadDiscountsSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load discounts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var saleID string
			var d MirrorSaleDiscount
			if err := rows.Scan(&saleID, &d.ID, &d.LineID, &d.Type, &d.Value, &d.Amount, &d.Reason); err != nil {
				return fmt.Errorf("sales mirror: scan discount: %w", err)
			}
			if s := out[saleID]; s != nil {
				s.Discounts = append(s.Discounts, d)
			}
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	if err := eachMirrorChunk(found, func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadLinksSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load links: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var saleID string
			var l MirrorSaleLink
			if err := rows.Scan(&saleID, &l.ID, &l.OriginalSaleID, &l.Reason); err != nil {
				return fmt.Errorf("sales mirror: scan link: %w", err)
			}
			if s := out[saleID]; s != nil {
				s.Links = append(s.Links, l)
			}
		}
		return rows.Err()
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadMirrorCloses reads the given report_archive rows (this main till's own
// closes) as feed records.
func (r *SalesMirrorRepo) LoadMirrorCloses(ctx context.Context, ids []string, src MirrorSource) (map[string]*MirrorClose, error) {
	out := make(map[string]*MirrorClose, len(ids))
	err := eachMirrorChunk(dedupeStrings(ids), func(chunk []string) error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadClosesSQL, mirrorPlaceholders(len(chunk))), mirrorArgs(chunk)...)
		if err != nil {
			return fmt.Errorf("sales mirror: load closes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c := &MirrorClose{OriginTillID: src.MainTillID, OriginTillName: src.MainTillName}
			if err := rows.Scan(&c.ID, &c.Kind, &c.Period, &c.ZNumber, &c.PrevZNumber, &c.PrevClosedAt, &c.FirstReceipt,
				&c.LastReceipt, &c.ContentJSON, &c.CreatedAt); err != nil {
				return fmt.Errorf("sales mirror: scan close: %w", err)
			}
			out[c.ID] = c
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// MirrorStateKey is a position in a state re-sync: sales in (local_date, id)
// order, then closes in (created_at, id) order.
type MirrorStateKey struct {
	Phase string // "sales" or "closes"
	A, B  string // (local_date, id) or (created_at, id), exclusive
}

// StateSaleIDs pages the ids of completed or voided sales with
// local_date >= fromDate, strictly after (afterDate, afterRowID) in
// (local_date, rowid) order, leaving out excludeTill's own (sales.till_id).
// It returns each sale's id, local_date and rowid (the next cursor). The
// order comes straight from idx_sales_local_date (whose entries end in the
// rowid), so a page never sorts the rest of the window (M2 review finding
// 4); TestSalesMirrorStateReadUsesIndexOrder pins it. The rowid is stable
// for the length of one re-sync on this till.
func (r *SalesMirrorRepo) StateSaleIDs(ctx context.Context, fromDate, afterDate string, afterRowID int64, excludeTill string, limit int) (ids, dates []string, rowids []int64, err error) {
	if afterDate < fromDate {
		afterDate, afterRowID = fromDate, -1
	}
	rows, err := r.db.QueryContext(ctx, mirrorStateSalesSQL, afterDate, afterDate, afterRowID, excludeTill, limit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sales mirror: state sales: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, d string
		var rid int64
		if err := rows.Scan(&id, &d, &rid); err != nil {
			return nil, nil, nil, fmt.Errorf("sales mirror: scan state sale: %w", err)
		}
		ids, dates, rowids = append(ids, id), append(dates, d), append(rowids, rid)
	}
	return ids, dates, rowids, rows.Err()
}

const mirrorStateSalesSQL = `
SELECT id, local_date, rowid FROM sales INDEXED BY idx_sales_local_date
WHERE (local_date > ? OR (local_date = ? AND rowid > ?))
  AND status IN ('completed', 'voided') AND till_id <> ?
ORDER BY local_date, rowid LIMIT ?`

// StateCloseIDs pages this main till's closes created on or after fromDate,
// strictly after (afterCreated, afterID).
func (r *SalesMirrorRepo) StateCloseIDs(ctx context.Context, fromDate, afterCreated, afterID string, limit int) ([]string, []string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, created_at FROM report_archive
WHERE created_at >= ? AND (created_at > ? OR (created_at = ? AND id > ?))
ORDER BY created_at, id LIMIT ?`, fromDate, afterCreated, afterCreated, afterID, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("sales mirror: state closes: %w", err)
	}
	defer rows.Close()
	var ids, at []string
	for rows.Next() {
		var id, c string
		if err := rows.Scan(&id, &c); err != nil {
			return nil, nil, fmt.Errorf("sales mirror: scan state close: %w", err)
		}
		ids, at = append(ids, id), append(at, c)
	}
	return ids, at, rows.Err()
}

// mirrorReceiptPrefixesInUse is every T<n>- receipt prefix in this till's
// read-only sales copy (design §3.6): another till's numbers a promoted till
// must never grant again. receiptPrefixesInUse (tills_receipt_prefix.go)
// adds them; this is the copy's side of that query, kept here because only
// this file may read the copy.
func mirrorReceiptPrefixesInUse(ctx context.Context, q dbExecutor) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT DISTINCT substr(receipt_no, 1, instr(receipt_no, '-')) FROM mirror_sales
WHERE receipt_no GLOB 'T[0-9]*-*'`)
	if err != nil {
		return nil, fmt.Errorf("receipt prefixes in the sales copy: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan copied receipt prefix: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------
// Additional tills' own closes (ut-docs#5074 M7, design D9).
// ---------------------------------------------------------------------

// MirrorCloseCursor is a position in this till's report_archive, in
// (created_at, id) order.
type MirrorCloseCursor struct{ CreatedAt, ID string }

// OwnClosesAfter lists this till's own archived closes strictly after c, in
// (created_at, id) order, as close records (origin is filled by the main
// till from the pushing till's identity).
func (r *SalesMirrorRepo) OwnClosesAfter(ctx context.Context, c MirrorCloseCursor, limit int) ([]MirrorClose, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, period, z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt, content_json, created_at
FROM report_archive
WHERE created_at > ? OR (created_at = ? AND id > ?)
ORDER BY created_at, id LIMIT ?`, c.CreatedAt, c.CreatedAt, c.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("sales mirror: own closes: %w", err)
	}
	defer rows.Close()
	var out []MirrorClose
	for rows.Next() {
		var m MirrorClose
		if err := rows.Scan(&m.ID, &m.Kind, &m.Period, &m.ZNumber, &m.PrevZNumber, &m.PrevClosedAt, &m.FirstReceipt,
			&m.LastReceipt, &m.ContentJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("sales mirror: scan own close: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// InsertPeerCloses stores closes an additional till pushed. A close is
// immutable: one already held (same till, kind and period, or same id) is
// kept as it is. A close whose id is in this till's own report_archive is
// this till's own close, inherited by the pushing till in its join
// snapshot, and is never stored as that till's. (Accepted edge: after the
// main till restores a backup that lacks a close it had already handed out
// in a join snapshot, a till joined before ut-docs#5074 — no push cursor —
// could push that close as its own; every later join seeds the cursor.)
// Returns how many were new.
func (r *SalesMirrorRepo) InsertPeerCloses(ctx context.Context, tillID string, closes []MirrorClose, now time.Time) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("peer closes: begin: %w", err)
	}
	defer tx.Rollback()
	at := now.UTC().Format(time.RFC3339)
	n := 0
	for _, c := range closes {
		res, err := tx.ExecContext(ctx, `
INSERT INTO peer_closes (till_id, id, kind, period, z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt,
  content_json, created_at, received_at)
SELECT ?,?,?,?,?,?,?,?,?,?,?,?
WHERE NOT EXISTS (SELECT 1 FROM report_archive WHERE id = ?)
ON CONFLICT DO NOTHING`,
			tillID, c.ID, c.Kind, c.Period, c.ZNumber, c.PrevZNumber, c.PrevClosedAt, c.FirstReceipt, c.LastReceipt,
			c.ContentJSON, c.CreatedAt, at, c.ID)
		if err != nil {
			return 0, fmt.Errorf("peer close %s: %w", c.ID, err)
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("peer closes: commit: %w", err)
	}
	return n, nil
}

// MirrorPeerCloseKey names one additional till's close in peer_closes.
type MirrorPeerCloseKey struct{ TillID, ID string }

// LoadMirrorPeerCloses reads the given additional tills' closes as feed
// records (origin = the till that closed), in batched reads.
func (r *SalesMirrorRepo) LoadMirrorPeerCloses(ctx context.Context, keys []MirrorPeerCloseKey) (map[MirrorPeerCloseKey]*MirrorClose, error) {
	out := make(map[MirrorPeerCloseKey]*MirrorClose, len(keys))
	for start := 0; start < len(keys); start += mirrorInChunk / 2 {
		chunk := keys[start:min(start+mirrorInChunk/2, len(keys))]
		args := make([]any, 0, 2*len(chunk))
		for _, k := range chunk {
			args = append(args, k.TillID, k.ID)
		}
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf(mirrorLoadPeerClosesSQL, strings.TrimSuffix(strings.Repeat("(?,?),", len(chunk)), ",")), args...)
		if err != nil {
			return nil, fmt.Errorf("sales mirror: load peer closes: %w", err)
		}
		for rows.Next() {
			c := &MirrorClose{}
			if err := rows.Scan(&c.OriginTillID, &c.OriginTillName, &c.ID, &c.Kind, &c.Period, &c.ZNumber, &c.PrevZNumber,
				&c.PrevClosedAt, &c.FirstReceipt, &c.LastReceipt, &c.ContentJSON, &c.CreatedAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("sales mirror: scan peer close: %w", err)
			}
			out[MirrorPeerCloseKey{TillID: c.OriginTillID, ID: c.ID}] = c
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

const mirrorLoadPeerClosesSQL = `
SELECT p.till_id, COALESCE(t.name, ''), p.id, p.kind, p.period, p.z_number, p.prev_z_number, p.prev_closed_at,
  p.first_receipt, p.last_receipt, p.content_json, p.created_at
FROM peer_closes p LEFT JOIN tills t ON t.id = p.till_id
WHERE (p.till_id, p.id) IN (VALUES %s)`

// StatePeerCloseKeys pages the additional tills' closes created on or after
// fromDate, strictly after (afterCreated, afterRowID), leaving out
// excludeTill's own.
func (r *SalesMirrorRepo) StatePeerCloseKeys(ctx context.Context, fromDate, afterCreated string, afterRowID int64, excludeTill string, limit int) ([]MirrorPeerCloseKey, []string, []int64, error) {
	if afterCreated < fromDate {
		afterCreated, afterRowID = fromDate, -1
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT till_id, id, created_at, rowid FROM peer_closes INDEXED BY idx_peer_closes_created
WHERE (created_at > ? OR (created_at = ? AND rowid > ?)) AND till_id <> ?
ORDER BY created_at, rowid LIMIT ?`, afterCreated, afterCreated, afterRowID, excludeTill, limit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sales mirror: state peer closes: %w", err)
	}
	defer rows.Close()
	var keys []MirrorPeerCloseKey
	var created []string
	var rowids []int64
	for rows.Next() {
		var k MirrorPeerCloseKey
		var c string
		var rid int64
		if err := rows.Scan(&k.TillID, &k.ID, &c, &rid); err != nil {
			return nil, nil, nil, fmt.Errorf("sales mirror: scan state peer close: %w", err)
		}
		keys, created, rowids = append(keys, k), append(created, c), append(rowids, rid)
	}
	return keys, created, rowids, rows.Err()
}
