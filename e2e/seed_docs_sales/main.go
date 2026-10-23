// Command seed_docs_sales gives the docs-shots till a few completed sales
// and two invoices, so the manual's `reports` and `invoices` screenshots show
// representative data instead of an empty register (ut-docs#359).
//
// It runs ONLY when run-till.sh sees UT_DOCS_SHOTS_SEED_SALES=1, which
// playwright.docs.config.ts sets for the docs tills and nothing else does —
// the other configs that boot run-till.sh (e.g. the locale audit) never set
// the flag, so their specs never find these rows.
//
// DETERMINISM. The sales are written through the real pos.CompleteSale (so
// lines, tax, payments and stock are consistent with what a till produces),
// then re-stamped with FIXED mid-December-2025 timestamps: CompleteSale
// reads the wall clock and internal/clock does not reach it. Midday UTC on
// the 12th/15th/18th keeps every sale on the same calendar day and month in
// any timezone from UTC-11 to UTC+11; the docs tills run with TZ=UTC
// (DOCS_SHOTS_ENV) so a runner at +12..+14 cannot shift the dates the
// invoice register prints. Rows the re-stamp does not touch (stock
// movements, audit rows, payments.local_date) keep the seed run's wall
// clock on purpose: no screenshotted route renders them. The screenshots
// must therefore address that fixed
// window explicitly — see pinnedQuery in e2e/tests-docs/docs-shots.spec.ts —
// because /reports and /invoices default to windows anchored on the wall
// clock. Keep the dates here and the pinned window there in step.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// The seller written onto the seeded invoices. Must equal what
// ensureInvoiceSeller in docs-shots.spec.ts configures on the till, so the
// invoice list/detail and the settings page agree.
const (
	sellerName    = "Demo Shop Ltd"
	sellerAddress = "1 High Street, Demo Town"
	sellerVATNo   = "GB123456789"
)

type lineSpec struct {
	itemID, sku, barcode, name string
	qty                        float64
	unitMinor                  int64
	rateBP                     int
}

type saleSpec struct {
	id, at, method string
	lines          []lineSpec
	// invoice, when set, issues an invoice to this customer on the same day.
	invoiceTo *customer
}

type customer struct{ name, address, vatNo string }

// Prices and rates are the demo catalogue's own (internal/data/seeddata):
// itm001/itm002 at the 20% standard rate, itm003/itm005/itm007 at 0%.
var (
	cola   = lineSpec{"itm001", "SKU-0001", "5000000000012", "Coca-Cola Can 330ml", 1, 120, 2000}
	pepsi  = lineSpec{"itm002", "SKU-0002", "5000000000029", "Pepsi Can 330ml", 1, 115, 2000}
	water  = lineSpec{"itm003", "SKU-0003", "5000000000036", "Sparkling Water 500ml", 1, 85, 0}
	orange = lineSpec{"itm005", "SKU-0005", "5000000000050", "Orange Juice 1L", 1, 220, 0}
	milk   = lineSpec{"itm007", "SKU-0007", "5000000000074", "Semi-Skimmed Milk 2L", 1, 190, 0}
)

func qty(l lineSpec, n float64) lineSpec { l.qty = n; return l }

var sales = []saleSpec{
	{id: "docs-sale-1", at: "2025-12-12T12:00:00Z", method: "cash",
		lines:     []lineSpec{qty(cola, 2), orange},
		invoiceTo: &customer{"Example Cafe Ltd", "5 Market Square, Demo Town", "GB987654321"}},
	{id: "docs-sale-2", at: "2025-12-15T12:00:00Z", method: "card",
		lines:     []lineSpec{pepsi, qty(milk, 2)},
		invoiceTo: &customer{"Sample Catering Ltd", "12 Station Road, Demo Town", "GB555123456"}},
	{id: "docs-sale-3", at: "2025-12-18T12:00:00Z", method: "cash",
		lines: []lineSpec{qty(water, 3), cola}},
}

// channelOrders are the "Online orders" page's sample rows (ut-docs#4768, the
// channel-orders help topic): three web-shop orders at fixed October-2025
// times, one shipped, one new and one partly refunded with a unit restocked,
// so the screenshot shows every column the help text describes. Written
// through the real ChannelOrdersRepo.Apply (stock lowered by channel_sale
// movements), so the page is exactly what a recorded order looks like. Not
// till sales: they add nothing to /reports or /invoices.
func channelOrders() []data.ChannelOrderInput {
	line := func(id string, l lineSpec, n, refunded, restocked int64) data.ChannelOrderLineInput {
		return data.ChannelOrderLineInput{LineID: id, CloudItemID: l.itemID, SKU: l.sku, Name: l.name, Qty: n,
			UnitPrice: money.FromMinor(l.unitMinor), TaxBP: int64(l.rateBP), TaxIncluded: true,
			RefundedQuantity: refunded, RestockedQuantity: restocked}
	}
	order := func(n int, placed, status, payment string, total, refund int64, lines ...data.ChannelOrderLineInput) data.ChannelOrderInput {
		return data.ChannelOrderInput{
			DirectiveID: fmt.Sprintf("docs-dir-%d", n), ConnectionID: "docs-conn-1",
			PluginKey: "demo.webshop", ExternalOrderID: fmt.Sprintf("docs-ext-%d", n),
			ExternalVersion: "1", Revision: 1, OrderNumber: fmt.Sprintf("#%d", 1000+n), PlacedAt: placed,
			Currency: "GBP", Status: status, PaymentState: payment,
			Shipping: money.FromMinor(399), ShippingTaxIncluded: true,
			RefundTotal: money.FromMinor(refund), Total: money.FromMinor(total), CustomerCountry: "GB",
			LocationID: "loc_main", ActorID: "system", Lines: lines,
		}
	}
	return []data.ChannelOrderInput{
		order(1, "2025-10-20T09:30:00Z", "shipped", "paid", 859, 0, line("l-1", cola, 2, 0, 0), line("l-2", orange, 1, 0, 0)),
		order(2, "2025-10-21T14:05:00Z", "new", "paid", 894, 0, line("l-1", pepsi, 1, 0, 0), line("l-2", milk, 2, 0, 0)),
		order(3, "2025-10-22T16:45:00Z", "partially_refunded", "partially_refunded", 654, 85, line("l-1", water, 3, 1, 1)),
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fatalf("UT_DATA_DIR must be set")
	}
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	if err := seed(context.Background(), conn.DB); err != nil {
		fatalf("seed docs sales: %v", err)
	}
}

// seed writes the sales and invoices. Idempotent: a sale already present
// (by its fixed id) is left alone.
func seed(ctx context.Context, sqlDB *sql.DB) error {
	repo := data.NewPOSRepo(sqlDB)
	invoices := data.NewInvoiceRepo(sqlDB)
	series, _, err := data.NewSettingsRepo(sqlDB).Get(ctx, "sync.receipt_prefix")
	if err != nil {
		return fmt.Errorf("read receipt prefix: %w", err)
	}
	sellerJSON, _ := json.Marshal(struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		VATNo   string `json:"vat_no"`
	}{sellerName, sellerAddress, sellerVATNo})

	for _, s := range sales {
		var exists int
		if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE id = ?`, s.id).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}

		var total int64
		in := pos.SaleInput{
			SaleType: "sale", SaleID: s.id, Currency: "GBP", TaxInclusive: true,
			CashierID: "system", ActorID: "system", AllowNegativeInventory: true,
		}
		for _, l := range s.lines {
			in.Lines = append(in.Lines, pos.SaleLineInput{
				ItemID: l.itemID, SKU: l.sku, Barcode: l.barcode, Name: l.name,
				Qty: l.qty, UnitPrice: money.FromMinor(l.unitMinor),
				TaxRateBasisPoints: l.rateBP, LocationID: "loc_main",
			})
			total += int64(l.qty) * l.unitMinor
		}
		in.Payments = []pos.PaymentInput{{MethodID: s.method, Amount: money.FromMinor(total)}}
		if _, err := pos.CompleteSale(ctx, sqlDB, in); err != nil {
			return fmt.Errorf("complete %s: %w", s.id, err)
		}
		if _, err := sqlDB.ExecContext(ctx,
			`UPDATE sales SET created_at = ?1, completed_at = ?1, local_date = date(?1, 'localtime') WHERE id = ?2`, s.at, s.id); err != nil {
			return fmt.Errorf("restamp %s: %w", s.id, err)
		}
		if _, err := sqlDB.ExecContext(ctx, `UPDATE payments SET paid_at = ? WHERE sale_id = ?`, s.at, s.id); err != nil {
			return fmt.Errorf("restamp payments of %s: %w", s.id, err)
		}

		// Weeks-old sales are collected orders, not live ones: /orders (the
		// order-status topic) would otherwise list them as "Not started" with
		// cancel buttons. Stamped at the sale's own fixed time.
		receiptNo, ok, err := repo.GetReceiptNo(ctx, s.id)
		if err != nil || !ok {
			return fmt.Errorf("receipt of %s: ok=%v err=%v", s.id, ok, err)
		}
		if _, _, err := repo.ApplyOrderStatus(ctx, receiptNo, pos.OrderStatusCollected, "system", s.at,
			func(current string) bool { return pos.OrderStatusAllowed(current, pos.OrderStatusCollected) }); err != nil {
			return fmt.Errorf("collect %s: %w", s.id, err)
		}

		if s.invoiceTo == nil {
			continue
		}
		sale, ok, err := repo.GetSaleDetailByID(ctx, s.id)
		if err != nil || !ok {
			return fmt.Errorf("read back %s: ok=%v err=%v", s.id, ok, err)
		}
		// The same banding invoice_page.go's vatBreakdown runs.
		vl := make([]pos.VATLine, 0, len(sale.Lines))
		for _, l := range sale.Lines {
			vl = append(vl, pos.VATLine{RateBP: l.TaxRateBP, LineTotal: l.LineTotal, TaxAmount: l.TaxAmount})
		}
		inclusive := pos.InferTaxInclusive(sale.Subtotal, sale.DiscountTotal, sale.TaxTotal, sale.Total, sale.ServiceCharge, sale.VoucherIssueTotal)
		type band struct {
			RateBP int   `json:"rate_bp"`
			Net    int64 `json:"net"`
			Tax    int64 `json:"tax"`
			Gross  int64 `json:"gross"`
		}
		var bands []band
		var net, tax int64
		for _, b := range pos.VATBandsForSale(vl, sale.DiscountTotal, inclusive, pos.SaleChargesForBanding(sale.Charges, sale.ServiceCharge, sale.ServiceChargeTaxBasisBP)) {
			bands = append(bands, band{b.RateBP, b.Net, b.Tax, b.Gross})
			net, tax = net+b.Net, tax+b.Tax
		}
		bandsJSON, _ := json.Marshal(bands)
		if _, err := invoices.Create(ctx, data.InvoiceInput{
			Series: strings.TrimSpace(series), Kind: "invoice", SaleID: s.id,
			CustomerName: s.invoiceTo.name, CustomerAddress: s.invoiceTo.address, CustomerVATNo: s.invoiceTo.vatNo,
			SellerJSON: string(sellerJSON),
			NetTotal:   net, TaxTotal: tax, GrossTotal: net + tax,
			VATBreakdownJSON: string(bandsJSON),
			IssuedAt:         s.at, IssuedBy: "system",
		}); err != nil {
			return fmt.Errorf("invoice for %s: %w", s.id, err)
		}
	}
	// Idempotent through the repo's own duplicate-directive check.
	co := data.NewChannelOrdersRepo(sqlDB)
	for _, o := range channelOrders() {
		if _, err := co.Apply(ctx, o); err != nil {
			return fmt.Errorf("channel order %s: %w", o.OrderNumber, err)
		}
	}
	return nil
}
