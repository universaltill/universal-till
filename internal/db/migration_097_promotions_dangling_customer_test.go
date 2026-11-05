package db

import (
	"database/sql"
	"testing"
)

type promoRow struct {
	typ, desc, starts, ends sql.NullString
	value                   int64
	cust                    sql.NullString
	active, sample          int
}

func readPromo(t *testing.T, d *DB, code string) promoRow {
	t.Helper()
	var p promoRow
	err := d.DB.QueryRow(`SELECT type, value, description, starts_at, ends_at, customer_id, is_active, is_sample_data FROM promotions WHERE code = ?`, code).
		Scan(&p.typ, &p.value, &p.desc, &p.starts, &p.ends, &p.cust, &p.active, &p.sample)
	if err != nil {
		t.Fatalf("read promo %s: %v", code, err)
	}
	return p
}

func scalarInt(t *testing.T, d *DB, q string) int {
	t.Helper()
	var n int
	if err := d.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func seedPromoDangling(t *testing.T, d *DB, withNullIDCustomer bool) {
	t.Helper()
	stmts := `
INSERT INTO customers (id, name) VALUES ('c-real', 'Real Customer');
PRAGMA foreign_keys = OFF;
INSERT INTO promotions (code, type, value, description, starts_at, ends_at, customer_id, is_active, is_sample_data) VALUES
 ('ORPHAN-ACTIVE', 'percent', 1500, 'gone', '2020-01-01T00:00:00Z', '2099-01-01T00:00:00Z', 'c-gone', 1, 1),
 ('ORPHAN-INACTIVE', 'amount', 200, NULL, NULL, NULL, 'c-gone2', 0, 0),
 ('VALID-ACTIVE', 'amount', 300, NULL, NULL, NULL, 'c-real', 1, 0),
 ('VALID-INACTIVE', 'amount', 400, NULL, NULL, NULL, 'c-real', 0, 0),
 ('GLOBAL', 'amount', 500, NULL, NULL, NULL, NULL, 1, 0);
PRAGMA foreign_keys = ON;`
	if withNullIDCustomer {
		stmts = `INSERT INTO customers (id, name) VALUES (NULL, 'Null Id Row');` + stmts
	}
	if _, err := d.DB.Exec(stmts); err != nil {
		t.Fatal(err)
	}
}

func TestMigration097_PromotionsDanglingCustomer(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, 97, "promo-dangling.db")
	// (e) a customers row with a NULL id must not defeat the repair (NOT IN would).
	seedPromoDangling(t, d, true)
	custBefore := scalarInt(t, d, `SELECT COUNT(*) FROM customers`)
	salesBefore := scalarInt(t, d, `SELECT COUNT(*) FROM sales`)
	genBefore := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`)
	d.Close()

	d = openMigratedTo(t, path, 97)
	defer d.Close()

	a := readPromo(t, d, "ORPHAN-ACTIVE")
	if a.cust.Valid || a.active != 0 {
		t.Fatalf("active orphan: customer=%v active=%d, want NULL/0", a.cust, a.active)
	}
	if a.typ.String != "percent" || a.value != 1500 || a.desc.String != "gone" ||
		a.starts.String != "2020-01-01T00:00:00Z" || a.ends.String != "2099-01-01T00:00:00Z" || a.sample != 1 {
		t.Fatalf("other columns changed: %+v", a)
	}
	if o := readPromo(t, d, "ORPHAN-INACTIVE"); o.cust.Valid || o.active != 0 {
		t.Fatalf("inactive orphan: %+v", o)
	}
	if v := readPromo(t, d, "VALID-ACTIVE"); v.cust.String != "c-real" || v.active != 1 {
		t.Fatalf("valid active touched: %+v", v)
	}
	if v := readPromo(t, d, "VALID-INACTIVE"); v.cust.String != "c-real" || v.active != 0 {
		t.Fatalf("valid inactive touched: %+v", v)
	}
	if g := readPromo(t, d, "GLOBAL"); g.cust.Valid || g.active != 1 {
		t.Fatalf("global promo touched: %+v", g)
	}

	// (f) the 023 trigger bumped the sync generation.
	if gen := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`); gen <= genBefore {
		t.Fatalf("sync_admin_version.generation %d, want > %d", gen, genBefore)
	}
	// (g) idempotent.
	res, err := d.DB.Exec(loadMigrationVersion(t, 97).SQL)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("re-run changed %d rows", n)
	}
	// (h) no FK violations left; customers and sales untouched.
	rows, err := d.DB.Query(`PRAGMA foreign_key_check(promotions)`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("foreign_key_check(promotions) still reports a violation")
	}
	rows.Close()
	if scalarInt(t, d, `SELECT COUNT(*) FROM customers`) != custBefore || scalarInt(t, d, `SELECT COUNT(*) FROM sales`) != salesBefore {
		t.Fatal("customers or sales changed")
	}
}

// A till with nothing to repair sees no sync bump; a fresh DB opens clean.
func TestMigration097_NoOrphansNoBump(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, 97, "promo-clean.db")
	if _, err := d.DB.Exec(`INSERT INTO customers (id, name) VALUES ('c1', 'A'); INSERT INTO promotions (code, type, value, customer_id) VALUES ('P1', 'amount', 100, 'c1'), ('P2', 'amount', 100, NULL)`); err != nil {
		t.Fatal(err)
	}
	gen := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`)
	d.Close()
	d = openMigratedTo(t, path, 97)
	defer d.Close()
	if got := scalarInt(t, d, `SELECT generation FROM sync_admin_version WHERE id = 1`); got != gen {
		t.Fatalf("generation %d -> %d on a till with no orphans", gen, got)
	}
	if readPromo(t, d, "P1").active != 1 {
		t.Fatal("clean promo changed")
	}

	fresh, err := Open(t.TempDir() + "/fresh.db")
	if err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	fresh.Close()
}
