package catalogpull

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0162 §4, ut-docs#4494: a fresh till runs the setup wizard before it
// can be paired, so the cloud-master pull meets the till's seed rows — the
// migration's tax codes and the wizard's gift voucher item — and must deal
// with them instead of refusing the shop's catalogue.

func namedTaxRow(id string, v int64, name string, rate int) feedRow {
	return feedRow{Kind: "tax_code", CloudID: id, Version: v, Data: map[string]any{
		"name": name, "rate_basis_points": rate, "takeaway_rate_basis_points": nil, "active": true,
	}}
}

func (h *harness) link(entity, cloudID string) (string, bool) {
	h.t.Helper()
	local, ok, err := data.NewCloudLinkRepo().LocalID(context.Background(), h.db, entity, cloudID)
	if err != nil {
		h.t.Fatal(err)
	}
	return local, ok
}

func TestPullAdoptsTheSeedTaxCodeOfTheSameName(t *testing.T) {
	h := newHarness(t)
	h.feed.add(namedTaxRow(cTax, 1, "Standard VAT", 1900))
	it := itemRowData("Coffee", 300)
	it["tax_code_cloud_id"] = cTax
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(1), Version: 1, Data: it})

	res := h.mustRun()
	if res.Refused != 0 || res.Applied != 2 {
		t.Fatalf("result = %+v, want 2 applied and nothing refused", res)
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_apply_failures`); n != 0 {
		t.Fatalf("%d apply failures recorded, want none", n)
	}
	if local, ok := h.link("tax_code", cTax); !ok || local != "tax_std" {
		t.Fatalf("tax code link = %q %v, want the seed tax_std", local, ok)
	}
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE name = 'Standard VAT'`); n != 1 {
		t.Fatalf("%d tax codes named Standard VAT, want exactly one", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE id = 'tax_std' AND rate_basis_points = 1900 AND is_active = 1`); n != 1 {
		t.Fatal("tax_std did not take the cloud's rate (1900)")
	}
	got, ok := h.item(itemID(1))
	if !ok || got.TaxCode != "tax_std" {
		t.Fatalf("item = %+v %v, want it applied with tax_std", got, ok)
	}
	var payload string
	if err := h.db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'cloud_catalog_tax_code_saved' AND entity_id = 'tax_std'`).Scan(&payload); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if p["adopted_seed"] != true || p["created"] != false || p["cloud_id"] != cTax {
		t.Fatalf("audit payload = %v, want adopted_seed true, created false", p)
	}

	// The adopted seed row is an ordinary linked row from now on: a
	// tombstone deactivates it (the till has no tax-code delete).
	h.feed.add(feedRow{Kind: "tax_code", CloudID: cTax, Version: 2, Deleted: true})
	h.mustRun()
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE id = 'tax_std' AND is_active = 0`); n != 1 {
		t.Fatal("a tombstone of the adopted seed tax code did not deactivate it")
	}
	if _, ok := h.link("tax_code", cTax); ok {
		t.Fatal("the tombstoned link stayed")
	}
}

func TestPullDoesNotAdoptASeedTaxCodeAlreadyLinked(t *testing.T) {
	h := newHarness(t)
	h.feed.add(namedTaxRow(cTax, 1, "Standard VAT", 1900))
	h.feed.add(namedTaxRow(cTax2, 2, "Standard VAT", 1900))
	res := h.mustRun()
	if res.Refused != 1 {
		t.Fatalf("result = %+v, want the second same-named row refused", res)
	}
	if local, ok := h.link("tax_code", cTax); !ok || local != "tax_std" {
		t.Fatalf("first row link = %q %v, want tax_std", local, ok)
	}
	if _, ok := h.link("tax_code", cTax2); ok {
		t.Fatal("the second row took over a seed tax code already linked to another cloud row")
	}
	if n := h.count(`SELECT COUNT(*) FROM cloud_apply_failures WHERE entity = 'tax_code' AND cloud_id = ?`, cTax2); n != 1 {
		t.Fatal("the second row's refusal was not recorded")
	}
}

func TestPullDoesNotAdoptANonSeedTaxCodeOfTheSameName(t *testing.T) {
	h := newHarness(t)
	if _, err := h.db.Exec(`INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES ('tax_local', 'Takeaway VAT', 700, 1)`); err != nil {
		t.Fatal(err)
	}
	h.feed.add(namedTaxRow(cTax, 1, "Takeaway VAT", 700))
	res := h.mustRun()
	if res.Refused != 1 {
		t.Fatalf("result = %+v, want the row refused (general adoption is ut-docs#4465)", res)
	}
	if _, ok := h.link("tax_code", cTax); ok {
		t.Fatal("a non-seed local tax code was adopted")
	}
	// A seed name that differs only by case is a different name to the
	// UNIQUE constraint (BINARY collation): created, not adopted.
	h.feed.add(namedTaxRow(cTax2, 2, "standard vat", 1900))
	h.mustRun()
	if local, ok := h.link("tax_code", cTax2); !ok || local != cTax2 {
		t.Fatalf("case-different name link = %q %v, want a new row under its cloud id", local, ok)
	}
	if n := h.count(`SELECT COUNT(*) FROM tax_codes WHERE id = 'tax_std' AND rate_basis_points = 2000`); n != 1 {
		t.Fatal("tax_std changed though no cloud row named it")
	}
}

// voucherHarness is a harness whose till ran the wizard: the seeded gift
// voucher item exists and is marked.
func voucherHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	if ok, err := data.NewCatalogRepo(h.db).SeedDefaultGiftVoucherItem(ctx, "Gift voucher"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	id, ok, err := data.NewCatalogRepo(h.db).SeededGiftVoucherItemID(ctx)
	if err != nil || !ok {
		t.Fatalf("seeded id: %v %v", ok, err)
	}
	it := itemRowData("Voucher", 0)
	it["item_type"] = "gift_voucher"
	it["stock_untracked"] = true
	h.feed.add(feedRow{Kind: "item", CloudID: itemID(9), Version: 1, Data: it})
	return h, id
}

func TestPullRetiresTheSeededGiftVoucherItemOnceTheShopsOwnArrives(t *testing.T) {
	h, seeded := voucherHarness(t)
	var asked []string
	h.opt.ItemInLiveBasket = func(_ context.Context, id string) (bool, error) {
		asked = append(asked, id)
		return false, nil
	}
	res := h.mustRun()
	if !res.Changed {
		t.Fatalf("result = %+v, want changed", res)
	}
	if _, ok := h.item(seeded); ok {
		t.Fatal("the seeded gift voucher item stayed beside the shop's own")
	}
	if _, ok := h.item(itemID(9)); !ok {
		t.Fatal("the pulled voucher item is missing")
	}
	if len(asked) != 1 || asked[0] != seeded {
		t.Fatalf("live basket asked about %v, want the seeded item", asked)
	}
	if h.setting(data.SeededGiftVoucherItemKey) != "" {
		t.Fatal("the seeded-item marker stayed after the retire")
	}
}

func TestPullKeepsTheSeededGiftVoucherItemInALiveBasket(t *testing.T) {
	h, seeded := voucherHarness(t)
	h.opt.ItemInLiveBasket = func(_ context.Context, id string) (bool, error) { return id == seeded, nil }
	h.mustRun()
	if _, ok := h.item(seeded); !ok {
		t.Fatal("the seeded item was removed while in a live basket")
	}
	if h.setting(data.SeededGiftVoucherItemKey) != seeded {
		t.Fatal("the marker was cleared: the next run could not retry")
	}

	// The basket check failing keeps it too, and never fails the run.
	h.opt.ItemInLiveBasket = func(context.Context, string) (bool, error) { return false, errors.New("basket down") }
	h.feed.add(itemFeedRow(1, 2, "Tea", 200))
	if _, err := h.run(); err != nil {
		t.Fatalf("a failing basket check failed the run: %v", err)
	}
	if _, ok := h.item(seeded); !ok {
		t.Fatal("the seeded item was removed though the basket check failed")
	}

	// The next run that applies something retries, and removes it.
	h.opt.ItemInLiveBasket = nil
	h.feed.add(itemFeedRow(2, 3, "Juice", 200))
	h.mustRun()
	if _, ok := h.item(seeded); ok {
		t.Fatal("the next run did not retry the retire")
	}
}

func TestPullKeepsASeededGiftVoucherItemTheShopEdited(t *testing.T) {
	h, seeded := voucherHarness(t)
	if _, err := h.db.Exec(`UPDATE items SET name = 'Our voucher' WHERE id = ?`, seeded); err != nil {
		t.Fatal(err)
	}
	h.mustRun()
	if _, ok := h.item(seeded); !ok {
		t.Fatal("a seeded item the shop renamed was removed")
	}
}
