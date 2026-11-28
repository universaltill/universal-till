package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// ut-docs#6243: a main till that starts mastering the catalogue in the cloud
// (cached mode till/unset -> cloud/adopting) marks the promoted fields the
// cloud never received, in the transaction that caches the mode.

const modeKey = "entitlement.catalog_master"

func modeKV(mode string) map[string]string {
	return map[string]string{modeKey: mode, "entitlement.plan": "p-new"}
}

func setCachedMode(t *testing.T, db *sql.DB, mode string) {
	t.Helper()
	if mode != "" {
		obExec(t, db, `INSERT INTO settings (key, value) VALUES (?, ?)`, modeKey, mode)
	}
}

func cachedSetting(t *testing.T, db *sql.DB, k string) string {
	t.Helper()
	var v string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&v)
	return v
}

func TestCatalogModeEntry_TillToCloudMarksItemAndCategory_6243(t *testing.T) {
	d := openMigratedDB(t, "mode-entry.db")
	db := d.DB
	setCachedMode(t, db, "till")
	linkItem(t, db, "i1", "x", `{"icon":""}`)
	linkCategory(t, db, "c1", "x", `{"icon":""}`)
	clearOutbox(t, db)

	n, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud"))
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v; want 2 marks", n, err)
	}
	wantMarks(t, db, "item:i1", "category:c1")
	if got := cachedSetting(t, db, modeKey); got != "cloud" {
		t.Fatalf("cached mode = %q", got)
	}
}

func TestCatalogModeEntry_UnsetToCloudAndTillToAdopting_6243(t *testing.T) {
	for _, tc := range []struct{ name, prev, next string }{
		{"unset to cloud", "", "cloud"},
		{"till to adopting", "till", "adopting"},
		{"unknown to cloud", "bogus", "cloud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openMigratedDB(t, "mode-entry-table.db")
			db := d.DB
			setCachedMode(t, db, tc.prev)
			linkItem(t, db, "i1", "x", `{"icon":""}`)
			clearOutbox(t, db)
			n, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV(tc.next))
			if err != nil || n != 1 {
				t.Fatalf("n = %d, err = %v; want 1", n, err)
			}
			wantMarks(t, db, "item:i1")
		})
	}
}

func TestCatalogModeEntry_NoReMarkWhenAlreadyLive_6243(t *testing.T) {
	for _, tc := range []struct{ prev, next string }{
		{"adopting", "cloud"}, {"cloud", "cloud"}, {"cloud", "adopting"},
		{"adopting", "adopting"}, {"cloud", "till"}, {"till", "till"},
	} {
		t.Run(tc.prev+"->"+tc.next, func(t *testing.T) {
			d := openMigratedDB(t, "mode-entry-live.db")
			db := d.DB
			setCachedMode(t, db, tc.prev)
			linkItem(t, db, "i1", "x", `{"icon":""}`)
			clearOutbox(t, db)
			n, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV(tc.next))
			if err != nil || n != 0 {
				t.Fatalf("n = %d, err = %v; want 0", n, err)
			}
			wantMarks(t, db)
		})
	}
}

func TestCatalogModeEntry_KeyAbsentFromKVIsNotEntering_6243(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-nokey.db")
	db := d.DB
	setCachedMode(t, db, "till")
	linkItem(t, db, "i1", "x", `{"icon":""}`)
	clearOutbox(t, db)
	n, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), map[string]string{"entitlement.plan": "p"})
	if err != nil || n != 0 {
		t.Fatalf("n = %d, err = %v; want 0", n, err)
	}
	wantMarks(t, db)
}

func TestCatalogModeEntry_ReplicaNoMarks_6243(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-replica.db")
	db := d.DB
	seedPromoteReplica(t, db, "till")
	linkItem(t, db, "i1", "x", `{"icon":""}`)
	clearOutbox(t, db)
	n, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud"))
	if err != nil || n != 0 {
		t.Fatalf("n = %d, err = %v; want 0", n, err)
	}
	wantMarks(t, db)
	if got := cachedSetting(t, db, modeKey); got != "cloud" {
		t.Fatalf("cached mode = %q, want cloud", got)
	}
}

func TestCatalogModeEntry_RepeatIsNoop_6243(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-repeat.db")
	db := d.DB
	setCachedMode(t, db, "till")
	linkItem(t, db, "i1", "x", `{"icon":""}`)
	clearOutbox(t, db)
	repo := NewSettingsRepo(db)
	var total int64
	for i := 0; i < 2; i++ {
		n, err := repo.SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud"))
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 1 {
		t.Fatalf("total marks = %d, want 1", total)
	}
	wantMarks(t, db, "item:i1")
}

func TestCatalogModeEntry_Skips_6243(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
		want  []string
	}{
		{"open mark exists", func(t *testing.T, db *sql.DB) {
			linkItem(t, db, "i1", "x", `{"icon":""}`)
			clearOutbox(t, db)
			obExec(t, db, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES ('item', 'i1', 'x')`)
		}, []string{"item:i1"}},
		{"local equals base", func(t *testing.T, db *sql.DB) { linkItem(t, db, "i1", "", `{"icon":""}`) }, nil},
		{"local empty, base real", func(t *testing.T, db *sql.DB) { linkItem(t, db, "i1", "", `{"icon":"y"}`) }, nil},
		{"suppressed", func(t *testing.T, db *sql.DB) {
			linkItem(t, db, "i1", "x", `{"icon":""}`)
			obExec(t, db, `INSERT INTO cloud_outbox_suppress DEFAULT VALUES`)
		}, nil},
		{"base holds real cloud value (re-upgrade, cloud wins)", func(t *testing.T, db *sql.DB) {
			linkItem(t, db, "i1", "x", `{"icon":"y"}`)
		}, nil},
		{"base key missing is marked", func(t *testing.T, db *sql.DB) {
			linkItem(t, db, "i1", "x", `{"name":"N"}`)
		}, []string{"item:i1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openMigratedDB(t, "mode-entry-skips.db")
			db := d.DB
			setCachedMode(t, db, "till")
			tc.setup(t, db)
			if tc.name != "open mark exists" {
				clearOutbox(t, db)
			}
			if _, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud")); err != nil {
				t.Fatal(err)
			}
			wantMarks(t, db, tc.want...)
			if tc.name == "base key missing is marked" {
				var v string
				if err := db.QueryRow(`SELECT json_extract(base, '$.icon') FROM cloud_links WHERE local_id = 'i1'`).Scan(&v); err != nil || v != "" {
					t.Fatalf("seeded base icon = %q, %v; want empty string", v, err)
				}
				if got := baseIconType(t, db, "item", "i1"); got != "text" {
					t.Fatalf("base icon json_type = %q", got)
				}
			}
		})
	}
}

func TestCatalogModeEntry_MarkFailureRollsBack_6243(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-fail.db")
	db := d.DB
	setCachedMode(t, db, "till")
	obExec(t, db, `INSERT INTO settings (key, value) VALUES ('entitlement.plan', 'p-old')`)
	linkItem(t, db, "i1", "x", `{"icon":""}`)
	clearOutbox(t, db)
	obExec(t, db, `CREATE TRIGGER t6243 BEFORE INSERT ON cloud_outbox BEGIN SELECT RAISE(ABORT, 'boom'); END`)

	n, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("n = %d, err = %v; want the marking failure", n, err)
	}
	if got := cachedSetting(t, db, modeKey); got != "till" {
		t.Fatalf("cached mode = %q, want till (rolled back)", got)
	}
	if got := cachedSetting(t, db, "entitlement.plan"); got != "p-old" {
		t.Fatalf("plan = %q, want p-old", got)
	}
	wantMarks(t, db)
}

func TestCatalogModeEntry_PromotedReplicaThenCheckin_6243(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-promoted.db")
	db := d.DB
	seedPromoteReplica(t, db, "till")
	linkItem(t, db, "i1", "x", `{"icon":""}`)
	clearOutbox(t, db)
	repo := NewSettingsRepo(db)
	if err := repo.PromoteReplica(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, db) // till mode: promotion marks nothing
	n, err := repo.SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud"))
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v; want 1", n, err)
	}
	wantMarks(t, db, "item:i1")
}

// fullItemBase is a link base as a real pull leaves it: every upload field,
// matching the row linkItem inserts (price 100, no category/tax/barcodes).
func fullItemBase(price int) string {
	return fmt.Sprintf(`{"name":"N","description":"","sku":"S-i1","price_minor":%d,"category_cloud_id":null,"tax_code_cloud_id":null,`+
		`"active":true,"barcodes":[],"is_weighed":false,"age_restricted":false,"net_quantity_value":null,"net_quantity_unit":null,"icon":""}`, price)
}

// A mark uploads every field the row differs on, so a row whose till-mode
// period also edited another upload field is not marked: ADR-0162 §6 cloud
// wins there and the pull resets it (ut-docs#6243 fix round).
func TestCatalogModeEntry_OtherFieldEditedNotMarked_6243(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price int
		want  []string
	}{
		{"icon only (full base) is marked", 100, []string{"item:i1"}},
		{"icon and price diverge is not marked", 250, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openMigratedDB(t, "mode-entry-other.db")
			db := d.DB
			setCachedMode(t, db, "till")
			linkItem(t, db, "i1", "x", fullItemBase(tc.price))
			clearOutbox(t, db)
			if _, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud")); err != nil {
				t.Fatal(err)
			}
			wantMarks(t, db, tc.want...)
		})
	}
}

func TestCatalogModeEntry_OtherCategoryFieldEditedNotMarked_6243(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
		want []string
	}{
		{"icon only", `{"name":"C","parent_cloud_id":null,"sort_order":0,"active":true,"icon":""}`, []string{"category:c1"}},
		{"name differs", `{"name":"Old","parent_cloud_id":null,"sort_order":0,"active":true,"icon":""}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openMigratedDB(t, "mode-entry-other-cat.db")
			db := d.DB
			setCachedMode(t, db, "till")
			linkCategory(t, db, "c1", "x", tc.base)
			clearOutbox(t, db)
			if _, err := NewSettingsRepo(db).SetManyMarkingCatalogModeEntry(context.Background(), modeKV("cloud")); err != nil {
				t.Fatal(err)
			}
			wantMarks(t, db, tc.want...)
		})
	}
}
