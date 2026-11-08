package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ADR-0169 §9, ut-docs#5427: Reports → Online channels totals.

var (
	reportFrom = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	reportTo   = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

// seedReportOrder inserts directly so status/payment_state are exact and not
// filtered through Apply's status machine.
func seedReportOrder(t *testing.T, d *sql.DB, id, plugin, cur, placedAt, status, pay string, total, refund int64, currencyMismatch bool) {
	t.Helper()
	mm := 0
	if currencyMismatch {
		mm = 1
	}
	mustExecCO(t, d, `INSERT INTO channel_orders(id, connection_id, plugin_key, external_order_id, revision, placed_at,
  currency, status, payment_state, total_minor, refund_total_minor, currency_mismatch, created_at, updated_at)
VALUES(?, 'conn-'||?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, id, plugin, "ext-"+id, placedAt, cur, status, pay, total, refund, mm, placedAt, placedAt)
}

func TestChannelSummary_GroupsByChannelAndCurrency(t *testing.T) {
	d, repo := openChannelOrders(t)
	seedReportOrder(t, d, "a", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "new", "paid", 5000, 0, false)
	seedReportOrder(t, d, "b", "com.x.shop", "GBP", "2026-10-06T10:00:00Z", "new", "partially_refunded", 3000, 1000, false)
	seedReportOrder(t, d, "c", "com.x.market", "GBP", "2026-10-07T10:00:00Z", "new", "paid", 2000, 0, false)
	seedReportOrder(t, d, "e", "com.x.shop", "EUR", "2026-10-08T10:00:00Z", "new", "paid", 4000, 0, false)

	sum, err := repo.SummaryByWindow(context.Background(), reportFrom, reportTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Groups) != 2 || sum.Groups[0].Currency != "EUR" || sum.Groups[1].Currency != "GBP" {
		t.Fatalf("groups = %+v", sum.Groups)
	}
	eur := sum.Groups[0]
	if len(eur.Rows) != 1 || eur.Total != nil || eur.Rows[0].Gross != money.FromMinor(4000) {
		t.Fatalf("eur = %+v", eur)
	}
	gbp := sum.Groups[1]
	if len(gbp.Rows) != 2 {
		t.Fatalf("gbp rows = %+v", gbp.Rows)
	}
	want := []ChannelSummaryRow{
		{PluginKey: "com.x.market", Currency: "GBP", Orders: 1, Gross: 2000, Refunds: 0, Net: 2000},
		{PluginKey: "com.x.shop", Currency: "GBP", Orders: 2, Gross: 8000, Refunds: 1000, Net: 7000},
	}
	for i, w := range want {
		if gbp.Rows[i] != w {
			t.Fatalf("row %d = %+v, want %+v", i, gbp.Rows[i], w)
		}
	}
	if gbp.Total == nil || *gbp.Total != (ChannelSummaryRow{Currency: "GBP", Orders: 3, Gross: 10000, Refunds: 1000, Net: 9000}) {
		t.Fatalf("gbp total = %+v", gbp.Total)
	}
}

func TestChannelSummary_WindowIsHalfOpenOnPlacedAt(t *testing.T) {
	d, repo := openChannelOrders(t)
	seedReportOrder(t, d, "o1", "com.x.shop", "GBP", "2026-09-30T23:59:59Z", "new", "paid", 100, 0, false)
	seedReportOrder(t, d, "o2", "com.x.shop", "GBP", "2026-10-01T00:00:00Z", "new", "paid", 100, 0, false)
	seedReportOrder(t, d, "o3", "com.x.shop", "GBP", "2026-10-31T23:59:59.5Z", "new", "paid", 100, 0, false)
	seedReportOrder(t, d, "o4", "com.x.shop", "GBP", "2026-11-01T00:00:00Z", "new", "paid", 100, 0, false)
	sum, err := repo.SummaryByWindow(context.Background(), reportFrom, reportTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Groups) != 1 || sum.Groups[0].Rows[0].Orders != 2 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestChannelSummary_CancelledBeforePaymentNotCounted(t *testing.T) {
	d, repo := openChannelOrders(t)
	for i, pay := range []string{"", "pending", "authorized", "voided"} {
		seedReportOrder(t, d, "x"+pay+string(rune('a'+i)), "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "cancelled", pay, 9999, 0, false)
	}
	seedReportOrder(t, d, "paid", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "cancelled", "paid", 5000, 5000, false)
	seedReportOrder(t, d, "part", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "cancelled", "partially_refunded", 3000, 1000, false)
	seedReportOrder(t, d, "newp", "com.x.shop", "GBP", "2026-10-05T10:00:00Z", "new", "pending", 700, 0, false)

	sum, err := repo.SummaryByWindow(context.Background(), reportFrom, reportTo)
	if err != nil {
		t.Fatal(err)
	}
	if sum.CancelledUnpaid != 4 {
		t.Fatalf("CancelledUnpaid = %d, want 4", sum.CancelledUnpaid)
	}
	r := sum.Groups[0].Rows[0]
	if r.Orders != 3 || r.Gross != 8700 || r.Refunds != 6000 || r.Net != 2700 {
		t.Fatalf("row = %+v", r)
	}
}

func TestChannelSummary_RefundDatedByPlacedAt(t *testing.T) {
	d, repo := openChannelOrders(t)
	seedReportOrder(t, d, "o1", "com.x.shop", "GBP", "2026-10-10T10:00:00Z", "new", "partially_refunded", 5000, 1200, false)
	mustExecCO(t, d, `UPDATE channel_orders SET updated_at='2026-11-15T09:00:00Z' WHERE id='o1'`) // fixed-date:ok data only: SummaryByWindow is queried with explicit reportFrom/reportTo, no real-clock check reads it
	oct, err := repo.SummaryByWindow(context.Background(), reportFrom, reportTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(oct.Groups) != 1 || oct.Groups[0].Rows[0].Refunds != 1200 {
		t.Fatalf("oct = %+v", oct)
	}
	nov, err := repo.SummaryByWindow(context.Background(), reportTo, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(nov.Groups) != 0 {
		t.Fatalf("nov = %+v", nov)
	}
}

func TestChannelSummary_CurrencyMismatchKeptInOwnCurrency(t *testing.T) {
	d, repo := openChannelOrders(t)
	seedReportOrder(t, d, "o1", "com.x.shop", "USD", "2026-10-10T10:00:00Z", "new", "paid", 2500, 0, true)
	sum, err := repo.SummaryByWindow(context.Background(), reportFrom, reportTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Groups) != 1 || sum.Groups[0].Currency != "USD" || sum.Groups[0].Rows[0].Gross != 2500 {
		t.Fatalf("sum = %+v", sum)
	}
}

func TestChannelSummary_EmptyAndHasAny(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	if has, err := repo.HasAny(ctx); err != nil || has {
		t.Fatalf("HasAny empty = %v, %v", has, err)
	}
	sum, err := repo.SummaryByWindow(ctx, reportFrom, reportTo)
	if err != nil || len(sum.Groups) != 0 || sum.CancelledUnpaid != 0 {
		t.Fatalf("empty summary = %+v, %v", sum, err)
	}
	seedReportOrder(t, d, "o1", "com.x.shop", "GBP", "2026-01-10T10:00:00Z", "new", "paid", 100, 0, false)
	if has, err := repo.HasAny(ctx); err != nil || !has {
		t.Fatalf("HasAny after insert = %v, %v", has, err)
	}
	sum, err = repo.SummaryByWindow(ctx, reportFrom, reportTo)
	if err != nil || len(sum.Groups) != 0 {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
}
