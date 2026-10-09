package data

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/phonenumber"
)

// CustomerPhoneE164RegionSettingsKey records the shop country
// customers.phone_e164 was last computed with (ut-docs#3200). When
// store.country differs, BackfillCustomerPhoneE164 recomputes every row.
// Per till (PerTillSettingPrefixes): each till back-fills its own table.
const CustomerPhoneE164RegionSettingsKey = "customers.phone_e164_region"

// DefaultPhoneE164BackfillBatch is the back-fill's chunk size: one short
// write transaction per chunk, so a sale never waits long on the lock.
const DefaultPhoneE164BackfillBatch = 200

// callerLookupLimit caps how many customers one number returns.
const callerLookupLimit = 10

// callerRecentSales is how many past sales the caller pop-up shows.
const callerRecentSales = 5

// CallerCustomer is one customer matching a caller's number (ADR-0131 §4).
type CallerCustomer struct {
	ID          string
	Name        string
	Phone       string
	Address     string
	Notes       string
	RecentSales []CallerSale
}

// CallerSale is one of a caller's last completed sales.
type CallerSale struct {
	SaleID    string
	ReceiptNo string
	Date      string // completed_at (else created_at), as stored
	Total     money.Money
	ItemCount int // number of sale lines
}

// phoneE164Value is what customers.phone_e164 stores for phone: E.164 when
// it normalises with region, else its digits ("" when it has none).
func phoneE164Value(phone, region string) string {
	if e, ok := phonenumber.Normalise(phone, region); ok {
		return e
	}
	return phonenumber.Digits(phone)
}

// storeCountry is the shop's country, upper-cased ("" when unset).
func (r *POSRepo) storeCountry(ctx context.Context) (string, error) {
	v, _, err := r.settings.Get(ctx, StoreCountrySettingsKey)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(strings.TrimSpace(v)), nil
}

// phoneE164Row is one customer the back-fill still has to compute.
type phoneE164Row struct {
	id    string
	phone sql.NullString
}

// BackfillCustomerPhoneE164 computes customers.phone_e164 for every row
// where it is NULL, in chunks of batch rows (ut-docs#3200, ADR-0131 §3),
// and returns how many rows it wrote. When the shop country differs from
// the one the column was last computed with, every row is first reset to
// NULL. A row whose phone changed between the chunk's read and write is
// left NULL (never overwritten with a stale value) for the next run; the
// readers normalise NULL rows themselves. Idempotent: a second run writes
// nothing. Stops with ctx's error between chunks.
func (r *POSRepo) BackfillCustomerPhoneE164(ctx context.Context, batch int) (int, error) {
	if batch <= 0 {
		batch = DefaultPhoneE164BackfillBatch
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	region, err := r.storeCountry(ctx)
	if err != nil {
		return 0, fmt.Errorf("phone_e164 back-fill: %w", err)
	}
	if err := r.resetPhoneE164OnRegionChange(ctx, region); err != nil {
		return 0, err
	}
	updated := 0
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return updated, err
		}
		rows, err := r.readPhoneE164Chunk(ctx, after, batch)
		if err != nil {
			return updated, err
		}
		if len(rows) == 0 {
			return updated, nil
		}
		n, err := r.writePhoneE164Chunk(ctx, rows, region)
		updated += n
		if err != nil {
			return updated, err
		}
		// Keyset paging: a row skipped above is not read again this run,
		// so the loop always ends.
		after = rows[len(rows)-1].id
	}
}

// resetPhoneE164OnRegionChange NULLs every phone_e164 and records region
// as the marker, in one transaction, unless the marker already says region.
func (r *POSRepo) resetPhoneE164OnRegionChange(ctx context.Context, region string) error {
	marker, ok, err := r.settings.Get(ctx, CustomerPhoneE164RegionSettingsKey)
	if err != nil {
		return fmt.Errorf("phone_e164 back-fill: %w", err)
	}
	if ok && marker == region {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("phone_e164 reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE customers SET phone_e164 = NULL WHERE phone_e164 IS NOT NULL`); err != nil {
		return fmt.Errorf("phone_e164 reset: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		CustomerPhoneE164RegionSettingsKey, region, time.Now().UTC()); err != nil {
		return fmt.Errorf("phone_e164 region marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("phone_e164 reset: %w", err)
	}
	invalidateCachedSetting(r.db, CustomerPhoneE164RegionSettingsKey)
	return nil
}

// readPhoneE164Chunk reads up to batch rows still to compute, by id after
// the given one.
func (r *POSRepo) readPhoneE164Chunk(ctx context.Context, after string, batch int) ([]phoneE164Row, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, phone FROM customers
WHERE phone_e164 IS NULL AND id > ?
ORDER BY id LIMIT ?`, after, batch)
	if err != nil {
		return nil, fmt.Errorf("phone_e164 back-fill read: %w", err)
	}
	defer rows.Close()
	var out []phoneE164Row
	for rows.Next() {
		var p phoneE164Row
		if err := rows.Scan(&p.id, &p.phone); err != nil {
			return nil, fmt.Errorf("phone_e164 back-fill read: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// writePhoneE164Chunk writes each row's value in one transaction, only
// where phone is still what was read and phone_e164 is still NULL, so a
// concurrent edit is never overwritten. Returns the rows written.
func (r *POSRepo) writePhoneE164Chunk(ctx context.Context, rows []phoneE164Row, region string) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("phone_e164 back-fill write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
UPDATE customers SET phone_e164 = ?
WHERE id = ? AND phone IS ? AND phone_e164 IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("phone_e164 back-fill write: %w", err)
	}
	defer stmt.Close()
	n := 0
	for _, p := range rows {
		res, err := stmt.ExecContext(ctx, phoneE164Value(p.phone.String, region), p.id, p.phone)
		if err != nil {
			return 0, fmt.Errorf("phone_e164 back-fill write %s: %w", p.id, err)
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("phone_e164 back-fill write: %w", err)
	}
	return n, nil
}

// LookupCustomersByPhone returns every customer whose phone matches raw
// (ADR-0131 §4), at most 10, by name: an exact E.164 match when raw
// normalises with the shop's country, plus a match on the trailing 9
// digits — against every stored number when nothing matched exactly, else
// only against stored numbers that could not be normalised. Rows the
// back-fill has not reached yet (phone_e164 NULL) are normalised here,
// read-only. Erased customer shells (empty name) never match. Each customer
// carries their last 5 completed sales. An empty or withheld raw returns nil.
func (r *POSRepo) LookupCustomersByPhone(ctx context.Context, raw string) ([]CallerCustomer, error) {
	if phonenumber.Digits(raw) == "" {
		return nil, nil
	}
	region, err := r.storeCountry(ctx)
	if err != nil {
		return nil, fmt.Errorf("caller lookup: %w", err)
	}
	pending, err := r.customersPendingPhoneE164(ctx)
	if err != nil {
		return nil, err
	}

	var found []CallerCustomer
	if e164, ok := phonenumber.Normalise(raw, region); ok {
		if found, err = r.callerCustomers(ctx, `phone_e164 = ?`, e164); err != nil {
			return nil, err
		}
		for _, p := range pending {
			if phoneE164Value(p.Phone, region) == e164 {
				found = append(found, p)
			}
		}
	}
	// Trailing-9 fallback (ADR-0131 §3). With exact matches it still adds
	// stored numbers that could not be normalised (digits only, no '+'):
	// they may be the same line and the pop-up lists every match (§4). A
	// stored E.164 number that only shares the last 9 digits stays out.
	if key := phonenumber.TrailingKey(raw); key != "" {
		exact := len(found) > 0
		where := `phone_e164 <> '' AND length(phone_e164) >= ? AND substr(phone_e164, -?) = ?`
		if exact {
			where += ` AND substr(phone_e164, 1, 1) <> '+'`
		}
		more, err := r.callerCustomers(ctx, where, len(key), len(key), key)
		if err != nil {
			return nil, err
		}
		for _, p := range pending {
			v := phoneE164Value(p.Phone, region)
			if strings.HasSuffix(v, key) && (!exact || !strings.HasPrefix(v, "+")) {
				more = append(more, p)
			}
		}
		seen := make(map[string]bool, len(found))
		for _, c := range found {
			seen[c.ID] = true
		}
		for _, c := range more {
			if !seen[c.ID] {
				seen[c.ID] = true
				found = append(found, c)
			}
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Name != found[j].Name {
			return found[i].Name < found[j].Name
		}
		return found[i].ID < found[j].ID
	})
	if len(found) > callerLookupLimit {
		found = found[:callerLookupLimit]
	}
	for i := range found {
		if found[i].RecentSales, err = r.callerRecentSales(ctx, found[i].ID); err != nil {
			return nil, err
		}
	}
	return found, nil
}

const callerCustomerCols = `id, name, COALESCE(phone, ''), COALESCE(address, ''), COALESCE(notes, '')`

// callerCustomers lists non-erased customers matching where (a fixed
// predicate from LookupCustomersByPhone, never user text), by name.
func (r *POSRepo) callerCustomers(ctx context.Context, where string, args ...any) ([]CallerCustomer, error) {
	args = append(args, callerLookupLimit)
	return r.scanCallerCustomers(ctx, `SELECT `+callerCustomerCols+` FROM customers
WHERE name <> '' AND `+where+`
ORDER BY name, id LIMIT ?`, args...)
}

// customersPendingPhoneE164 lists non-erased customers with a phone whose
// phone_e164 the back-fill has not computed yet.
func (r *POSRepo) customersPendingPhoneE164(ctx context.Context) ([]CallerCustomer, error) {
	return r.scanCallerCustomers(ctx, `SELECT `+callerCustomerCols+` FROM customers
WHERE name <> '' AND phone_e164 IS NULL AND phone IS NOT NULL AND trim(phone) <> ''`)
}

func (r *POSRepo) scanCallerCustomers(ctx context.Context, q string, args ...any) ([]CallerCustomer, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("caller lookup: %w", err)
	}
	defer rows.Close()
	var out []CallerCustomer
	for rows.Next() {
		var c CallerCustomer
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Address, &c.Notes); err != nil {
			return nil, fmt.Errorf("caller lookup: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// callerRecentSales is a customer's last 5 completed sales (no voids, no
// returns), newest first.
func (r *POSRepo) callerRecentSales(ctx context.Context, customerID string) ([]CallerSale, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT s.id, s.receipt_no, COALESCE(s.completed_at, s.created_at), s.total,
       (SELECT COUNT(*) FROM sale_lines l WHERE l.sale_id = s.id)
FROM sales s
WHERE s.customer_id = ? AND s.status = 'completed' AND s.sale_type = 'sale'
ORDER BY COALESCE(s.completed_at, s.created_at) DESC, s.id DESC
LIMIT ?`, customerID, callerRecentSales)
	if err != nil {
		return nil, fmt.Errorf("caller recent sales: %w", err)
	}
	defer rows.Close()
	var out []CallerSale
	for rows.Next() {
		var s CallerSale
		var total int64
		if err := rows.Scan(&s.SaleID, &s.ReceiptNo, &s.Date, &total, &s.ItemCount); err != nil {
			return nil, fmt.Errorf("caller recent sales: %w", err)
		}
		s.Total = money.FromMinor(total)
		out = append(out, s)
	}
	return out, rows.Err()
}
