package catalogpull

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4521: the pull applies item_variant rows (with their option
// values) and item_barcode rows, one row per barcode.

const (
	// Sort before every item, set and variant id: the server's
	// kind/cloud_id order alone would apply a barcode or variant before
	// its owner.
	cEarlyBarcode = "00000000-aaaa-4aaa-8aaa-111111111111"
	cEarlyVariant = "00000000-8888-4888-8888-111111111111"

	eanA = "4006381333931" // valid EAN-13
	eanB = "5901234123457" // valid EAN-13
)

func variantID(n int) string { return "88888888-8888-4888-8888-" + strconv.Itoa(100000000000+n) }
func barcodeID(n int) string { return "aaaaaaaa-aaaa-4aaa-8aaa-" + strconv.Itoa(100000000000+n) }

func variantData(item, name, sku string, price int64, values ...string) map[string]any {
	if values == nil {
		values = []string{}
	}
	return map[string]any{
		"item_cloud_id": item, "name": name, "sku": sku, "price_minor": price,
		"active": true, "retired_with_item": false, "option_value_cloud_ids": values,
	}
}

func variantRow(id string, v int64, d map[string]any) feedRow {
	return feedRow{Kind: "item_variant", CloudID: id, Version: v, Data: d}
}

func barcodeRow(id string, v int64, code, item string, variant any, primary bool) feedRow {
	return feedRow{Kind: "item_barcode", CloudID: id, Version: v, Data: map[string]any{
		"barcode": code, "item_cloud_id": item, "variant_cloud_id": variant, "barcode_type": "EAN13", "is_primary": primary,
	}}
}

type variantState struct {
	Item, Name, SKU string
	Price           int64
	Active, Retired bool
	Values          string // option_set_value ids, sorted
}

func (h *harness) variant(id string) (variantState, bool) {
	h.t.Helper()
	var v variantState
	if h.count(`SELECT COUNT(*) FROM item_variants WHERE id = ?`, id) == 0 {
		return v, false
	}
	if err := h.db.QueryRow(`SELECT item_id, name, COALESCE(sku, ''), price, is_active, retired_with_item FROM item_variants WHERE id = ?`, id).
		Scan(&v.Item, &v.Name, &v.SKU, &v.Price, &v.Active, &v.Retired); err != nil {
		h.t.Fatal(err)
	}
	v.Values = h.list(`SELECT option_set_value_id FROM item_variant_options WHERE variant_id = ? ORDER BY option_set_value_id`, id)
	return v, true
}

// barcodeOwner is "item:<id>:<type>:<primary>" or "variant:<id>:…", "" when
// the barcode is on no row.
func (h *harness) barcodeOwner(code string) string {
	h.t.Helper()
	return h.list(`SELECT 'item:' || item_id || ':' || barcode_type || ':' || is_primary FROM item_barcodes WHERE barcode = ?
		UNION ALL SELECT 'variant:' || variant_id || ':' || barcode_type || ':' || is_primary FROM variant_barcodes WHERE barcode = ?`, code, code)
}

// shirt seeds an option set with two values and an item using it.
func (h *harness) shirt(v int64) {
	h.t.Helper()
	h.feed.add(setRow(cSet, v, "Size", val(1, "S"), val(2, "M")))
	it := itemRowData("Shirt", 1500)
	it["option_set_cloud_ids"] = []string{cSet}
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: v + 1, Data: it})
}

func TestPullVariantCreateUpdateKeepAndTombstone(t *testing.T) {
	h := newHarness(t)
	h.shirt(1)
	h.feed.add(variantRow(variantID(1), 3, variantData(itemID(1), "S", "SHIRT-S", 1500, valID(1))))
	h.feed.add(variantRow(variantID(2), 4, variantData(itemID(1), "M", "", 1600, valID(2))))
	res := h.mustRun()
	if res.Applied != 4 || res.Refused != 0 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	v, ok := h.variant(variantID(1))
	if !ok {
		t.Fatal("variant not created under its cloud id")
	}
	if want := (variantState{Item: itemID(1), Name: "S", SKU: "SHIRT-S", Price: 1500, Active: true, Values: valID(1)}); v != want {
		t.Fatalf("variant = %+v, want %+v", v, want)
	}
	if v2, _ := h.variant(variantID(2)); !strings.HasPrefix(v2.SKU, "VAR-") || v2.Values != valID(2) {
		t.Fatalf("blank sku on create not generated / values not written: %+v", v2)
	}
	if local, ok, _ := data.NewCloudLinkRepo().LocalID(context.Background(), h.db, "item_variant", variantID(1)); !ok || local != variantID(1) {
		t.Fatalf("link = %q %v", local, ok)
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_catalog_item_variant_saved' AND entity_id = ?`, variantID(1)); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}

	// Update: rename, price, blank sku keeps, retired with its item,
	// other option value.
	upd := variantData(itemID(1), "Small", "", 1550, valID(2))
	upd["active"], upd["retired_with_item"] = false, true
	h.feed.add(variantRow(variantID(1), 5, upd))
	h.mustRun()
	v, _ = h.variant(variantID(1))
	if want := (variantState{Item: itemID(1), Name: "Small", SKU: "SHIRT-S", Price: 1550, Retired: true, Values: valID(2)}); v != want {
		t.Fatalf("updated variant = %+v, want %+v", v, want)
	}

	// Field-wise: absent fields keep; switching it on without the flag is
	// an explicit switch, so retired_with_item clears.
	h.feed.add(variantRow(variantID(1), 6, map[string]any{"item_cloud_id": itemID(1), "active": true}))
	h.mustRun()
	v, _ = h.variant(variantID(1))
	if want := (variantState{Item: itemID(1), Name: "Small", SKU: "SHIRT-S", Price: 1550, Active: true, Values: valID(2)}); v != want {
		t.Fatalf("keep update = %+v, want %+v", v, want)
	}
	// retired_with_item never holds on an active variant.
	on := variantData(itemID(1), "Small", "SHIRT-S", 1550, valID(2))
	on["retired_with_item"] = true
	h.feed.add(variantRow(variantID(1), 7, on))
	h.mustRun()
	if v, _ = h.variant(variantID(1)); v.Retired || !v.Active {
		t.Fatalf("active variant stored retired_with_item: %+v", v)
	}

	// Tombstone: deactivated, never deleted.
	h.feed.add(feedRow{Kind: "item_variant", CloudID: variantID(1), Version: 8, Deleted: true})
	res = h.mustRun()
	if res.Applied != 1 || res.Refused != 0 {
		t.Fatalf("tombstone = %+v", res)
	}
	v, ok = h.variant(variantID(1))
	if !ok || v.Active || v.Name != "Small" {
		t.Fatalf("tombstoned variant = %+v (exists %v), want kept and inactive", v, ok)
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_catalog_item_variant_deactivated' AND entity_id = ?`, variantID(1)); n != 1 {
		t.Fatalf("deactivated audit rows = %d", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_links WHERE entity = 'item_variant' AND cloud_id = ?`, variantID(1)); n != 0 {
		t.Fatal("variant link kept after its tombstone")
	}
}

func TestPullVariantRefusals(t *testing.T) {
	h := newHarness(t)
	h.shirt(1)
	other := itemRowData("Mug", 800)
	other["sku"] = "MUG-1"
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(2), Version: 3, Data: other})
	h.feed.add(variantRow(variantID(1), 4, variantData(itemID(1), "S", "SHIRT-S", 1500, valID(1))))
	h.mustRun()

	unknownItem := variantData(cUnknown, "X", "", 100)
	unknownValue := variantData(itemID(1), "L", "", 100, cUnknown)
	noPrice := variantData(itemID(1), "XL", "", 0)
	delete(noPrice, "price_minor")
	nullItem := variantData(itemID(1), "XS", "", 100)
	nullItem["item_cloud_id"] = nil
	moved := variantData(itemID(2), "S", "SHIRT-S", 1500)
	negative := variantData(itemID(1), "S", "SHIRT-S", -1)
	skuTaken := variantData(itemID(1), "S", "MUG-1", 1500)
	nullValues := variantData(itemID(1), "S", "SHIRT-S", 1500)
	nullValues["option_value_cloud_ids"] = nil
	for i, d := range []map[string]any{unknownItem, unknownValue, noPrice, nullItem} {
		h.feed.add(variantRow(variantID(10+i), int64(5+i), d))
	}
	for i, d := range []map[string]any{moved, negative, skuTaken, nullValues} {
		h.feed.add(variantRow(variantID(1), int64(9+i), d))
	}
	res := h.mustRun()
	if res.Refused != 8 || res.Applied != 0 || h.cursor() != 12 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v cursor %d, failures %+v", res, h.cursor(), fails)
	}
	if n := h.count(`SELECT COUNT(*) FROM item_variants`); n != 1 {
		t.Fatalf("variants = %d, want only the first", n)
	}
	v, _ := h.variant(variantID(1))
	if want := (variantState{Item: itemID(1), Name: "S", SKU: "SHIRT-S", Price: 1500, Active: true, Values: valID(1)}); v != want {
		t.Fatalf("refused rows changed the variant: %+v", v)
	}
	if fails, _ := applyFailures(h.db); len(fails) != 5 {
		t.Fatalf("failures = %+v, want 5 cloud rows", fails)
	}
}

func TestPullBarcodesPutMoveAndTombstone(t *testing.T) {
	h := newHarness(t)
	h.shirt(1)
	h.feed.add(itemFeedRow(2, 3, "Mug", 800))
	h.feed.add(variantRow(variantID(1), 4, variantData(itemID(1), "S", "SHIRT-S", 1500, valID(1))))
	h.feed.add(barcodeRow(barcodeID(1), 5, eanA, itemID(1), nil, true))
	h.feed.add(barcodeRow(barcodeID(2), 6, "SHIRT-S-01", itemID(1), variantID(1), false))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 6 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if got, want := h.barcodeOwner(eanA), "item:"+itemID(1)+":EAN13:1"; got != want {
		t.Fatalf("item barcode = %q, want %q", got, want)
	}
	if got := h.barcodeOwner("SHIRT-S-01"); !strings.HasPrefix(got, "variant:"+variantID(1)+":") || !strings.HasSuffix(got, ":0") {
		t.Fatalf("variant barcode = %q", got)
	}
	if local, ok, _ := data.NewCloudLinkRepo().LocalID(context.Background(), h.db, "item_barcode", barcodeID(1)); !ok || local != "item:"+itemID(1)+":"+eanA {
		t.Fatalf("link = %q %v, want the owner and stored key", local, ok)
	}

	// A changed code replaces the old one; a move from the variant to the
	// item leaves nothing on the variant.
	h.feed.add(barcodeRow(barcodeID(1), 7, eanB, itemID(1), nil, true))
	h.feed.add(barcodeRow(barcodeID(2), 8, "SHIRT-S-01", itemID(1), nil, false))
	res = h.mustRun()
	if res.Refused != 0 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if h.barcodeOwner(eanA) != "" || h.barcodeOwner(eanB) != "item:"+itemID(1)+":EAN13:1" {
		t.Fatalf("changed code: old %q new %q", h.barcodeOwner(eanA), h.barcodeOwner(eanB))
	}
	if got := h.barcodeOwner("SHIRT-S-01"); !strings.HasPrefix(got, "item:"+itemID(1)+":") {
		t.Fatalf("moved barcode = %q", got)
	}

	// Refusals keep the till's state: a code another item holds, a
	// variant of another item, an unknown variant, unprintable text.
	h.feed.add(barcodeRow(barcodeID(3), 9, eanB, itemID(2), nil, true))
	h.feed.add(barcodeRow(barcodeID(4), 10, "MUG-01", itemID(2), variantID(1), false))
	h.feed.add(barcodeRow(barcodeID(5), 11, "MUG-02", itemID(2), cUnknown, false))
	h.feed.add(barcodeRow(barcodeID(2), 12, "has space", itemID(1), nil, false))
	res = h.mustRun()
	if res.Refused != 4 || res.Applied != 0 || h.cursor() != 12 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v cursor %d, failures %+v", res, h.cursor(), fails)
	}
	if h.barcodeOwner(eanB) != "item:"+itemID(1)+":EAN13:1" || !strings.HasPrefix(h.barcodeOwner("SHIRT-S-01"), "item:"+itemID(1)) {
		t.Fatal("refused rows changed a barcode")
	}
	if n := h.count(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = ?`, itemID(2)) + h.count(`SELECT COUNT(*) FROM cloud_links WHERE entity = 'item_barcode' AND cloud_id IN (?, ?, ?)`, barcodeID(3), barcodeID(4), barcodeID(5)); n != 0 {
		t.Fatalf("refused barcodes left %d rows", n)
	}

	// Tombstone: the barcode goes, the item stays.
	h.feed.add(feedRow{Kind: "item_barcode", CloudID: barcodeID(1), Version: 13, Deleted: true})
	h.feed.add(feedRow{Kind: "item_barcode", CloudID: barcodeID(9), Version: 14, Deleted: true}) // never pulled
	res = h.mustRun()
	if res.Refused != 0 || res.Applied != 2 {
		t.Fatalf("tombstones = %+v", res)
	}
	if h.barcodeOwner(eanB) != "" {
		t.Fatal("tombstoned barcode kept")
	}
	if _, ok := h.item(itemID(1)); !ok {
		t.Fatal("barcode tombstone removed its item")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'cloud_catalog_item_barcode_deleted'`); n != 1 {
		t.Fatalf("deleted audit rows = %d, want 1 (the never-pulled one is a no-op)", n)
	}
}

func TestPullAppliesVariantsAndBarcodesAfterTheirOwnersInOneVersion(t *testing.T) {
	h := newHarness(t)
	h.feed.add(barcodeRow(cEarlyBarcode, 1, eanA, itemID(1), cEarlyVariant, true))
	h.feed.add(variantRow(cEarlyVariant, 1, variantData(itemID(1), "Paper", "", 300, valID(1))))
	it := itemRowData("Latte", 300)
	it["option_set_cloud_ids"] = []string{cLateSet}
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 1, Data: it})
	h.feed.add(setRow(cLateSet, 1, "Cup", val(1, "Paper")))
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 4 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if got := h.barcodeOwner(eanA); !strings.HasPrefix(got, "variant:"+cEarlyVariant+":") {
		t.Fatalf("barcode = %q", got)
	}
}

func TestOrderGroupPutsVariantsAfterItemsAndBarcodesAfterVariants(t *testing.T) {
	group := []Row{
		{Kind: "item_barcode", CloudID: cEarlyBarcode, Version: 1},
		{Kind: "layout", CloudID: itemID(2), Version: 1},
		{Kind: "item_variant", CloudID: cEarlyVariant, Version: 1},
		{Kind: "item", CloudID: itemID(1), Version: 1},
		{Kind: "option_set", CloudID: cSet, Version: 1},
	}
	var got []string
	for _, r := range orderGroup(group) {
		got = append(got, r.Kind)
	}
	if want := "option_set,item,item_variant,item_barcode,layout"; strings.Join(got, ",") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestPullResyncPrunesVariantsAndBarcodes(t *testing.T) {
	h := newHarness(t)
	h.shirt(1)
	h.feed.add(variantRow(variantID(1), 3, variantData(itemID(1), "S", "", 1500, valID(1))))
	h.feed.add(barcodeRow(barcodeID(1), 4, eanA, itemID(1), nil, true))
	h.mustRun()
	h.feed.drop(variantID(1))
	h.feed.drop(barcodeID(1))
	h.feed.goneOnce = true
	h.feed.add(itemFeedRow(2, 5, "New", 100))
	res := h.mustRun()
	if !res.Done || res.Pruned != 2 {
		t.Fatalf("result = %+v, want 2 pruned", res)
	}
	if v, ok := h.variant(variantID(1)); !ok || v.Active {
		t.Fatalf("pruned variant = %+v (exists %v), want kept and inactive", v, ok)
	}
	if h.barcodeOwner(eanA) != "" {
		t.Fatal("unreturned barcode not pruned")
	}
}

// An applier bump re-pulls everything from 0: variants and barcodes
// re-apply onto the same rows, nothing duplicated or refused.
func TestPullRepullReappliesVariantsAndBarcodesIdempotently(t *testing.T) {
	h := newHarness(t)
	h.shirt(1)
	h.feed.add(variantRow(variantID(1), 3, variantData(itemID(1), "S", "SHIRT-S", 1500, valID(1))))
	h.feed.add(barcodeRow(barcodeID(1), 4, eanA, itemID(1), variantID(1), true))
	h.mustRun()
	if err := data.NewSettingsRepo(h.db).Set(context.Background(), KeyApplierVersion, "2"); err != nil {
		t.Fatal(err)
	}
	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 4 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("re-pull = %+v, failures %+v", res, fails)
	}
	if got := h.feed.requests(); got[len(got)-1] != 0 {
		t.Fatalf("re-pull asked since=%d, want 0", got[len(got)-1])
	}
	if n := h.count(`SELECT COUNT(*) FROM item_variants`) + h.count(`SELECT COUNT(*) FROM variant_barcodes`); n != 2 {
		t.Fatalf("re-pull left %d variant+barcode rows, want 2", n)
	}
	if got := h.barcodeOwner(eanA); !strings.HasPrefix(got, "variant:"+variantID(1)+":EAN13:1") {
		t.Fatalf("barcode after re-pull = %q", got)
	}
}

// Review findings (ut-docs#4521): a barcode row only ever removes its own
// owner's barcode, keeps one primary per owner, and two cloud rows never
// share one till barcode.
func TestPullBarcodeOwnershipPrimaryAndDuplicates(t *testing.T) {
	h := newHarness(t)
	h.feed.add(itemFeedRow(1, 1, "Tea", 300))
	h.feed.add(itemFeedRow(2, 2, "Cake", 400))
	h.feed.add(barcodeRow(barcodeID(1), 3, eanA, itemID(1), nil, true))
	h.feed.add(barcodeRow(barcodeID(2), 4, "TEA-2", itemID(1), nil, false))
	h.mustRun()

	// The till moved eanA to another item on its own; the cloud's next
	// version of the row (now another code) must not delete it there.
	if _, err := h.db.Exec(`UPDATE item_barcodes SET item_id = ? WHERE barcode = ?`, itemID(2), eanA); err != nil {
		t.Fatal(err)
	}
	h.feed.add(barcodeRow(barcodeID(1), 5, eanB, itemID(1), nil, false))
	// A second primary demotes the first.
	h.feed.add(barcodeRow(barcodeID(2), 6, "TEA-2", itemID(1), nil, true))
	// Another cloud row naming a code a linked row already put there.
	h.feed.add(barcodeRow(barcodeID(3), 7, "TEA-2", itemID(1), nil, false))
	res := h.mustRun()
	if res.Refused != 1 || res.Applied != 2 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if got := h.barcodeOwner(eanA); got != "item:"+itemID(2)+":EAN13:1" {
		t.Fatalf("another owner's barcode = %q, want left alone", got)
	}
	if got := h.list(`SELECT barcode || ':' || is_primary FROM item_barcodes WHERE item_id = ? ORDER BY barcode`, itemID(1)); got != eanB+":0,TEA-2:1" {
		t.Fatalf("item barcodes = %s, want one primary", got)
	}
	// Tombstoning the refused duplicate leaves the linked row's code.
	h.feed.add(feedRow{Kind: "item_barcode", CloudID: barcodeID(3), Version: 8, Deleted: true})
	h.mustRun()
	if !strings.HasPrefix(h.barcodeOwner("TEA-2"), "item:"+itemID(1)) {
		t.Fatal("a never-applied row's tombstone removed another row's barcode")
	}
}

// The pre-resolution reads the code exactly as applyBarcode does: with a
// second, differently-cased key json.Unmarshal into a struct would take
// the last one, the fields map takes the exact key.
func TestResolveBarcodesReadsTheExactKey(t *testing.T) {
	h := newHarness(t)
	r := &runner{cat: data.NewCatalogRepo(h.db)}
	got := r.resolveBarcodes(context.Background(), []Row{{Kind: "item_barcode", CloudID: barcodeID(1), Version: 1,
		Data: json.RawMessage(`{"barcode":"TEA-B","Barcode":"TEA-A","item_cloud_id":"` + itemID(1) + `"}`)}})
	if _, ok := got["TEA-B"]; !ok || len(got) != 1 {
		t.Fatalf("resolved = %v, want only the exact key's TEA-B", got)
	}
	// And a miss is a refusal, never an infra error that wedges the feed.
	f, _ := decodeFields(json.RawMessage(`{"barcode":"NOT-RESOLVED","item_cloud_id":"` + itemID(1) + `"}`))
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	r.links = data.NewCloudLinkRepo()
	if _, err := tx.Exec(`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, ?, 1, 'x')`, itemID(1), itemID(1)); err != nil {
		t.Fatal(err)
	}
	_, _, err = r.applyBarcode(context.Background(), tx, barcodeID(1), "", f)
	if err == nil || isInfra(context.Background(), err) {
		t.Fatalf("unresolved code: err = %v, want a plain refusal", err)
	}
}

func TestPullVariantOptionValuesAreOnePerSetAndBounded(t *testing.T) {
	h := newHarness(t)
	h.shirt(1)
	h.feed.add(setRow(cSet2, 3, "Colour", val(4, "Red"), val(5, "Blue")))
	h.feed.add(variantRow(variantID(2), 4, variantData(itemID(1), "SM", "", 1500, valID(1), valID(2))))          // two values of one set
	h.feed.add(variantRow(variantID(3), 5, variantData(itemID(1), "X", "", 1500, valID(1), valID(4), valID(5)))) // over the axis cap
	// A value of a set the item does not use (the cloud may keep it after
	// detaching the set) is provenance only: kept, not refused.
	h.feed.add(variantRow(variantID(1), 6, variantData(itemID(1), "S Red", "", 1500, valID(1), valID(4))))
	res := h.mustRun()
	if res.Refused != 2 {
		fails, _ := applyFailures(h.db)
		t.Fatalf("result = %+v, failures %+v", res, fails)
	}
	if n := h.count(`SELECT COUNT(*) FROM item_variants`); n != 1 {
		t.Fatalf("variants = %d, want only the valid one", n)
	}
	if v, _ := h.variant(variantID(1)); v.Values != valID(1)+","+valID(4) {
		t.Fatalf("values = %q", v.Values)
	}
}
