package data

// The receiving side of SalesMirrorRepo (ut-docs#5074 M3): an additional
// till's copy — its state, the apply of a feed page or a state re-sync page
// in one transaction with the cursor, and the copy's bounds. Part of the one
// repository guard-sales-mirror-isolation.sh allows to touch the copy.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// mirror_state keys (purely local to this till).
const (
	mirrorStateEpoch        = "epoch"
	mirrorStateCursor       = "cursor"
	mirrorStateMainTillID   = "main_till_id"
	mirrorStateHorizon      = "horizon"
	mirrorStatePaused       = "paused_reason"
	mirrorStateLastApply    = "last_apply_at"
	mirrorStateRejected     = "rejected"
	mirrorStateResyncEpoch  = "resync_epoch"
	mirrorStateResyncResume = "resync_resume_seq"
	mirrorStateResyncCursor = "resync_cursor"
)

// MirrorState is this till's copy cursor and bookkeeping.
type MirrorState struct {
	Epoch        string
	Cursor       int64
	MainTillID   string
	Horizon      int64
	PausedReason string
	LastApplyAt  string
	Rejected     int64
	// ResyncEpoch/ResyncResumeSeq/ResyncCursor are a state re-sync in
	// progress (contract "state re-sync"); empty when none.
	ResyncEpoch     string
	ResyncResumeSeq int64
	ResyncCursor    string
}

// MirrorApplyResult counts what one applied page did.
type MirrorApplyResult struct {
	Applied  int // sales or closes written
	Skipped  int // own-origin, gone, older than the window, or not newer
	Rejected int // malformed (logged by the caller; the cursor still moves)
}

// ErrMirrorCursorMoved means the page does not continue this till's cursor
// (a duplicate, a gap, or a concurrent apply moved it): nothing was written.
var ErrMirrorCursorMoved = errors.New("sales mirror: page does not continue the cursor")

// ---------------------------------------------------------------------
// Receiving side: state, apply, bounds.
// ---------------------------------------------------------------------

// State reads this till's copy state. A till that never copied has the zero
// value.
func (r *SalesMirrorRepo) State(ctx context.Context) (MirrorState, error) {
	return readMirrorState(ctx, r.db)
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func readMirrorState(ctx context.Context, q queryer) (MirrorState, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM mirror_state`)
	if err != nil {
		return MirrorState{}, fmt.Errorf("read mirror state: %w", err)
	}
	defer rows.Close()
	var s MirrorState
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return MirrorState{}, fmt.Errorf("scan mirror state: %w", err)
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case mirrorStateEpoch:
			s.Epoch = v
		case mirrorStateCursor:
			s.Cursor = n
		case mirrorStateMainTillID:
			s.MainTillID = v
		case mirrorStateHorizon:
			s.Horizon = n
		case mirrorStatePaused:
			s.PausedReason = v
		case mirrorStateLastApply:
			s.LastApplyAt = v
		case mirrorStateRejected:
			s.Rejected = n
		case mirrorStateResyncEpoch:
			s.ResyncEpoch = v
		case mirrorStateResyncResume:
			s.ResyncResumeSeq = n
		case mirrorStateResyncCursor:
			s.ResyncCursor = v
		}
	}
	return s, rows.Err()
}

func putMirrorState(ctx context.Context, q queryer, kv map[string]string) error {
	for k, v := range kv {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO mirror_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return fmt.Errorf("write mirror state %s: %w", k, err)
		}
	}
	return nil
}

// SetPaused records (or clears, with "") why the copy is paused.
func (r *SalesMirrorRepo) SetPaused(ctx context.Context, reason string) error {
	return putMirrorState(ctx, r.db, map[string]string{mirrorStatePaused: reason})
}

// MirrorFeedPage is one feed page or link frame to apply.
type MirrorFeedPage struct {
	Epoch      string
	MainTillID string
	FromSeq    int64
	ToSeq      int64
	Horizon    int64
	Records    []MirrorRecord
}

// MirrorApplyOpts are the receiving till's own facts for an apply.
type MirrorApplyOpts struct {
	// SelfTillID is this till's id in the shop roster (sync.till_id); its
	// own sales are left out (they live in sales).
	SelfTillID string
	// WindowStart (YYYY-MM-DD): sales with an earlier local_date are not
	// stored (the window, design §6). Empty = no lower bound.
	WindowStart string
	// Now stamps received_at / last_apply_at.
	Now time.Time
}

// ApplyFeedPage applies one feed page or frame in ONE transaction together
// with the cursor move to p.ToSeq. It refuses (ErrMirrorCursorMoved, nothing
// written) unless p continues the stored cursor in the stored epoch, so a
// duplicate, reordered or gapped delivery changes nothing.
func (r *SalesMirrorRepo) ApplyFeedPage(ctx context.Context, p MirrorFeedPage, o MirrorApplyOpts) (MirrorApplyResult, error) {
	var res MirrorApplyResult
	if p.ToSeq < p.FromSeq {
		return res, fmt.Errorf("sales mirror: bad range (%d, %d]", p.FromSeq, p.ToSeq)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("sales mirror apply: begin: %w", err)
	}
	defer tx.Rollback()
	st, err := readMirrorState(ctx, tx)
	if err != nil {
		return res, err
	}
	if st.Epoch == "" || st.Epoch != p.Epoch || st.Cursor != p.FromSeq {
		return res, ErrMirrorCursorMoved
	}
	inPage := func(seq int64) bool { return seq > p.FromSeq && seq <= p.ToSeq }
	if err := applyMirrorRecords(ctx, tx, p.Epoch, p.Records, inPage, o, &res); err != nil {
		return MirrorApplyResult{}, err
	}
	kv := map[string]string{
		mirrorStateCursor:    strconv.FormatInt(p.ToSeq, 10),
		mirrorStateLastApply: o.Now.UTC().Format(time.RFC3339),
		mirrorStateHorizon:   strconv.FormatInt(p.Horizon, 10),
	}
	if p.MainTillID != "" {
		kv[mirrorStateMainTillID] = p.MainTillID
	}
	if res.Rejected > 0 {
		kv[mirrorStateRejected] = strconv.FormatInt(st.Rejected+int64(res.Rejected), 10)
	}
	if err := putMirrorState(ctx, tx, kv); err != nil {
		return MirrorApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MirrorApplyResult{}, fmt.Errorf("sales mirror apply: commit: %w", err)
	}
	return res, nil
}

// MirrorStatePage is one state re-sync page (contract "state re-sync").
type MirrorStatePage struct {
	Epoch      string
	MainTillID string
	ResumeSeq  int64
	NextCursor string
	More       bool
	Records    []MirrorRecord
}

// ApplyStatePage applies one state re-sync page in one transaction and
// records the re-sync's progress in the same transaction, so a crash resumes
// at the next page. On the last page (More false) the cursor becomes
// (Epoch, ResumeSeq) and the re-sync bookkeeping is cleared. A page for a
// re-sync other than the one in progress (a different epoch or resume_seq)
// restarts the bookkeeping only when it is a first page (fromCursor "").
func (r *SalesMirrorRepo) ApplyStatePage(ctx context.Context, fromCursor string, p MirrorStatePage, o MirrorApplyOpts) (MirrorApplyResult, error) {
	var res MirrorApplyResult
	if p.Epoch == "" {
		return res, fmt.Errorf("sales mirror: state page without an epoch")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("sales mirror state apply: begin: %w", err)
	}
	defer tx.Rollback()
	st, err := readMirrorState(ctx, tx)
	if err != nil {
		return res, err
	}
	if fromCursor != "" && (st.ResyncEpoch != p.Epoch || st.ResyncResumeSeq != p.ResumeSeq || st.ResyncCursor != fromCursor) {
		return res, ErrMirrorCursorMoved
	}
	atResume := func(seq int64) bool { return seq == p.ResumeSeq }
	if err := applyMirrorRecords(ctx, tx, p.Epoch, p.Records, atResume, o, &res); err != nil {
		return MirrorApplyResult{}, err
	}
	kv := map[string]string{mirrorStateLastApply: o.Now.UTC().Format(time.RFC3339)}
	if p.MainTillID != "" {
		kv[mirrorStateMainTillID] = p.MainTillID
	}
	if res.Rejected > 0 {
		kv[mirrorStateRejected] = strconv.FormatInt(st.Rejected+int64(res.Rejected), 10)
	}
	if p.More {
		kv[mirrorStateResyncEpoch] = p.Epoch
		kv[mirrorStateResyncResume] = strconv.FormatInt(p.ResumeSeq, 10)
		kv[mirrorStateResyncCursor] = p.NextCursor
	} else {
		kv[mirrorStateEpoch] = p.Epoch
		kv[mirrorStateCursor] = strconv.FormatInt(p.ResumeSeq, 10)
		kv[mirrorStateResyncEpoch] = ""
		kv[mirrorStateResyncResume] = "0"
		kv[mirrorStateResyncCursor] = ""
	}
	if err := putMirrorState(ctx, tx, kv); err != nil {
		return MirrorApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MirrorApplyResult{}, fmt.Errorf("sales mirror state apply: commit: %w", err)
	}
	return res, nil
}

// validMirrorSale is the shape check of an incoming sale (external input,
// CLAUDE.md "validate all external input"). Duplicate child ids are refused
// here, before any write: they would fail a primary key and, without this,
// wedge the copy on the same page forever (M1 review finding 1).
func validMirrorSale(s *MirrorSale) bool {
	if s == nil || strings.TrimSpace(s.ID) == "" || s.ReceiptNo == "" || s.CreatedAt == "" || s.Currency == "" {
		return false
	}
	if s.Status != "completed" && s.Status != "voided" {
		return false
	}
	// local_date drives the window and the disk budget's oldest-day trim: a
	// missing or malformed one would bypass both (M3 review finding 4).
	if _, err := time.Parse("2006-01-02", s.LocalDate); err != nil {
		return false
	}
	ids := map[string]struct{}{}
	fresh := func(kind, id string) bool {
		if strings.TrimSpace(id) == "" {
			return false
		}
		k := kind + "\x00" + id
		if _, dup := ids[k]; dup {
			return false
		}
		ids[k] = struct{}{}
		return true
	}
	for _, l := range s.Lines {
		if !fresh("line", l.ID) {
			return false
		}
		for _, m := range l.Modifiers {
			if !fresh("modifier", m.ID) {
				return false
			}
		}
	}
	for _, p := range s.Payments {
		if p.MethodID == "" || !fresh("payment", p.ID) {
			return false
		}
	}
	for _, c := range s.Charges {
		if !fresh("charge", strconv.FormatInt(c.Seq, 10)) {
			return false
		}
	}
	for _, d := range s.Discounts {
		if !fresh("discount", d.ID) {
			return false
		}
	}
	for _, l := range s.Links {
		if l.OriginalSaleID == "" || !fresh("link", l.ID) {
			return false
		}
	}
	return true
}

func validMirrorClose(c *MirrorClose) bool {
	return c != nil && c.ID != "" && c.Kind != "" && c.Period != "" && c.CreatedAt != ""
}

// isConstraintError reports a SQLite constraint failure (a record that
// cannot be stored as sent), as opposed to an I/O or context error that
// must abort the page.
func isConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}

// applyMirrorRecords stores recs inside tx. seqOK says whether a record's
// seq belongs to the page (a feed page's (from, to], a state page's
// resume_seq): a record outside it is rejected, never stored with a seq
// that would shadow a later real state (M1 review finding 4). Each record
// runs under its own savepoint, so a record the schema refuses is rejected
// and the page goes on.
func applyMirrorRecords(ctx context.Context, tx *sql.Tx, epoch string, recs []MirrorRecord, seqOK func(int64) bool, o MirrorApplyOpts, res *MirrorApplyResult) error {
	now := o.Now.UTC().Format(time.RFC3339)
	for i := range recs {
		rec := &recs[i]
		if !seqOK(rec.Seq) {
			res.Rejected++
			continue
		}
		var write func() (bool, error)
		switch {
		case rec.Gone:
			res.Skipped++
			continue
		case rec.Op == MirrorOpSale:
			if !validMirrorSale(rec.Sale) {
				res.Rejected++
				continue
			}
			s := rec.Sale
			if (o.SelfTillID != "" && s.OriginTillID == o.SelfTillID) ||
				(o.WindowStart != "" && s.LocalDate != "" && s.LocalDate < o.WindowStart) {
				res.Skipped++
				continue
			}
			write = func() (bool, error) { return upsertMirrorSale(ctx, tx, epoch, rec.Seq, s, now) }
		case rec.Op == MirrorOpClose:
			if !validMirrorClose(rec.Close) {
				res.Rejected++
				continue
			}
			if o.SelfTillID != "" && rec.Close.OriginTillID == o.SelfTillID {
				res.Skipped++
				continue
			}
			write = func() (bool, error) { return upsertMirrorClose(ctx, tx, epoch, rec.Seq, rec.Close, now) }
		default:
			res.Rejected++
			continue
		}
		if _, err := tx.ExecContext(ctx, `SAVEPOINT mirror_record`); err != nil {
			return fmt.Errorf("sales mirror apply: savepoint: %w", err)
		}
		wrote, err := write()
		if err != nil {
			if _, rerr := tx.ExecContext(ctx, `ROLLBACK TO mirror_record`); rerr != nil {
				return fmt.Errorf("sales mirror apply: rollback record: %w", rerr)
			}
			if _, rerr := tx.ExecContext(ctx, `RELEASE mirror_record`); rerr != nil {
				return fmt.Errorf("sales mirror apply: release record: %w", rerr)
			}
			if !isConstraintError(err) {
				return err
			}
			res.Rejected++
			continue
		}
		if _, err := tx.ExecContext(ctx, `RELEASE mirror_record`); err != nil {
			return fmt.Errorf("sales mirror apply: release record: %w", err)
		}
		if wrote {
			res.Applied++
		} else {
			res.Skipped++
		}
	}
	return nil
}

// upsertMirrorSale writes s when it is newer than the stored copy (a higher
// seq, or another epoch). Children are replaced wholesale.
func upsertMirrorSale(ctx context.Context, tx *sql.Tx, epoch string, seq int64, s *MirrorSale, now string) (bool, error) {
	var storedSeq int64
	var storedEpoch string
	err := tx.QueryRowContext(ctx, `SELECT src_seq, epoch FROM mirror_sales WHERE id = ?`, s.ID).Scan(&storedSeq, &storedEpoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("mirror sale %s: read: %w", s.ID, err)
	default:
		if storedEpoch == epoch && storedSeq >= seq {
			return false, nil
		}
		// ON DELETE CASCADE removes every child (foreign_keys is on for
		// every pooled connection, internal/db.Open's DSN).
		if _, err := tx.ExecContext(ctx, `DELETE FROM mirror_sales WHERE id = ?`, s.ID); err != nil {
			return false, fmt.Errorf("mirror sale %s: replace: %w", s.ID, err)
		}
	}
	offline := 0
	if s.Offline {
		offline = 1
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_sales (id, src_seq, epoch, origin_till_id, origin_till_name, receipt_no, display_no, status,
  sale_type, tender_type, order_type, register_id, cashier_id, customer_id, table_id, currency,
  subtotal, discount_total, tax_total, total, rounding, service_charge_amount, service_charge_tax_basis_bp,
  voucher_issue_total, note, offline, created_at, completed_at, voided_at, local_date, voided_local_date, received_at)
VALUES (?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?)`,
		s.ID, seq, epoch, s.OriginTillID, s.OriginTillName, s.ReceiptNo, s.DisplayNo, s.Status,
		s.SaleType, s.TenderType, s.OrderType, s.RegisterID, s.CashierID, s.CustomerID, s.TableID, s.Currency,
		s.Subtotal, s.DiscountTotal, s.TaxTotal, s.Total, s.Rounding, s.ServiceChargeAmount, s.ServiceChargeTaxBasisBP,
		s.VoucherIssueTotal, s.Note, offline, s.CreatedAt, s.CompletedAt, s.VoidedAt, s.LocalDate, s.VoidedLocalDate, now); err != nil {
		return false, fmt.Errorf("mirror sale %s: insert: %w", s.ID, err)
	}
	for _, l := range s.Lines {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_sale_lines (id, sale_id, line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot,
  quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type, refund_of_line_id)
VALUES (?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?)`,
			l.ID, s.ID, l.LineNo, l.ItemID, l.VariantID, l.NameSnapshot, l.SKUSnapshot, l.BarcodeSnapshot,
			l.Quantity, l.UnitPrice, l.LineDiscount, l.TaxRateBP, l.TaxAmount, l.TotalBeforeTax, l.TotalAfterTax, l.OrderType, l.RefundOfLineID); err != nil {
			return false, fmt.Errorf("mirror sale %s: line %s: %w", s.ID, l.ID, err)
		}
		for _, m := range l.Modifiers {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_sale_line_modifiers (id, sale_line_id, group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor)
VALUES (?,?,?,?,?,?,?)`, m.ID, l.ID, m.GroupID, m.OptionID, m.GroupNameSnapshot, m.OptionNameSnapshot, m.PriceDeltaMinor); err != nil {
				return false, fmt.Errorf("mirror sale %s: modifier %s: %w", s.ID, m.ID, err)
			}
		}
	}
	for _, p := range s.Payments {
		tip := p.TipRecipient
		if tip == "" {
			tip = "employee"
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_payments (id, sale_id, method_id, amount, currency, change_given, tip_amount, tip_recipient, paid_at,
  local_date, voucher_id, reference, masked_pan, auth_code, terminal_id, trace_id)
VALUES (?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?)`,
			p.ID, s.ID, p.MethodID, p.Amount, p.Currency, p.ChangeGiven, p.TipAmount, tip, p.PaidAt,
			p.LocalDate, p.VoucherID, p.Reference, p.MaskedPAN, p.AuthCode, p.TerminalID, p.TraceID); err != nil {
			return false, fmt.Errorf("mirror sale %s: payment %s: %w", s.ID, p.ID, err)
		}
	}
	for _, c := range s.Charges {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_sale_charges (sale_id, seq, key, label, amount_minor, tax_basis_bp, base) VALUES (?,?,?,?,?,?,?)`,
			s.ID, c.Seq, c.Key, c.Label, c.AmountMinor, c.TaxBasisBP, c.Base); err != nil {
			return false, fmt.Errorf("mirror sale %s: charge %d: %w", s.ID, c.Seq, err)
		}
	}
	for _, d := range s.Discounts {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_sale_discounts (id, sale_id, line_id, type, value, amount, reason) VALUES (?,?,?,?,?,?,?)`,
			d.ID, s.ID, d.LineID, d.Type, d.Value, d.Amount, d.Reason); err != nil {
			return false, fmt.Errorf("mirror sale %s: discount %s: %w", s.ID, d.ID, err)
		}
	}
	for _, l := range s.Links {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO mirror_sale_links (id, sale_id, original_sale_id, reason) VALUES (?,?,?,?)`,
			l.ID, s.ID, l.OriginalSaleID, l.Reason); err != nil {
			return false, fmt.Errorf("mirror sale %s: link %s: %w", s.ID, l.ID, err)
		}
	}
	return true, nil
}

func upsertMirrorClose(ctx context.Context, tx *sql.Tx, epoch string, seq int64, c *MirrorClose, now string) (bool, error) {
	res, err := tx.ExecContext(ctx, `
INSERT INTO mirror_closes (origin_till_id, kind, period, src_id, origin_till_name, z_number, prev_z_number, prev_closed_at,
  first_receipt, last_receipt, content_json, created_at, src_seq, epoch, received_at)
VALUES (?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?)
ON CONFLICT (origin_till_id, kind, period) DO UPDATE SET
  src_id = excluded.src_id, origin_till_name = excluded.origin_till_name, z_number = excluded.z_number,
  prev_z_number = excluded.prev_z_number, prev_closed_at = excluded.prev_closed_at,
  first_receipt = excluded.first_receipt, last_receipt = excluded.last_receipt,
  content_json = excluded.content_json, created_at = excluded.created_at,
  src_seq = excluded.src_seq, epoch = excluded.epoch, received_at = excluded.received_at
WHERE mirror_closes.epoch <> excluded.epoch OR mirror_closes.src_seq < excluded.src_seq`,
		c.OriginTillID, c.Kind, c.Period, c.ID, c.OriginTillName, c.ZNumber, c.PrevZNumber, c.PrevClosedAt,
		c.FirstReceipt, c.LastReceipt, c.ContentJSON, c.CreatedAt, seq, epoch, now)
	if err != nil {
		return false, fmt.Errorf("mirror close %s: %w", c.ID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MirrorCopyStats summarises this till's copy (diagnostics, budget).
type MirrorCopyStats struct {
	Sales           int64
	Lines           int64
	Payments        int64
	Closes          int64
	CloseBytes      int64
	OldestLocalDate string
}

// BytesEstimate is the copy's approximate size on disk: measured per-row
// sizes of a typical sale (design §6.1), plus the closes' own content.
func (s MirrorCopyStats) BytesEstimate() int64 {
	return s.Sales*600 + s.Lines*260 + s.Payments*200 + s.Closes*300 + s.CloseBytes
}

// CopyStats counts the copy. Every count is an indexed or rowid scan of a
// copy table; it never touches sales.
func (r *SalesMirrorRepo) CopyStats(ctx context.Context) (MirrorCopyStats, error) {
	var s MirrorCopyStats
	var oldest sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM mirror_sales),
       (SELECT COUNT(*) FROM mirror_sale_lines),
       (SELECT COUNT(*) FROM mirror_payments),
       (SELECT COUNT(*) FROM mirror_closes),
       (SELECT COALESCE(SUM(LENGTH(content_json)), 0) FROM mirror_closes),
       (SELECT MIN(local_date) FROM mirror_sales)`).
		Scan(&s.Sales, &s.Lines, &s.Payments, &s.Closes, &s.CloseBytes, &oldest)
	if err != nil {
		return MirrorCopyStats{}, fmt.Errorf("sales mirror stats: %w", err)
	}
	s.OldestLocalDate = oldest.String
	return s, nil
}

// PruneCopyBefore deletes copied sales with local_date < salesBefore and
// copied closes created before closesBefore (each YYYY-MM-DD; "" skips),
// in chunks so no transaction holds the write lock long. Returns the
// number of sales deleted.
func (r *SalesMirrorRepo) PruneCopyBefore(ctx context.Context, salesBefore, closesBefore string) (int64, error) {
	var total int64
	if salesBefore != "" {
		for {
			res, err := r.db.ExecContext(ctx, `
DELETE FROM mirror_sales WHERE id IN (
  SELECT id FROM mirror_sales WHERE local_date < ? ORDER BY local_date LIMIT ?)`, salesBefore, salesMirrorPruneChunk)
			if err != nil {
				return total, fmt.Errorf("sales mirror prune copy: %w", err)
			}
			n, _ := res.RowsAffected()
			total += n
			if n < salesMirrorPruneChunk {
				break
			}
		}
	}
	if closesBefore != "" {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM mirror_closes WHERE created_at < ?`, closesBefore); err != nil {
			return total, fmt.Errorf("sales mirror prune closes: %w", err)
		}
	}
	return total, nil
}

// StartResync begins (or restarts) a state re-sync in epoch: the next state
// page is a first page. The copied rows and the current cursor stay until
// the re-sync's last page moves the cursor.
func (r *SalesMirrorRepo) StartResync(ctx context.Context, epoch string) error {
	if strings.TrimSpace(epoch) == "" {
		return errors.New("sales mirror: re-sync without an epoch")
	}
	return putMirrorState(ctx, r.db, map[string]string{
		mirrorStateResyncEpoch: epoch, mirrorStateResyncResume: "0", mirrorStateResyncCursor: "",
	})
}
