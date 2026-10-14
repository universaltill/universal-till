package pages

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudlink"
	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0155 §3's allow-list, per data type: SaleDetail field → wire key.
// Every field of the three types is either here or in the never-list
// below; a new field fails TestCloudLinkSaleDetailClassifiesEveryField
// until someone decides (and an ADR agrees) whether the cloud may see it.
var (
	saleDetailAllowed = map[string]string{
		"ID": "id", "ReceiptNo": "receipt_no", "DisplayNo": "display_no", "CreatedAt": "created_at",
		"Status": "status", "SaleType": "sale_type", "OrderType": "order_type", "TableLabel": "table_label",
		"Currency": "currency", "Subtotal": "subtotal_minor", "DiscountTotal": "discount_total_minor",
		"TaxTotal": "tax_total_minor", "ServiceCharge": "service_charge_minor", "Total": "total_minor",
		"RegisterID": "register_id", "Lines": "lines", "Payments": "payments",
	}
	saleDetailNever = []string{
		"CashierID", "TableID", "SyncStatus", "Offline", "Charges", "VoucherIssueTotal",
		"ServiceChargeTaxBasisBP", "VoucherIssues",
		"TenderType", // not on §3's list: my. reads tender kind from the live summary
	}
	saleDetailLineAllowed = map[string]string{
		"Name": "name", "SKU": "sku", "ItemID": "item_id", "VariantID": "variant_id", "Qty": "qty",
		"UnitPrice": "unit_price_minor", "LineDiscount": "line_discount_minor", "TaxRateBP": "tax_rate_bp",
		"TaxAmount": "tax_minor", "LineTotal": "line_total_minor", "Modifiers": "modifiers", "OrderType": "order_type",
	}
	saleDetailLineNever      = []string{"ID"}
	saleDetailPaymentAllowed = map[string]string{
		"Method": "method", "Amount": "amount_minor", "ChangeGiven": "change_given_minor", "TipAmount": "tip_minor",
	}
	saleDetailPaymentNever = []string{
		"Reference", "MaskedPAN", "AuthCode", "TerminalID", "TraceID", "VoucherID", "PaidAt", "TipRecipient",
	}
)

// A field SaleDetail (the LAN journal contract) gains later must not reach
// the cloud by default (ADR-0155 §3): every field is classified.
func TestCloudLinkSaleDetailClassifiesEveryField(t *testing.T) {
	for _, tc := range []struct {
		typ     reflect.Type
		allowed map[string]string
		never   []string
	}{
		{reflect.TypeOf(data.SaleDetail{}), saleDetailAllowed, saleDetailNever},
		{reflect.TypeOf(data.SaleDetailLine{}), saleDetailLineAllowed, saleDetailLineNever},
		{reflect.TypeOf(data.SaleDetailPayment{}), saleDetailPaymentAllowed, saleDetailPaymentNever},
	} {
		seen := map[string]bool{}
		for i := range tc.typ.NumField() {
			name := tc.typ.Field(i).Name
			seen[name] = true
			_, ok := tc.allowed[name]
			if ok == slices.Contains(tc.never, name) {
				t.Errorf("%s.%s is classified %s: put it in exactly one of the allow-list and the never-list (ADR-0155 §3)",
					tc.typ.Name(), name, map[bool]string{true: "as both allowed and never", false: "nowhere"}[ok])
			}
		}
		for name := range tc.allowed {
			if !seen[name] {
				t.Errorf("allow-list names %s.%s, which no longer exists", tc.typ.Name(), name)
			}
		}
		for _, name := range tc.never {
			if !seen[name] {
				t.Errorf("never-list names %s.%s, which no longer exists", tc.typ.Name(), name)
			}
		}
	}
}

// neverSaleDetail is a sale with every never-list field set to a sentinel.
func neverSaleDetail() data.SaleDetail {
	return data.SaleDetail{
		ID: "sale-1", ReceiptNo: "R-1", DisplayNo: "1", Status: "completed", SaleType: "sale",
		TenderType: "SENTINEL-tender", OrderType: "takeaway", TableID: "SENTINEL-table-id", TableLabel: "T4",
		Offline: true, SyncStatus: "SENTINEL-sync", Currency: "GBP",
		Subtotal: 1000, DiscountTotal: 100, TaxTotal: 150, Total: 1050, ServiceCharge: 0,
		ServiceChargeTaxBasisBP: 987601, VoucherIssueTotal: 987602,
		CreatedAt: "2026-10-14T09:30:00Z", CashierID: "SENTINEL-cashier-id", RegisterID: "reg-1",
		Lines: []data.SaleDetailLine{{
			ID: "SENTINEL-line-id", Name: "Flat white", SKU: "FW-1", ItemID: "item-1", VariantID: "var-1",
			TaxRateBP: 2000, Qty: 1.25, UnitPrice: 400, LineDiscount: 50, TaxAmount: 75, LineTotal: 450,
			Modifiers: []string{"Oat milk"}, OrderType: "takeaway",
		}},
		Payments: []data.SaleDetailPayment{{
			Method: "card", Amount: 1050, ChangeGiven: 0, TipAmount: 100, TipRecipient: "SENTINEL-tip-recipient",
			Reference: "SENTINEL-reference", MaskedPAN: "SENTINEL-masked-pan", AuthCode: "SENTINEL-auth-code",
			TerminalID: "SENTINEL-terminal-id", TraceID: "SENTINEL-trace-id", VoucherID: "SENTINEL-voucher-id",
			PaidAt: "SENTINEL-paid-at",
		}},
		Charges: []data.SaleCharge{{Key: "SENTINEL-charge-key", Label: "SENTINEL-charge-label", Amount: 987603}},
		VoucherIssues: []data.SaleDetailVoucherIssue{{
			VoucherID: "SENTINEL-vi-id", HolderLabel: "SENTINEL-holder-label", Amount: 987604,
		}},
	}
}

// jsonKeys collects every object key in v, at any depth.
func jsonKeys(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			into[k] = true
			jsonKeys(e, into)
		}
	case []any:
		for _, e := range x {
			jsonKeys(e, into)
		}
	}
}

// ADR-0155 §3's never-list: none of these keys or values reach the
// cloud, whatever the sale carries.
func TestCloudLinkSaleDetailNeverList(t *testing.T) {
	b, err := json.Marshal(cloudLinkSaleDetailOf(neverSaleDetail(), data.SaleProvenance{CashierName: "Sam"}))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	jsonKeys(v, keys)
	for _, k := range []string{
		"reference", "masked_pan", "auth_code", "terminal_id", "trace_id", "voucher_id", "paid_at", "tip_recipient",
		"cashier_id", "table_id", "sync_status", "offline", "charges", "voucher_issue_total",
		"service_charge_tax_basis_bp", "voucher_issues", "holder_label", "tender_type",
		// data.SaleDetail's own json names for allowed fields: the mapping
		// renames them, so their presence would mean SaleDetail was marshalled.
		"subtotal", "total", "unit_price", "tax_amount", "amount",
	} {
		if keys[k] {
			t.Errorf("payload has never-list key %q: %s", k, b)
		}
	}
	for _, s := range []string{"SENTINEL", "987601", "987602", "987603", "987604"} {
		if strings.Contains(string(b), s) {
			t.Errorf("payload carries never-list value %q: %s", s, b)
		}
	}
}

// ADR-0155 §3: the mapping carries exactly the allow-list, values intact.
func TestCloudLinkSaleDetailMapping(t *testing.T) {
	got := cloudLinkSaleDetailOf(neverSaleDetail(), data.SaleProvenance{TillID: "till-2", RegisterName: "Front Till", CashierName: "Sam"})
	want := cloudlink.SaleDetail{
		ID: "sale-1", ReceiptNo: "R-1", DisplayNo: "1", CreatedAt: "2026-10-14T09:30:00Z",
		Status: "completed", SaleType: "sale", OrderType: "takeaway", TableLabel: "T4", Currency: "GBP",
		SubtotalMinor: 1000, DiscountTotalMinor: 100, TaxTotalMinor: 150, TotalMinor: 1050,
		CashierName: "Sam", TillID: "till-2", RegisterID: "reg-1", RegisterName: "Front Till",
		Lines: []cloudlink.SaleDetailLine{{
			Name: "Flat white", SKU: "FW-1", ItemID: "item-1", VariantID: "var-1", Qty: 1.25,
			UnitPriceMinor: 400, LineDiscountMinor: 50, TaxRateBP: 2000, TaxMinor: 75, LineTotalMinor: 450,
			Modifiers: []string{"Oat milk"}, OrderType: "takeaway",
		}},
		Payments: []cloudlink.SaleDetailPayment{{Method: "card", AmountMinor: 1050, TipMinor: 100}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapping =\n%+v\nwant\n%+v", got, want)
	}

	// The wire keys are §3's list, no more.
	b, _ := json.Marshal(got)
	var top map[string]json.RawMessage
	_ = json.Unmarshal(b, &top)
	var keys []string
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	wantKeys := []string{"cashier_name", "created_at", "currency", "discount_total_minor", "display_no", "id",
		"lines", "lines_omitted", "order_type", "payments", "receipt_no", "register_id", "register_name",
		"sale_type", "service_charge_minor", "status", "subtotal_minor", "table_label", "tax_total_minor",
		"till_id", "total_minor"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("wire keys = %v\nwant %v", keys, wantKeys)
	}

	// A return's header totals are negative on the wire, as in the sale
	// summary (cloudLinkSaleOf); empty slices are [], never null.
	ret := cloudLinkSaleDetailOf(data.SaleDetail{SaleType: "return", Subtotal: 400, DiscountTotal: 20, TaxTotal: 80, ServiceCharge: 10, Total: 470}, data.SaleProvenance{})
	if ret.SubtotalMinor != -400 || ret.DiscountTotalMinor != -20 || ret.TaxTotalMinor != -80 || ret.ServiceChargeMinor != -10 || ret.TotalMinor != -470 {
		t.Fatalf("return totals = %+v, want negative", ret)
	}
	if b, _ := json.Marshal(ret); !strings.Contains(string(b), `"lines":[]`) || !strings.Contains(string(b), `"payments":[]`) {
		t.Fatalf("empty sale = %s, want lines/payments []", b)
	}
	noMods := cloudLinkSaleDetailOf(data.SaleDetail{Lines: []data.SaleDetailLine{{Name: "Tea"}}}, data.SaleProvenance{})
	if b, _ := json.Marshal(noMods); !strings.Contains(string(b), `"modifiers":[]`) {
		t.Fatalf("line without modifiers = %s, want modifiers []", b)
	}
}

// The lookup the cloud link calls (ADR-0155 §1/§3), on a real migrated
// DB: a replica's journaled sale (sales.till_id set, no register on this
// till) answers by its id with its till's provenance; an own sale names
// its register and cashier; an unknown id is not found.
func TestCloudLinkSaleDetailLookup(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	lookup := cloudLinkSaleDetailLookup(dp)

	j := seedJournalSale("remote-sale-sd1", "T2-SD001", "sale", "", "itm1", 2, 150)
	j.Sale.RegisterID = "reg-only-on-the-replica"
	if applied, _, err := applyJournal(ctx, dp, "replica-42", j); err != nil || !applied {
		t.Fatalf("applyJournal: applied=%v err=%v", applied, err)
	}
	got, found, err := lookup(ctx, "remote-sale-sd1")
	if err != nil || !found {
		t.Fatalf("replica sale: found=%v err=%v", found, err)
	}
	if got.TillID != "replica-42" || got.RegisterID != "" || got.RegisterName != "" {
		t.Fatalf("replica provenance = till %q register %q/%q, want till replica-42 and no register", got.TillID, got.RegisterID, got.RegisterName)
	}
	if got.ID != "remote-sale-sd1" || got.TotalMinor != 300 || len(got.Lines) != 1 || got.Lines[0].Qty != 2 || len(got.Payments) != 1 {
		t.Fatalf("replica sale detail = %+v", got)
	}
	if got.CashierName != "Admin" {
		t.Fatalf("cashier_name = %q, want the till user's display name", got.CashierName)
	}

	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id, name, is_active) VALUES('reg-front', 'Front Till', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE sales SET till_id = '', register_id = 'reg-front' WHERE id = 'remote-sale-sd1'`); err != nil {
		t.Fatal(err)
	}
	got, found, err = lookup(ctx, "remote-sale-sd1")
	if err != nil || !found || got.TillID != "" || got.RegisterID != "reg-front" || got.RegisterName != "Front Till" {
		t.Fatalf("own sale: found=%v err=%v till %q register %q/%q", found, err, got.TillID, got.RegisterID, got.RegisterName)
	}

	if _, found, err := lookup(ctx, "no-such-sale"); err != nil || found {
		t.Fatalf("unknown sale: found=%v err=%v, want not found", found, err)
	}
}
