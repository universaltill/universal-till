package catalogpull

import (
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#5098: the feed serves each row at its LATEST version, so a
// barcode last written at version 1 can name an item last edited at
// version 2. The barcode comes first and used to be refused for good
// ("has not reached this till"): the cursor moved past it and nothing
// re-sent it. A reference that resolves later in the same run must apply,
// leave no refusal and put nothing in the problems ring.
func TestPullReferenceArrivingInALaterVersionApplies_5098(t *testing.T) {
	logging.ResetRecent()
	h := newHarness(t)
	h.feed.add(barcodeRow(barcodeID(1), 1, eanA, itemID(1), nil, true))
	h.feed.add(itemFeedRow(1, 2, "Mug", 800))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 2 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if got, want := h.barcodeOwner(eanA), "item:"+itemID(1)+":EAN13:1"; got != want {
		t.Fatalf("barcode = %q, want %q", got, want)
	}
	if fails, _ := applyFailures(h.db); len(fails) != 0 {
		t.Fatalf("failures = %+v, want none", fails)
	}
	if open := logging.OpenProblems(time.Now(), 0); len(open) != 0 {
		t.Fatalf("problems = %+v, want none", open)
	}
}

// A reference that never arrives is still a recorded, logged refusal.
func TestPullReferenceNeverArrivingIsStillRefused_5098(t *testing.T) {
	h := newHarness(t)
	h.feed.add(barcodeRow(barcodeID(1), 1, eanA, itemID(1), nil, true))
	res := h.mustRun()
	if res.Refused != 1 || res.Applied != 0 {
		t.Fatalf("result = %+v", res)
	}
	if fails, _ := applyFailures(h.db); len(fails) != 1 {
		t.Fatalf("failures = %+v, want 1", fails)
	}
}

// A chain across versions: barcode (v1) -> item (v2) -> category (v3). The
// last pass resolves it from the bottom up.
func TestPullReferenceChainAcrossVersionsApplies_5098(t *testing.T) {
	h := newHarness(t)
	h.feed.add(barcodeRow(barcodeID(1), 1, eanA, itemID(1), nil, true))
	it := itemFeedRow(1, 2, "Mug", 800)
	it.Data["category_cloud_id"] = cCat
	h.feed.add(it)
	h.feed.add(catRow(cCat, 3, "Drinks", nil))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 3 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if fails, _ := applyFailures(h.db); len(fails) != 0 {
		t.Fatalf("failures = %+v, want none", fails)
	}
	if got, want := h.barcodeOwner(eanA), "item:"+itemID(1)+":EAN13:1"; got != want {
		t.Fatalf("barcode = %q, want %q", got, want)
	}
}
