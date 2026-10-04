package pages

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#2537: the print_report hook lays the cloud's ready-made report
// out as a print.Doc — the EOD report's own 80mm layout — and prints it on
// this till's receipt printer, failing (never a silent "applied") when
// there is no printer or the print does not go through.

func samplePrintReport(truncated int) cloudsync.PrintReport {
	return cloudsync.PrintReport{
		DeviceID: "dev-self", ReportKind: "overview", PeriodLabel: "2026-09-01 to 2026-09-07",
		KPIs: []cloudsync.PrintReportKPI{{Label: "Net sales", Value: "£130.00"}, {Label: "Sales", Value: "5"}},
		Rows: []cloudsync.PrintReportRow{
			{Label: "09:00", Values: []string{"4", "£128.40"}},
			{Label: "A very long item name that cannot fit on one receipt row", Values: []string{"1", "£1.60"}},
		},
		TruncatedCount: truncated,
	}
}

func TestBuildPrintReportDoc(t *testing.T) {
	doc := buildPrintReportDoc(samplePrintReport(0), "Corner Shop", "cp858")
	if doc.StoreName != "Corner Shop" || doc.Charset != "cp858" {
		t.Fatalf("doc = %+v", doc)
	}
	if len(doc.Meta) < 2 || doc.Meta[0] != "SALES OVERVIEW" || doc.Meta[1] != "2026-09-01 to 2026-09-07" {
		t.Fatalf("meta = %q", doc.Meta)
	}
	if len(doc.Totals) != 2 || doc.Totals[0] != (print.KV{Label: "Net sales", Amount: "£130.00"}) || doc.Totals[1].Amount != "5" {
		t.Fatalf("totals = %+v", doc.Totals)
	}
	footer := strings.Join(doc.Footer, "\n")
	if !strings.Contains(footer, "Hour") || !strings.Contains(footer, "09:00") || !strings.Contains(footer, "£128.40") {
		t.Fatalf("footer = %q", footer)
	}
	for _, l := range doc.Footer {
		if n := len([]rune(l)); n > print.Width {
			t.Fatalf("footer line %q is %d runes, over the %d-column roll", l, n, print.Width)
		}
		// A long label is clipped, never the figures (footerRow's rule).
		if strings.HasPrefix(l, "A very long") && !strings.HasSuffix(l, "£1.60") {
			t.Fatalf("figures clipped off %q", l)
		}
	}
	if strings.Contains(footer, "more (see cloud manager)") {
		t.Fatalf("a whole report printed a +N more line: %q", footer)
	}

	doc = buildPrintReportDoc(samplePrintReport(17), "Corner Shop", "utf8")
	if last := doc.Footer[len(doc.Footer)-1]; last != "+17 more (see cloud manager)" {
		t.Fatalf("last footer line = %q", last)
	}

	// An unknown kind (a newer cloud) still prints, titled by its id.
	r := samplePrintReport(0)
	r.ReportKind = "margins"
	if doc := buildPrintReportDoc(r, "S", ""); doc.Meta[0] != "MARGINS" {
		t.Fatalf("unknown kind title = %q", doc.Meta[0])
	}
}

func setNetworkPrinter(t *testing.T, dp *common.Deps, addr string) {
	t.Helper()
	for k, v := range map[string]string{keyPrinterMode: "network", keyPrinterAddress: addr, keyPrinterCharset: "utf8", "store.name": "Corner Shop"} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloudPrintReport_PrintsOnConfiguredPrinter(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	printer := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, dp, printer.addr)

	msg, err := cloudPrintReport(t.Context(), dp, samplePrintReport(3))
	if err != nil || msg != "printed" {
		t.Fatalf("cloudPrintReport = %q, %v", msg, err)
	}
	got := printer.Settle(1)
	if len(got) != 1 {
		t.Fatalf("printed %d jobs, want 1", len(got))
	}
	for _, want := range []string{"Corner Shop", "SALES OVERVIEW", "2026-09-01 to 2026-09-07", "Net sales", "£128.40", "+3 more (see cloud manager)"} {
		if !strings.Contains(got[0], want) {
			t.Fatalf("printout lacks %q:\n%s", want, got[0])
		}
	}
}

func TestCloudPrintReport_NoPrinterFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	if _, err := cloudPrintReport(t.Context(), dp, samplePrintReport(0)); err == nil || !strings.Contains(err.Error(), "no receipt printer") {
		t.Fatalf("err = %v, want a no-printer failure", err)
	}
}

func TestCloudPrintReport_UnreachablePrinterFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	setNetworkPrinter(t, dp, addr)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := cloudPrintReport(ctx, dp, samplePrintReport(0)); err == nil || !strings.HasPrefix(err.Error(), "print failed") {
		t.Fatalf("err = %v, want print failed", err)
	}
}

func TestBuildCloudHooks_WiresPrintReport(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	hooks := buildCloudHooks(dp, func(context.Context) {})
	if hooks.PrintReport == nil {
		t.Fatal("PrintReport hook not wired")
	}
	printer := newFakeKitchenPrinter(t)
	setNetworkPrinter(t, dp, printer.addr)
	if _, err := hooks.PrintReport(t.Context(), samplePrintReport(0)); err != nil {
		t.Fatalf("PrintReport: %v", err)
	}
	if got := printer.Settle(1); len(got) != 1 {
		t.Fatalf("printed %d jobs", len(got))
	}
}
