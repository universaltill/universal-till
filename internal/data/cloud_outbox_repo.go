package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// OutboxMark is one cloud_outbox row (migration 084, ADR-0162 §5,
// ut-docs#4464): a local row of the uploadable mastered set that changed
// since its last upload. An open mark (SealedAt "") is the net change still
// to compute; a sealed one carries its change (ChangeID, Body) and is sent
// as is until the cloud answers it.
type OutboxMark struct {
	ID        int64
	Entity    string
	LocalID   string
	CreatedAt string
	ChangeID  string
	Body      string
	SealedAt  string
}

// CloudOutboxRepo reads and writes cloud_outbox and the suppression flag,
// and reads the local values the uploader compares. Stateless; every call
// names its executor.
type CloudOutboxRepo struct{}

// NewCloudOutboxRepo returns the repository.
func NewCloudOutboxRepo() *CloudOutboxRepo { return &CloudOutboxRepo{} }

// Suppress makes the outbox triggers skip every write of tx until
// Unsuppress (the pull's and the upload answer's own writes are the
// cloud's, never till edits). SQLite has one writer, so the flag row is
// never visible to another transaction; it must be removed before tx
// commits.
func (*CloudOutboxRepo) Suppress(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO cloud_outbox_suppress (one) VALUES (1)`); err != nil {
		return fmt.Errorf("cloud outbox suppress: %w", err)
	}
	return nil
}

// Unsuppress removes Suppress's flag; call it before tx commits.
func (*CloudOutboxRepo) Unsuppress(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_outbox_suppress`); err != nil {
		return fmt.Errorf("cloud outbox unsuppress: %w", err)
	}
	return nil
}

// ClearStaleSuppress removes a suppression flag that was ever committed (a
// bug would have to commit it; then every till edit would go unmarked).
// Run outside any transaction: a flag inside an open one is invisible
// here, and SQLite's one writer makes this wait for that transaction,
// whose own Unsuppress has removed it by then.
func (*CloudOutboxRepo) ClearStaleSuppress(ctx context.Context, db *sql.DB) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM cloud_outbox_suppress`)
	if err != nil {
		return false, fmt.Errorf("cloud outbox clear suppress: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func scanMarks(rows *sql.Rows) ([]OutboxMark, error) {
	defer rows.Close()
	var out []OutboxMark
	for rows.Next() {
		var m OutboxMark
		var changeID, body, sealed sql.NullString
		if err := rows.Scan(&m.ID, &m.Entity, &m.LocalID, &m.CreatedAt, &changeID, &body, &sealed); err != nil {
			return nil, fmt.Errorf("cloud outbox: %w", err)
		}
		m.ChangeID, m.Body, m.SealedAt = changeID.String, body.String, sealed.String
		out = append(out, m)
	}
	return out, rows.Err()
}

const outboxCols = `id, entity, local_id, created_at, change_id, body, sealed_at`

// SealedMarks returns up to limit sealed marks in seal order (a row
// another one references is sealed, and so sent, first).
func (*CloudOutboxRepo) SealedMarks(ctx context.Context, q SyncQuerier, limit int) ([]OutboxMark, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+outboxCols+` FROM cloud_outbox WHERE sealed_at IS NOT NULL ORDER BY seal_seq, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("cloud outbox sealed: %w", err)
	}
	return scanMarks(rows)
}

// OpenMarks returns up to limit open marks with id > after, oldest first.
func (*CloudOutboxRepo) OpenMarks(ctx context.Context, q SyncQuerier, after int64, limit int) ([]OutboxMark, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+outboxCols+` FROM cloud_outbox WHERE sealed_at IS NULL AND id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("cloud outbox open: %w", err)
	}
	return scanMarks(rows)
}

// OpenMarkFor returns the open mark of (entity, localID), if any.
func (*CloudOutboxRepo) OpenMarkFor(ctx context.Context, q SyncQuerier, entity, localID string) (OutboxMark, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+outboxCols+` FROM cloud_outbox WHERE entity = ? AND local_id = ? AND sealed_at IS NULL`, entity, localID)
	if err != nil {
		return OutboxMark{}, false, fmt.Errorf("cloud outbox open mark: %w", err)
	}
	ms, err := scanMarks(rows)
	if err != nil || len(ms) == 0 {
		return OutboxMark{}, false, err
	}
	return ms[0], true, nil
}

// Seal stores an open mark's change; false when the mark is no longer open.
func (*CloudOutboxRepo) Seal(ctx context.Context, q SyncQuerier, id int64, changeID, body, at string) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE cloud_outbox SET change_id = ?, body = ?, sealed_at = ?, seal_seq = (SELECT COALESCE(MAX(seal_seq), 0) + 1 FROM cloud_outbox INDEXED BY idx_cloud_outbox_seal) WHERE id = ? AND sealed_at IS NULL`, changeID, body, at, id)
	if err != nil {
		return false, fmt.Errorf("cloud outbox seal: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteMark removes a mark (answered, or nothing to upload).
func (*CloudOutboxRepo) DeleteMark(ctx context.Context, q SyncQuerier, id int64) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM cloud_outbox WHERE id = ?`, id); err != nil {
		return fmt.Errorf("cloud outbox delete: %w", err)
	}
	return nil
}

// EnsureOpenMark opens a mark for (entity, localID) unless one is open,
// stamped createdAt (the uploader re-queues a row it must upload: a
// referenced row not in the cloud yet, a create to re-mint).
func (*CloudOutboxRepo) EnsureOpenMark(ctx context.Context, q SyncQuerier, entity, localID, createdAt string) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO cloud_outbox (entity, local_id, created_at)
SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = ? AND local_id = ? AND sealed_at IS NULL)`,
		entity, localID, createdAt, entity, localID)
	if err != nil {
		return fmt.Errorf("cloud outbox mark: %w", err)
	}
	return nil
}

// HasMark reports whether (entity, localID) has any mark, open or sealed:
// a local edit not answered by the cloud yet, which the pull must not
// overwrite (ADR-0162 §4).
func (*CloudOutboxRepo) HasMark(ctx context.Context, q SyncQuerier, entity, localID string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM cloud_outbox WHERE entity = ? AND local_id = ? LIMIT 1`, entity, localID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cloud outbox has mark: %w", err)
	}
	return true, nil
}

// HasSealedMark reports whether (entity, localID) has a sealed mark: a
// change in flight, not answered yet. A later edit of the row waits for
// that answer before it is sealed (its before is the answered base).
func (*CloudOutboxRepo) HasSealedMark(ctx context.Context, q SyncQuerier, entity, localID string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM cloud_outbox WHERE entity = ? AND local_id = ? AND sealed_at IS NOT NULL LIMIT 1`, entity, localID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cloud outbox has sealed mark: %w", err)
	}
	return true, nil
}

// Counts returns the open and sealed marks.
func (*CloudOutboxRepo) Counts(ctx context.Context, q SyncQuerier) (open, sealed int, err error) {
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(sealed_at IS NULL), 0), COALESCE(SUM(sealed_at IS NOT NULL), 0) FROM cloud_outbox`).Scan(&open, &sealed)
	if err != nil {
		return 0, 0, fmt.Errorf("cloud outbox counts: %w", err)
	}
	return open, sealed, nil
}

// OutboxItem is an item's uploadable local values (contract 1.0.0).
type OutboxItem struct {
	Name, Description, SKU string
	PriceMinor             int64
	CategoryID, TaxCodeID  string // "" = none
	Active                 bool
}

// OutboxBarcode is one item-level barcode: its stored key, type, primary.
type OutboxBarcode struct {
	Key, Type string
	Primary   bool
}

// OutboxItemValues reads an item's uploadable values and its item-level
// barcodes (primary first, then by key); ok is false when it is gone.
func (*CloudOutboxRepo) OutboxItemValues(ctx context.Context, q SyncQuerier, id string) (OutboxItem, []OutboxBarcode, bool, error) {
	var it OutboxItem
	var desc, sku, cat, tax sql.NullString
	err := q.QueryRowContext(ctx, `SELECT name, description, sku, base_price, category_id, tax_code_id, is_active FROM items WHERE id = ?`, id).
		Scan(&it.Name, &desc, &sku, &it.PriceMinor, &cat, &tax, &it.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return it, nil, false, nil
	}
	if err != nil {
		return it, nil, false, fmt.Errorf("outbox item: %w", err)
	}
	it.Description, it.SKU, it.CategoryID, it.TaxCodeID = desc.String, sku.String, cat.String, tax.String
	rows, err := q.QueryContext(ctx, `SELECT barcode, COALESCE(barcode_type, ''), is_primary FROM item_barcodes WHERE item_id = ? ORDER BY is_primary DESC, barcode`, id)
	if err != nil {
		return it, nil, false, fmt.Errorf("outbox item barcodes: %w", err)
	}
	defer rows.Close()
	var bcs []OutboxBarcode
	for rows.Next() {
		var b OutboxBarcode
		if err := rows.Scan(&b.Key, &b.Type, &b.Primary); err != nil {
			return it, nil, false, fmt.Errorf("outbox item barcodes: %w", err)
		}
		bcs = append(bcs, b)
	}
	return it, bcs, true, rows.Err()
}

// OutboxCategory is a category's uploadable local values.
type OutboxCategory struct {
	Name     string
	ParentID string // "" = top level
	Sort     int64
	Active   bool
}

// OutboxCategoryValues reads a category's uploadable values.
func (*CloudOutboxRepo) OutboxCategoryValues(ctx context.Context, q SyncQuerier, id string) (OutboxCategory, bool, error) {
	var c OutboxCategory
	var parent sql.NullString
	err := q.QueryRowContext(ctx, `SELECT name, parent_id, sort_order, is_active FROM categories WHERE id = ?`, id).Scan(&c.Name, &parent, &c.Sort, &c.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("outbox category: %w", err)
	}
	c.ParentID = parent.String
	return c, true, nil
}

// OutboxTaxCode is a tax code's uploadable local values.
type OutboxTaxCode struct {
	Name       string
	RateBP     int64
	TakeawayBP *int64
	Active     bool
}

// OutboxTaxCodeValues reads a tax code's uploadable values.
func (*CloudOutboxRepo) OutboxTaxCodeValues(ctx context.Context, q SyncQuerier, id string) (OutboxTaxCode, bool, error) {
	var t OutboxTaxCode
	var takeaway sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT name, rate_basis_points, takeaway_rate_basis_points, is_active FROM tax_codes WHERE id = ?`, id).
		Scan(&t.Name, &t.RateBP, &takeaway, &t.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, fmt.Errorf("outbox tax code: %w", err)
	}
	if takeaway.Valid {
		v := takeaway.Int64
		t.TakeawayBP = &v
	}
	return t, true, nil
}

// --- cloud_links: base and echo (migration 084) ---

// CloudLinkState is a link with its upload bookkeeping.
type CloudLinkState struct {
	CloudLink
	// Base is the JSON of the uploadable fields last adopted from the
	// cloud ("" = none yet).
	Base string
	// EchoVersion is the version of the till's own accepted upload.
	EchoVersion int64
}

const linkStateCols = `entity, cloud_id, local_id, version, gen, COALESCE(base, ''), echo_version`

func scanLinkState(row *sql.Row) (CloudLinkState, bool, error) {
	var l CloudLinkState
	err := row.Scan(&l.Entity, &l.CloudID, &l.LocalID, &l.Version, &l.Gen, &l.Base, &l.EchoVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, fmt.Errorf("cloud link: %w", err)
	}
	return l, true, nil
}

// State returns the link of (entity, cloudID) with its base and echo.
func (*CloudLinkRepo) State(ctx context.Context, q SyncQuerier, entity, cloudID string) (CloudLinkState, bool, error) {
	return scanLinkState(q.QueryRowContext(ctx, `SELECT `+linkStateCols+` FROM cloud_links WHERE entity = ? AND cloud_id = ?`, entity, cloudID))
}

// StateByLocal returns the link of entity pointing at localID (any one,
// should two ever). Pinned to idx_cloud_links_local: it runs per row of
// every upload and answer, and an ORDER BY on the primary key made the
// planner scan the table.
func (*CloudLinkRepo) StateByLocal(ctx context.Context, q SyncQuerier, entity, localID string) (CloudLinkState, bool, error) {
	return scanLinkState(q.QueryRowContext(ctx, `SELECT `+linkStateCols+` FROM cloud_links INDEXED BY idx_cloud_links_local WHERE entity = ? AND local_id = ? LIMIT 1`, entity, localID))
}

// SetBase stores a link's base ("" clears it).
func (*CloudLinkRepo) SetBase(ctx context.Context, q SyncQuerier, entity, cloudID, base string) error {
	var v any
	if base != "" {
		v = base
	}
	if _, err := q.ExecContext(ctx, `UPDATE cloud_links SET base = ? WHERE entity = ? AND cloud_id = ?`, v, entity, cloudID); err != nil {
		return fmt.Errorf("cloud link base: %w", err)
	}
	return nil
}

// SetEcho stores the version of the till's own accepted upload of a row.
func (*CloudLinkRepo) SetEcho(ctx context.Context, q SyncQuerier, entity, cloudID string, version int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE cloud_links SET echo_version = ? WHERE entity = ? AND cloud_id = ?`, version, entity, cloudID); err != nil {
		return fmt.Errorf("cloud link echo: %w", err)
	}
	return nil
}

// LinksWithoutBase returns up to limit links of entity with a cloud
// version (pulled, not a till create awaiting its answer) and no base yet,
// with cloud_id > after, in cloud_id order (an applier-3 build linked them
// before migration 084 added the base).
func (*CloudLinkRepo) LinksWithoutBase(ctx context.Context, q SyncQuerier, entity, after string, limit int) ([]CloudLink, error) {
	rows, err := q.QueryContext(ctx, `
SELECT entity, cloud_id, local_id, version, gen FROM cloud_links
WHERE entity = ? AND cloud_id > ? AND version > 0 AND base IS NULL
ORDER BY cloud_id LIMIT ?`, entity, after, limit)
	if err != nil {
		return nil, fmt.Errorf("cloud links without base: %w", err)
	}
	defer rows.Close()
	var out []CloudLink
	for rows.Next() {
		var l CloudLink
		if err := rows.Scan(&l.Entity, &l.CloudID, &l.LocalID, &l.Version, &l.Gen); err != nil {
			return nil, fmt.Errorf("cloud links without base: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ClearEchoes forgets every echo version (a re-pull from 0 applies every
// row again).
func (*CloudLinkRepo) ClearEchoes(ctx context.Context, q SyncQuerier) error {
	if _, err := q.ExecContext(ctx, `UPDATE cloud_links SET echo_version = 0 WHERE echo_version <> 0`); err != nil {
		return fmt.Errorf("cloud link echoes: %w", err)
	}
	return nil
}

// ItemBarcodeLinks returns the item_barcode links whose local record is
// one of itemID's item-level barcodes ("item:<itemID>:<key>").
func (*CloudLinkRepo) ItemBarcodeLinks(ctx context.Context, q SyncQuerier, entity, itemID string) ([]CloudLink, error) {
	prefix := "item:" + itemID + ":"
	rows, err := q.QueryContext(ctx, `
SELECT entity, cloud_id, local_id, version, gen FROM cloud_links
WHERE entity = ? AND substr(local_id, 1, ?) = ? ORDER BY cloud_id`, entity, len(prefix), prefix)
	if err != nil {
		return nil, fmt.Errorf("cloud item barcode links: %w", err)
	}
	defer rows.Close()
	var out []CloudLink
	for rows.Next() {
		var l CloudLink
		if err := rows.Scan(&l.Entity, &l.CloudID, &l.LocalID, &l.Version, &l.Gen); err != nil {
			return nil, fmt.Errorf("cloud item barcode links: %w", err)
		}
		if strings.HasPrefix(l.LocalID, prefix) {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}
