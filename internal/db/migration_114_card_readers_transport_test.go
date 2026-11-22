package db

import (
	"os"
	"path/filepath"
	"testing"
)

// ut-docs#6756 (ADR-0205 §1, §5): card_readers gains transport,
// device_serial and bound_at; the retired bluetooth kind becomes sdk.

func TestMigration114_AddsTransportSerialBoundAt(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m114.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	type col struct {
		typ     string
		notNull int
		dflt    *string
	}
	got := map[string]col{}
	rows, err := d.DB.Query(`SELECT name, type, "notnull", dflt_value FROM pragma_table_info('card_readers')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n, ty string
		var nn int
		var df *string
		if err := rows.Scan(&n, &ty, &nn, &df); err != nil {
			t.Fatal(err)
		}
		got[n] = col{ty, nn, df}
	}
	if c, ok := got["transport"]; !ok || c.typ != "TEXT" || c.notNull != 1 || c.dflt == nil || *c.dflt != "'internet'" {
		t.Errorf("transport = %+v", c)
	}
	for _, n := range []string{"device_serial", "bound_at"} {
		if c, ok := got[n]; !ok || c.typ != "TEXT" || c.notNull != 0 || c.dflt != nil {
			t.Errorf("%s = %+v", n, c)
		}
	}
}

type seed114 struct{ id, kind, removed string }

var seeds114 = []seed114{
	{"r-cloud", "cloud", ""},
	{"r-phone", "phone", ""},
	{"r-tap", "tap_to_pay", ""},
	{"r-bt", "bluetooth", ""},
	{"r-bt-gone", "bluetooth", "2026-01-02 03:04:05"},
	{"r-weird", "weird", ""},
}

const select114 = `SELECT id, name, method_key, plugin_id, reader_ref, '' AS kind_checked_below, source, sort_order, unattended_default,
 COALESCE(last_used_at,'~'), created_at, updated_at, COALESCE(removed_at,'~') FROM card_readers ORDER BY id`

func rowsSnapshot114(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.DB.Query(select114)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f [13]string
		var sort, un int
		if err := rows.Scan(&f[0], &f[1], &f[2], &f[3], &f[4], &f[5], &f[6], &sort, &un, &f[9], &f[10], &f[11], &f[12]); err != nil {
			t.Fatal(err)
		}
		out = append(out, f[0]+"|"+f[1]+"|"+f[2]+"|"+f[3]+"|"+f[4]+"|"+f[6]+"|"+f[9]+"|"+f[10]+"|"+f[11]+"|"+f[12])
	}
	return out
}

func TestMigration114_RewritesKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre114.db")
	old := openMigratedTo(t, path, 113)
	for i, s := range seeds114 {
		var removed any
		if s.removed != "" {
			removed = s.removed
		}
		if _, err := old.DB.Exec(`INSERT INTO card_readers(id,name,method_key,plugin_id,reader_ref,kind,source,sort_order,unattended_default,last_used_at,created_at,updated_at,removed_at)
			VALUES(?,?,?,?,?,?,'manual',?,0,?,'2026-01-01 00:00:00','2026-01-01 10:00:00',?)`,
			s.id, "Name "+s.id, "m", "p", "ref-"+s.id, s.kind, i+7, "2026-01-0"+string(rune('1'+i))+"T00:00:00", removed); err != nil {
			t.Fatal(err)
		}
	}
	before := rowsSnapshot114(t, old)
	old.Close()
	d := openMigratedTo(t, path, 114)
	defer d.Close()
	after := rowsSnapshot114(t, d)
	if len(before) != len(after) {
		t.Fatalf("row count %d -> %d", len(before), len(after))
	}
	for i := range before {
		// rowsSnapshot114 leaves out kind (rewritten for bluetooth rows) and
		// the new columns, so every other column must be byte-identical.
		if before[i] != after[i] {
			t.Errorf("row %d changed: %s -> %s", i, before[i], after[i])
		}
	}
	want := map[string][2]string{
		"r-cloud": {"cloud", "internet"}, "r-phone": {"phone", "internet"}, "r-tap": {"tap_to_pay", "device_own"},
		"r-bt": {"sdk", "bluetooth"}, "r-bt-gone": {"sdk", "bluetooth"}, "r-weird": {"weird", "internet"},
	}
	for id, w := range want {
		var kind, tr string
		var serial, bound *string
		if err := d.DB.QueryRow(`SELECT kind, transport, device_serial, bound_at FROM card_readers WHERE id=?`, id).Scan(&kind, &tr, &serial, &bound); err != nil {
			t.Fatal(err)
		}
		if kind != w[0] || tr != w[1] || serial != nil || bound != nil {
			t.Errorf("%s = %s/%s serial=%v bound=%v, want %v", id, kind, tr, serial, bound, w)
		}
	}
	// the retired kind keeps everything else (incl. updated_at, removed_at)
	for i, s := range seeds114 {
		if s.kind == "bluetooth" {
			b, a := before[i], after[i]
			if b != a { // kind is not in the snapshot string, so this must be equal
				t.Errorf("bluetooth row %s other columns changed: %s -> %s", s.id, b, a)
			}
		}
	}
}

func TestMigration114_ReplayIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay114.db")
	old := openMigratedTo(t, path, 113)
	if _, err := old.DB.Exec(`INSERT INTO card_readers(id,name,method_key,plugin_id,reader_ref,kind) VALUES('a','A','m','p','r1','bluetooth'),('b','B','m','p','r2','tap_to_pay')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	d := openMigratedTo(t, path, 114)
	defer d.Close()
	before := rowsSnapshot114(t, d)
	sqlText, err := os.ReadFile("migrations/114_card_readers_transport.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = 114`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrationSQL(t, d, 114, string(sqlText)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	after := rowsSnapshot114(t, d)
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("replay changed %s -> %s", before[i], after[i])
		}
	}
	var kind, tr string
	_ = d.DB.QueryRow(`SELECT kind, transport FROM card_readers WHERE id='a'`).Scan(&kind, &tr)
	if kind != "sdk" || tr != "bluetooth" {
		t.Errorf("after replay a = %s/%s", kind, tr)
	}
}
