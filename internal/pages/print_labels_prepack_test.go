package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3391: Price Marking Order 2004 — a pre-packed item's shelf label
// shows its unit price (per kg / per litre / per item) computed from its
// net quantity, only when the shop has opted in. The arithmetic is
// money.MulDiv (half-up), pinned here with worked examples that do NOT
// divide evenly, so a truncating implementation fails:
//
//	£2.00 for 300 g  → 200 × 1000 / 300 = 666.67p → £6.67 per kg   (trunc: £6.66)
//	£1.25 for 330 ml → 125 × 1000 / 330 = 378.79p → £3.79 per litre (trunc: £3.78)
//	£1.00 for 3 ea   → 100 / 3         = 33.33p  → £0.33 per item
//	£2.50 for 6 ea   → 250 / 6         = 41.67p  → £0.42 per item  (trunc: £0.41)
func TestPostPrintLabels_PrePackUnitPrice(t *testing.T) {
	initLabelsNoticeI18n(t)
	mux, dp := newPrintAPITestDeps(t)

	seed := []string{
		`INSERT INTO items (id, sku, name, base_price, net_quantity_value, net_quantity_unit) VALUES ('rice', 'SKU-RICE', 'Rice 300g', 200, 300, 'g')`,
		`INSERT INTO items (id, sku, name, base_price, net_quantity_value, net_quantity_unit) VALUES ('cola', 'SKU-COLA', 'Cola 330ml', 125, 330, 'ml')`,
		`INSERT INTO items (id, sku, name, base_price, net_quantity_value, net_quantity_unit) VALUES ('pens', 'SKU-PENS', 'Pens x3', 100, 3, 'ea')`,
		`INSERT INTO items (id, sku, name, base_price, net_quantity_value, net_quantity_unit) VALUES ('rolls', 'SKU-ROLLS', 'Rolls x6', 250, 6, 'ea')`,
		`INSERT INTO items (id, sku, name, base_price) VALUES ('mug', 'SKU-MUG', 'Mug', 500)`,
		// A weighed item that also (mistakenly) carries a net quantity: the
		// pre-pack setting must never touch its label.
		`INSERT INTO items (id, sku, name, base_price, is_weighed, net_quantity_value, net_quantity_unit) VALUES ('bananas', 'SKU-BAN', 'Bananas', 150, 1, 1000, 'g')`,
	}
	for _, q := range seed {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	devicePath := filepath.Join(t.TempDir(), "fake-printer")
	if err := os.WriteFile(devicePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterMode, "device"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), keyPrinterDevice, devicePath); err != nil {
		t.Fatal(err)
	}
	setPrePack := func(v string) {
		t.Helper()
		if err := dp.Settings.Set(t.Context(), data.CatalogPrePackUnitPriceEnabledKey, v); err != nil {
			t.Fatal(err)
		}
	}
	printItem := func(id string) string {
		t.Helper()
		// The device transport writes from offset 0 without truncating;
		// empty the file so each read is exactly one job.
		if err := os.WriteFile(devicePath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id="+id))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("print %s = %d: %s", id, rec.Code, rec.Body.String())
		}
		out, err := os.ReadFile(devicePath)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	// (b) Off by default (no row): no PRE-PACK unit price on any item.
	// "bananas" is weighed (ut-docs#3343, a separate, always-on mechanism
	// that merged into main after this branch started) and legitimately
	// prints its own " per kg" regardless of this setting — captured here
	// for assertion (d) below, but checked separately, not lumped into the
	// " per "-free assertion the true pre-pack items get.
	offJobs := map[string]string{}
	for _, id := range []string{"rice", "cola", "pens", "rolls", "mug"} {
		offJobs[id] = printItem(id)
		if strings.Contains(offJobs[id], " per ") {
			t.Fatalf("setting absent: %s label must carry no unit price, got %q", id, offJobs[id])
		}
	}
	offJobs["bananas"] = printItem("bananas")
	if !strings.Contains(offJobs["bananas"], "£1.50 per kg\n") {
		t.Fatalf("weighed item must still show its own per-kg line regardless of the pre-pack setting, got %q", offJobs["bananas"])
	}
	if strings.Count(offJobs["bananas"], " per ") != 1 {
		t.Fatalf("weighed item must carry exactly one unit-price line (its own, not also a pre-pack one), got %q", offJobs["bananas"])
	}

	// (a) On: the pack price line is unchanged and the unit price follows
	// on its own line.
	setPrePack("1")
	for id, want := range map[string]string{
		"rice":  "£6.67 per kg",
		"cola":  "£3.79 per litre",
		"pens":  "£0.33 per item",
		"rolls": "£0.42 per item",
	} {
		job := printItem(id)
		if !strings.Contains(job, want+"\n") {
			t.Errorf("setting on: %s label must contain %q, got %q", id, want, job)
		}
	}
	if job := printItem("rice"); !strings.Contains(job, "£2.00\n") {
		t.Errorf("the pack price line must stay the bare price, got %q", job)
	}

	// (c) No net quantity configured: never a unit price, setting or not.
	if job := printItem("mug"); job != offJobs["mug"] || strings.Contains(job, " per ") {
		t.Errorf("item without net quantity changed with the setting on: %q vs %q", job, offJobs["mug"])
	}

	// (d) A weighed item's label is byte-identical with the setting on.
	if job := printItem("bananas"); job != offJobs["bananas"] {
		t.Errorf("weighed item's label changed with the pre-pack setting on:\n on=%q\noff=%q", job, offJobs["bananas"])
	}

	// Explicit "0" behaves exactly like absent.
	setPrePack("0")
	if job := printItem("rice"); job != offJobs["rice"] {
		t.Errorf("setting 0: rice label = %q, want the setting-absent label %q", job, offJobs["rice"])
	}
}
