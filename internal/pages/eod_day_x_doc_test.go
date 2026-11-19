package pages

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/print"
)

func dxDocReport() data.EODReport {
	return data.EODReport{
		From: "2025-03-14T08:00:00Z", To: "2025-03-15T09:00:00Z", SalesCount: 2, // fixed-date:ok outside the 60-day window, never read against the clock
		Gross: 1000, Net: 900, TaxNet: 100,
		GeneratedBy: "Alex", Annotation: "should not print",
		FiscalDevice: &data.FiscalDeviceWindow{},
		Methods:      []data.EODMethod{{Method: "cash", In: 1000}},
	}
}

func TestBuildEODDayXDoc_IsZPlusTwoMetaLines(t *testing.T) {
	rep := dxDocReport()
	zRep := rep
	zRep.FiscalDevice, zRep.Annotation = nil, ""
	for _, cs := range []string{"", "utf8", "ascii", "cp858", "win1250"} {
		x := buildEODDayXDoc(rep, "Shop", cs, eodArticlePrintAll, 0)
		z := buildEODDoc(zRep, "Shop", cs, eodArticlePrintAll, 0)
		if len(x.Meta) < 2 || x.Meta[0] != httpx.T("en", "reports.eod.day_x.paper.title") || x.Meta[1] != httpx.T("en", "shifts.x_report.paper.not_a_close") {
			t.Fatalf("%q: Meta = %v", cs, x.Meta)
		}
		x.Meta = x.Meta[2:]
		if !reflect.DeepEqual(x, z) {
			t.Errorf("%q: day X is not the Z plus two Meta lines:\nX=%+v\nZ=%+v", cs, x, z)
		}
		if x.KickDrawer || x.Barcode != "" || len(x.Logo) != 0 || len(x.TSEQR) != 0 {
			t.Errorf("%q: drawer/barcode/logo/TSE must be empty", cs)
		}
		raw := print.Render(buildEODDayXDoc(rep, "Shop", cs, eodArticlePrintAll, 0))
		txt := print.RenderText(buildEODDayXDoc(rep, "Shop", cs, eodArticlePrintAll, 0))
		for _, want := range []string{"X REPORT - DAY SO FAR", "Not a day close. Nothing is closed."} {
			if !bytes.Contains(raw, []byte(want)) || !strings.Contains(txt, want) {
				t.Errorf("%q: paper lacks %q", cs, want)
			}
		}
		for _, bad := range []string{"END OF DAY", "Z no.", httpx.T("en", "reports.zpaper.fiscal_device.title"), "Anmerkung", "should not print"} {
			if bad != "" && (bytes.Contains(raw, []byte(bad)) || strings.Contains(txt, bad)) {
				t.Errorf("%q: paper must not contain %q", cs, bad)
			}
		}
	}
}

func TestBuildEODDayXDoc_ControlRunesNeutralised(t *testing.T) {
	kick := string(print.DrawerKickBytes(2)) + string(print.DrawerKickBytes(5))
	rep := dxDocReport()
	rep.GeneratedBy = "Alex" + kick
	for _, cs := range []string{"", "utf8", "ascii", "cp858", "win1250"} {
		out := print.Render(buildEODDayXDoc(rep, "Shop"+kick, cs, eodArticlePrintAll, 0))
		if n := bytes.Count(out, []byte("\x1bp")); n != 0 {
			t.Errorf("%q: drawer-kick bytes reached the printer x%d", cs, n)
		}
	}
}
