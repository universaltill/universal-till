package pages

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// newOpenOrdersResumeModeTestDeps is newHoldTestDeps' schema (held_sales,
// tables, table_claims, tills, a stub-resolver Engine) plus a real
// `settings` table (data.SettingsRepo's own schema — key/value/updated_at)
// so display.mode can actually be set. Neither existing fixture alone
// covers this: newHoldTestDeps has no settings table at all (its Deps
// doesn't even set the Settings field), and newOpenOrdersFullFixtureMux
// (open_orders_backtosale_redirect_test.go) has settings but no Engine, so
// resumeHeldSale would nil-panic there. ut-docs#2347: the resume redirect
// itself is mode-aware now, so testing it needs both.
func newOpenOrdersResumeModeTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE held_sales (id TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', total_minor INTEGER NOT NULL DEFAULT 0, line_count INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL, table_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT '', primary_synced INTEGER NOT NULL DEFAULT 0);`); err != nil {
		t.Fatalf("create held_sales: %v", err)
	}
	// ADR-0093 Amendment B (ut-docs#2712): resume claims the row through
	// HeldSalesRepo.ClaimAndTombstone, which writes this table (migration
	// 044) -- column-identical to the migration.
	if _, err := db.Exec(`CREATE TABLE held_sales_tombstones (id TEXT PRIMARY KEY, deleted_at TEXT NOT NULL, till TEXT NOT NULL DEFAULT '');`); err != nil {
		t.Fatalf("create held_sales_tombstones: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE tables (id TEXT PRIMARY KEY, label TEXT NOT NULL, area_zone TEXT NOT NULL DEFAULT '', seat_count INTEGER NOT NULL DEFAULT 0, shape TEXT NOT NULL DEFAULT 'rect', pos_x INTEGER NOT NULL DEFAULT 0, pos_y INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE table_claims (table_id TEXT PRIMARY KEY REFERENCES tables(id), claimed_at TEXT NOT NULL, till_id TEXT NOT NULL DEFAULT '');`); err != nil {
		t.Fatalf("create table_claims: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE tills (id TEXT PRIMARY KEY, name TEXT NOT NULL, bearer_hash TEXT UNIQUE, enrolled_at TEXT NOT NULL DEFAULT (datetime('now')), last_seen_at TEXT);`); err != nil {
		t.Fatalf("create tills: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT '');`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	dp := &common.Deps{
		Db:       db,
		Engine:   engine,
		State:    common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerOpenOrders(mux, dp)
	return mux, dp
}

// TestOpenOrdersResume_PlainSuccess_ModeAwareRedirect and
// TestOpenOrdersResume_ParkedPrior_ModeAwareRedirect (ut-docs#2347): both
// resume-success outcomes must land on saleScreenReturnURL(mode), not
// always bare "/" — a backoffice/self_order-mode till previously bounced
// straight past the resumed basket to /backoffice or /self-order.
func TestOpenOrdersResume_PlainSuccess_ModeAwareRedirect(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"", "/"},
		{"backoffice", "/?stay=1"},
		{"self_order", "/self-order"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			mux, d := newOpenOrdersResumeModeTestDeps(t)
			if c.mode != "" {
				if err := d.Settings.Set(t.Context(), "display.mode", c.mode); err != nil {
					t.Fatalf("set display.mode: %v", err)
				}
			}
			if _, err := d.Engine.Scan("ABC"); err != nil {
				t.Fatalf("seed scan: %v", err)
			}
			holdMux := http.NewServeMux()
			registerHoldAPI(holdMux, d)
			holdTestPost(holdMux, "/api/pos/hold", "label=Table+4")
			row := holdTestOnlyRow(t, d)

			rec := holdTestPost(mux, "/open-orders/resume", "id="+row.ID)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("POST /open-orders/resume = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != c.want {
				t.Fatalf("mode %q: Location = %q, want %q", c.mode, got, c.want)
			}
		})
	}
}

func TestOpenOrdersResume_ParkedPrior_ModeAwareRedirect(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"", "/?msg=hold.toast.parked_and_resumed"},
		{"backoffice", "/?stay=1&msg=hold.toast.parked_and_resumed"},
		{"self_order", "/self-order?msg=hold.toast.parked_and_resumed"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			mux, d := newOpenOrdersResumeModeTestDeps(t)
			if c.mode != "" {
				if err := d.Settings.Set(t.Context(), "display.mode", c.mode); err != nil {
					t.Fatalf("set display.mode: %v", err)
				}
			}
			if _, err := d.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','Table 4',1250,3,'{}',NULL,datetime('now'))`); err != nil {
				t.Fatalf("seed held sale: %v", err)
			}
			if _, err := d.Engine.Scan("ABC"); err != nil {
				t.Fatalf("seed a live basket: %v", err)
			}

			rec := holdTestPost(mux, "/open-orders/resume", "id=h1")
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("POST /open-orders/resume = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != c.want {
				t.Fatalf("mode %q: Location = %q, want %q", c.mode, got, c.want)
			}
		})
	}
}
