package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// BenchmarkScanItemOnly (ut-docs#4344) is the latency guard for the scan
// handler's commonest case, a code that is only an item: looking it up as
// every kind (voucher, promo, receipt, customer) must not make it
// measurably slower. "first" scans a code not yet in the basket; "repeat"
// scans one already there, the path that used to skip every DB read;
// "typed-first" is the same code keyed in by hand.
func BenchmarkScanItemOnly(b *testing.B) {
	_, file, _, _ := runtime.Caller(0)
	if err := os.Chdir(filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))); err != nil {
		b.Fatal(err)
	}
	migrated, err := db.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = migrated.Close() })
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		b.Fatal(err)
	}
	httpx.InitI18n(i18n, "en")
	if _, err := migrated.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-b','SKU-B','Bench item',250,1)`); err != nil {
		b.Fatal(err)
	}
	if _, err := migrated.Exec(`INSERT INTO item_barcodes (item_id, barcode, is_primary) VALUES ('itm-b','4000000000006',1)`); err != nil {
		b.Fatal(err)
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, ui.PriceResolverAdapter{Store: ui.NewButtonStore(migrated.DB)})
	dp := &common.Deps{
		Cfg:      &config.Config{Theme: "default", StoreName: "Bench Shop"},
		Db:       migrated.DB,
		State:    common.RuntimeState{Currency: "EUR", TaxRateBP: 2000},
		Settings: settings.NewStore(migrated.DB),
		Engine:   engine,
	}
	b.Cleanup(dp.WaitForAsyncWork)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	src := "scanner"
	scan := func() {
		req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=4000000000006&src="+src))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("scan: %d", rec.Code)
		}
	}
	b.Run("first", func(b *testing.B) {
		for b.Loop() {
			engine.Reset()
			scan()
		}
	})
	b.Run("repeat", func(b *testing.B) {
		engine.Reset()
		scan()
		for b.Loop() {
			scan()
		}
	})
	// Typed entry (no src) resolved the item once before #4344 too.
	src = ""
	b.Run("typed-first", func(b *testing.B) {
		for b.Loop() {
			engine.Reset()
			scan()
		}
	})
}
