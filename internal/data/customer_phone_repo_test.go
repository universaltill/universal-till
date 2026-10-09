package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#3200 (ADR-0131 §3–§4): customers.phone_e164, its chunked
// back-fill and the caller-ID lookup.

func phoneDB(t *testing.T, country string) *db.DB {
	t.Helper()
	d := openMigratedDB(t, "phone.db")
	if country != "" {
		mustExec(t, d, `INSERT INTO settings (key, value) VALUES (?, ?)`, StoreCountrySettingsKey, country)
	}
	return d
}

func addCustomer(t *testing.T, d *db.DB, id, name string, phone any) {
	t.Helper()
	mustExec(t, d, `INSERT INTO customers (id, name, phone, address, notes) VALUES (?, ?, ?, ?, ?)`,
		id, name, phone, "1 "+name+" Street", "notes of "+name)
}

func phoneE164Of(t *testing.T, d *db.DB, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := d.DB.QueryRow(`SELECT phone_e164 FROM customers WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read phone_e164 of %s: %v", id, err)
	}
	return v
}

func lookupNames(t *testing.T, d *db.DB, raw string) []string {
	t.Helper()
	got, err := NewPOSRepo(d.DB).LookupCustomersByPhone(context.Background(), raw)
	if err != nil {
		t.Fatalf("lookup %q: %v", raw, err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	return names
}

func backfill(t *testing.T, d *db.DB, batch int) int {
	t.Helper()
	n, err := NewPOSRepo(d.DB).BackfillCustomerPhoneE164(context.Background(), batch)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	return n
}

func TestBackfillCustomerPhoneE164_ValuesAndIdempotent(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c-nat", "National", "020 7946 0018")
	addCustomer(t, d, "c-intl", "International", "+1 (212) 555-0123")
	addCustomer(t, d, "c-bad", "Unparseable", "ext. 7946-0018")
	addCustomer(t, d, "c-empty", "Empty", "")
	addCustomer(t, d, "c-null", "NoPhone", nil)

	// batch 2 forces several chunks.
	if n := backfill(t, d, 2); n != 5 {
		t.Fatalf("first back-fill updated %d rows, want 5", n)
	}
	for id, want := range map[string]string{
		"c-nat":   "+442079460018",
		"c-intl":  "+12125550123",
		"c-bad":   "79460018", // digits only: matched by trailing digits
		"c-empty": "",
		"c-null":  "",
	} {
		if got := phoneE164Of(t, d, id); !got.Valid || got.String != want {
			t.Errorf("%s: phone_e164 = %+v, want %q", id, got, want)
		}
	}
	if n := backfill(t, d, 2); n != 0 {
		t.Errorf("second back-fill updated %d rows, want 0 (idempotent)", n)
	}
	var marker string
	if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, CustomerPhoneE164RegionSettingsKey).Scan(&marker); err != nil || marker != "GB" {
		t.Errorf("region marker = %q (%v), want GB", marker, err)
	}
}

func TestBackfillCustomerPhoneE164_NeverOverwritesAConcurrentPhoneEdit(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "Edited", "020 7946 0018")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()

	rows, err := repo.readPhoneE164Chunk(ctx, "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read chunk: rows=%v err=%v", rows, err)
	}
	// The run's reset recorded the region before it read (ut-docs#3992).
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES (?, ?)`, CustomerPhoneE164RegionSettingsKey, "GB")
	// A cashier edits the number between the back-fill's read and write.
	mustExec(t, d, `UPDATE customers SET phone = '07700 900123' WHERE id = 'c1'`)

	n, err := repo.writePhoneE164Chunk(ctx, rows, "GB")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("write chunk updated %d rows, want 0: the stale value must not land", n)
	}
	if got := phoneE164Of(t, d, "c1"); got.Valid {
		t.Fatalf("phone_e164 = %q after a concurrent edit, want NULL (recomputed later)", got.String)
	}
	// The next run computes it from the new number.
	if n := backfill(t, d, 10); n != 1 {
		t.Fatalf("follow-up back-fill updated %d, want 1", n)
	}
	if got := phoneE164Of(t, d, "c1"); got.String != "+447700900123" {
		t.Errorf("phone_e164 = %q, want the new number +447700900123", got.String)
	}
}

func TestBackfillCustomerPhoneE164_RegionChangeRecomputes(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "Mobile", "07700 900123")
	addCustomer(t, d, "c2", "Intl", "+90 532 123 45 67")
	backfill(t, d, 10)
	if got := phoneE164Of(t, d, "c1"); got.String != "+447700900123" {
		t.Fatalf("GB: %q", got.String)
	}
	mustExec(t, d, `UPDATE settings SET value = 'DE' WHERE key = ?`, StoreCountrySettingsKey)
	if n := backfill(t, d, 10); n != 2 {
		t.Errorf("region change re-normalised %d rows, want every row (2)", n)
	}
	if got := phoneE164Of(t, d, "c1"); got.String != "+497700900123" {
		t.Errorf("after GB -> DE: phone_e164 = %q, want +497700900123", got.String)
	}
	if got := phoneE164Of(t, d, "c2"); got.String != "+905321234567" {
		t.Errorf("an international number must not change with the region: %q", got.String)
	}
	if n := backfill(t, d, 10); n != 0 {
		t.Errorf("third run updated %d, want 0", n)
	}
}

func TestBackfillCustomerPhoneE164_StopsOnCancelledContext(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "A", "020 7946 0018")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewPOSRepo(d.DB).BackfillCustomerPhoneE164(ctx, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCustomersPhoneE164Trigger(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "A", "020 7946 0018")
	backfill(t, d, 10)

	// Phone alone changes: the stale value is dropped.
	mustExec(t, d, `UPDATE customers SET phone = '07700 900123' WHERE id = 'c1'`)
	if got := phoneE164Of(t, d, "c1"); got.Valid {
		t.Errorf("phone changed alone: phone_e164 = %q, want NULL", got.String)
	}
	// Both change together: the written value is kept.
	mustExec(t, d, `UPDATE customers SET phone = '020 7946 0018', phone_e164 = '+442079460018' WHERE id = 'c1'`)
	if got := phoneE164Of(t, d, "c1"); got.String != "+442079460018" {
		t.Errorf("phone and phone_e164 changed together: phone_e164 = %+v, want kept", got)
	}
	// Another column changes: untouched.
	mustExec(t, d, `UPDATE customers SET notes = 'rings twice' WHERE id = 'c1'`)
	if got := phoneE164Of(t, d, "c1"); got.String != "+442079460018" {
		t.Errorf("notes edit cleared phone_e164: %+v", got)
	}
	// Same phone rewritten: untouched.
	mustExec(t, d, `UPDATE customers SET phone = '020 7946 0018' WHERE id = 'c1'`)
	if got := phoneE164Of(t, d, "c1"); got.String != "+442079460018" {
		t.Errorf("an identical phone rewrite cleared phone_e164: %+v", got)
	}
}

func TestLookupCustomersByPhone_AllMatchesAcrossFormats(t *testing.T) {
	for _, backfilled := range []bool{true, false} {
		t.Run(fmt.Sprintf("backfilled=%v", backfilled), func(t *testing.T) {
			d := phoneDB(t, "GB")
			addCustomer(t, d, "c-b", "Bob", "+44 20 7946 0018")
			addCustomer(t, d, "c-a", "Alice", "020 7946 0018")
			addCustomer(t, d, "c-x", "Other", "020 7946 0019")
			addCustomer(t, d, "c-erased", "", "020 7946 0018") // GDPR shell
			if backfilled {
				backfill(t, d, 10)
			}
			for _, raw := range []string{"+442079460018", "02079460018", "0044 (20) 7946-0018", "020 7946 0018"} {
				got := lookupNames(t, d, raw)
				if len(got) != 2 || got[0] != "Alice" || got[1] != "Bob" {
					t.Errorf("lookup %q = %v, want [Alice Bob] (both, by name, erased shell excluded)", raw, got)
				}
			}
		})
	}
}

func TestLookupCustomersByPhone_ReturnsDisplayFields(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "Alice", "020 7946 0018")
	got, err := NewPOSRepo(d.DB).LookupCustomersByPhone(context.Background(), "+442079460018")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
	c := got[0]
	if c.ID != "c1" || c.Name != "Alice" || c.Phone != "020 7946 0018" || c.Address != "1 Alice Street" || c.Notes != "notes of Alice" {
		t.Errorf("display fields = %+v", c)
	}
	if c.RecentSales != nil {
		t.Errorf("no sales: RecentSales = %+v, want nil", c.RecentSales)
	}
}

func TestLookupCustomersByPhone_TrailingDigitsFallback(t *testing.T) {
	t.Run("stored number not normalisable", func(t *testing.T) {
		d := phoneDB(t, "GB")
		addCustomer(t, d, "c1", "Work", "020 7946 0018 (work)")
		for _, backfilled := range []bool{false, true} {
			if backfilled {
				backfill(t, d, 10)
			}
			if got := lookupNames(t, d, "+44 20 7946 0018"); len(got) != 1 || got[0] != "Work" {
				t.Errorf("backfilled=%v: lookup = %v, want [Work] by trailing 9 digits", backfilled, got)
			}
		}
	})
	t.Run("incoming number not normalisable", func(t *testing.T) {
		d := phoneDB(t, "") // no shop country: a national number can't be normalised
		addCustomer(t, d, "c1", "Abroad", "+44 20 7946 0018")
		backfill(t, d, 10)
		if got := lookupNames(t, d, "020 7946 0018"); len(got) != 1 || got[0] != "Abroad" {
			t.Errorf("lookup = %v, want [Abroad] by trailing 9 digits", got)
		}
	})
	t.Run("exact match wins over trailing", func(t *testing.T) {
		d := phoneDB(t, "GB")
		addCustomer(t, d, "c1", "Exact", "020 7946 0018")
		addCustomer(t, d, "c2", "Trailing", "+1 302 079 460 018") // shares the last 9 digits
		backfill(t, d, 10)
		if got := lookupNames(t, d, "020 7946 0018"); len(got) != 1 || got[0] != "Exact" {
			t.Errorf("lookup = %v, want only the exact match", got)
		}
	})
	t.Run("exact match keeps unnormalisable duplicates", func(t *testing.T) {
		// Review finding (ut-docs#3200): a stored number that could not be
		// normalised is still the same line — an exact match elsewhere must
		// not hide it (ADR-0131 §4 lists every match).
		d := phoneDB(t, "GB")
		addCustomer(t, d, "c1", "Alice", "020 7946 0018")
		addCustomer(t, d, "c2", "Work", "020 7946 0018 (work)")
		addCustomer(t, d, "c3", "Trailing", "+1 302 079 460 018") // E.164 elsewhere: stays out
		for _, backfilled := range []bool{false, true} {
			if backfilled {
				backfill(t, d, 10)
			}
			got := lookupNames(t, d, "+442079460018")
			if len(got) != 2 || got[0] != "Alice" || got[1] != "Work" {
				t.Errorf("backfilled=%v: lookup = %v, want [Alice Work]", backfilled, got)
			}
		}
	})
	t.Run("too few digits: no fallback", func(t *testing.T) {
		d := phoneDB(t, "GB")
		addCustomer(t, d, "c1", "Short", "12345")
		backfill(t, d, 10)
		if got := lookupNames(t, d, "12345"); len(got) != 0 {
			t.Errorf("lookup = %v, want none", got)
		}
	})
}

func TestLookupCustomersByPhone_WithheldAndEmpty(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c-empty", "Empty", "")
	backfill(t, d, 10)
	for _, raw := range []string{"", "   ", "withheld", "anonymous"} {
		got, err := NewPOSRepo(d.DB).LookupCustomersByPhone(context.Background(), raw)
		if err != nil || got != nil {
			t.Errorf("lookup %q = %+v, %v; want nil, nil", raw, got, err)
		}
	}
}

func TestLookupCustomersByPhone_CapsAtTen(t *testing.T) {
	d := phoneDB(t, "GB")
	for i := 11; i >= 0; i-- {
		addCustomer(t, d, fmt.Sprintf("c%02d", i), fmt.Sprintf("Dup %02d", i), "020 7946 0018")
	}
	got := lookupNames(t, d, "02079460018")
	if len(got) != 10 || got[0] != "Dup 00" || got[9] != "Dup 09" {
		t.Errorf("lookup = %v, want the first 10 by name", got)
	}
}

func TestLookupCustomersByPhone_RecentSales(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "Alice", "020 7946 0018")
	addCustomer(t, d, "c2", "Bob", "020 7946 0019")
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('i1', 'SKU-I1', 'Thing', 100)`)
	sale := func(id, customer, status, saleType string, completed any, created string, total int64, lines int) {
		t.Helper()
		mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, customer_id, subtotal, total, completed_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, "R-"+id, status, saleType, customer, total, total, completed, created)
		for i := 1; i <= lines; i++ {
			mustExec(t, d, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
				VALUES (?, ?, ?, 'i1', 'Thing', 1, 100, 0, 0, 100, 100)`, fmt.Sprintf("%s-l%d", id, i), id, i)
		}
	}
	sale("s1", "c1", "completed", "sale", "2026-09-01 10:00:00", "2026-09-01 09:59:00", 1000, 1)
	sale("s2", "c1", "completed", "sale", "2026-09-02 10:00:00", "2026-09-02 09:59:00", 2000, 2)
	sale("s3", "c1", "completed", "sale", "2026-09-03 10:00:00", "2026-09-03 09:59:00", 3000, 3)
	sale("s4", "c1", "completed", "sale", "2026-09-04 10:00:00", "2026-09-04 09:59:00", 4000, 1)
	sale("s5", "c1", "completed", "sale", nil, "2026-09-05 10:00:00", 5000, 2) // no completed_at: created_at orders it
	sale("s6", "c1", "completed", "sale", "2026-09-06 10:00:00", "2026-09-06 09:59:00", 6000, 4)
	sale("s7", "c1", "voided", "sale", "2026-09-07 10:00:00", "2026-09-07 09:59:00", 7000, 1)
	sale("s8", "c1", "completed", "return", "2026-09-08 10:00:00", "2026-09-08 09:59:00", -1000, 1)
	sale("s9", "c2", "completed", "sale", "2026-09-09 10:00:00", "2026-09-09 09:59:00", 9000, 1)

	got, err := NewPOSRepo(d.DB).LookupCustomersByPhone(context.Background(), "02079460018")
	if err != nil || len(got) != 1 {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	type want struct {
		id, date string
		total    money.Money
		items    int
	}
	wants := []want{
		{"s6", "2026-09-06 10:00:00", money.FromMinor(6000), 4},
		{"s5", "2026-09-05 10:00:00", money.FromMinor(5000), 2},
		{"s4", "2026-09-04 10:00:00", money.FromMinor(4000), 1},
		{"s3", "2026-09-03 10:00:00", money.FromMinor(3000), 3},
		{"s2", "2026-09-02 10:00:00", money.FromMinor(2000), 2},
	}
	rs := got[0].RecentSales
	if len(rs) != len(wants) {
		t.Fatalf("RecentSales = %+v, want 5 (voided, return and other customers' sales excluded, oldest dropped)", rs)
	}
	for i, w := range wants {
		r := rs[i]
		if r.SaleID != w.id || r.ReceiptNo != "R-"+w.id || r.Date != w.date || r.Total != w.total || r.ItemCount != w.items {
			t.Errorf("RecentSales[%d] = %+v, want %+v", i, r, w)
		}
	}
}

// ut-docs#3992: a run that started with region A must stop writing once a
// later run reset the column for region B, or its A-values would stay
// forever (the later run's marker says B, so nothing recomputes them).
func TestWritePhoneE164Chunk_StaleRegionWritesNothing(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "Mobile", "07700 900123")
	// Another run reset the column for GB and recorded it.
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES (?, ?)`, CustomerPhoneE164RegionSettingsKey, "GB")
	rows := []phoneE164Row{{id: "c1", phone: sql.NullString{String: "07700 900123", Valid: true}}}
	// A stale run still computing with DE.
	n, err := NewPOSRepo(d.DB).writePhoneE164Chunk(context.Background(), rows, "DE")
	if !errors.Is(err, ErrPhoneE164RegionChanged) {
		t.Fatalf("err = %v, want ErrPhoneE164RegionChanged", err)
	}
	if n != 0 {
		t.Errorf("wrote %d rows, want 0", n)
	}
	if got := phoneE164Of(t, d, "c1"); got.Valid {
		t.Errorf("phone_e164 = %q, want it left NULL", got.String)
	}
	// The run whose region matches the marker still writes.
	if n, err := NewPOSRepo(d.DB).writePhoneE164Chunk(context.Background(), rows, "GB"); err != nil || n != 1 {
		t.Fatalf("matching region: n=%d err=%v", n, err)
	}
}

// ut-docs#3992 review: a run that read an older country must neither reset
// over a newer run's marker nor write values for that older country.
func TestBackfillPhoneE164_OutdatedCountryNeitherResetsNorWrites(t *testing.T) {
	d := phoneDB(t, "GB")
	addCustomer(t, d, "c1", "Mobile", "07700 900123")
	if _, err := NewPOSRepo(d.DB).BackfillCustomerPhoneE164(context.Background(), 10); err != nil {
		t.Fatalf("back-fill: %v", err)
	}
	want := phoneE164Of(t, d, "c1")
	// A run still holding the previous country, DE.
	if err := NewPOSRepo(d.DB).resetPhoneE164OnRegionChange(context.Background(), "DE"); !errors.Is(err, ErrPhoneE164RegionChanged) {
		t.Fatalf("reset err = %v, want ErrPhoneE164RegionChanged", err)
	}
	if got := phoneE164Of(t, d, "c1"); got != want {
		t.Errorf("phone_e164 = %v after a stale reset, want %v kept", got, want)
	}
	var marker string
	if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, CustomerPhoneE164RegionSettingsKey).Scan(&marker); err != nil || marker != "GB" {
		t.Errorf("marker = %q (%v), want GB kept", marker, err)
	}
	// Marker says DE but the shop is GB: the DE run must not write.
	mustExec(t, d, `UPDATE settings SET value = 'DE' WHERE key = ?`, CustomerPhoneE164RegionSettingsKey)
	mustExec(t, d, `UPDATE customers SET phone_e164 = NULL`)
	rows := []phoneE164Row{{id: "c1", phone: sql.NullString{String: "07700 900123", Valid: true}}}
	if n, err := NewPOSRepo(d.DB).writePhoneE164Chunk(context.Background(), rows, "DE"); !errors.Is(err, ErrPhoneE164RegionChanged) || n != 0 {
		t.Fatalf("write n=%d err=%v, want 0 and ErrPhoneE164RegionChanged", n, err)
	}
}
