package pluginview

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#3874 (ADR-0121 build 8d): the qr and wizard_step components and
// table paging.

func TestDecodeViewAnswer_QRWizardPaging_3874(t *testing.T) {
	raw := doc(`
		{"type":"wizard_step","steps":[{"literal":"Connect"},{"key":"plugin.demo.hello"},{"literal":"Done"}],"current":2},
		{"type":"qr","data":"BCD\n002\n1\nSCT\n\nCafé Nord\nDE89370400440532013000\nEUR12.50","label":{"literal":"Pay by bank app"}},
		{"type":"qr","data":"https://example.com/pair?c=123"},
		{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"}],"rows":[["Tea"]],"paging":{"page":2,"pages":5}},
		{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"}],"rows":[["Tea"]],"paging":{"page":1,"has_next":true,"param":"log_page"}}`)
	d, err := DecodeViewAnswer([]byte(raw), testCtx)
	if err != nil {
		t.Fatalf("valid document refused: %v", err)
	}
	if len(d.Components) != 5 {
		t.Fatalf("components = %d, want 5", len(d.Components))
	}
}

func TestDecodeViewAnswer_QRWizardPagingRefusals_3874(t *testing.T) {
	steps13 := strings.TrimSuffix(strings.Repeat(`{"literal":"s"},`, MaxWizardSteps+1), ",")
	table := func(paging string) string {
		return `{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":` + paging + `}`
	}
	cases := []struct{ name, raw, want string }{
		{"qr empty data", doc(`{"type":"qr","data":""}`), "data"},
		{"qr no data", doc(`{"type":"qr"}`), "data"},
		{"qr data too long", doc(`{"type":"qr","data":"` + strings.Repeat("a", MaxQRDataBytes+1) + `"}`), "data"},
		{"qr data with NUL", doc(`{"type":"qr","data":"a\u0000b"}`), "data"},
		{"qr unknown field (no plugin image)", doc(`{"type":"qr","data":"x","src":"data:image/png;base64,AAAA"}`), "unknown field"},
		{"qr foreign label key", doc(`{"type":"qr","data":"x","label":{"key":"nav.home"}}`), "own locale bundle"},
		{"wizard one step", doc(`{"type":"wizard_step","steps":[{"literal":"a"}],"current":1}`), "steps"},
		{"wizard too many steps", doc(`{"type":"wizard_step","steps":[` + steps13 + `],"current":1}`), "steps"},
		{"wizard current 0", doc(`{"type":"wizard_step","steps":[{"literal":"a"},{"literal":"b"}],"current":0}`), "current"},
		{"wizard current past end", doc(`{"type":"wizard_step","steps":[{"literal":"a"},{"literal":"b"}],"current":3}`), "current"},
		{"wizard foreign key", doc(`{"type":"wizard_step","steps":[{"literal":"a"},{"key":"plugin.other.x"}],"current":1}`), "own locale bundle"},
		{"two wizard steps", doc(`{"type":"wizard_step","steps":[{"literal":"a"},{"literal":"b"}],"current":1},{"type":"wizard_step","steps":[{"literal":"a"},{"literal":"b"}],"current":2}`), "one wizard_step"},
		{"paging page 0", doc(table(`{"page":0,"has_next":true}`)), "page"},
		{"paging page too big", doc(table(`{"page":1000001,"has_next":false}`)), "page"},
		{"paging pages below page", doc(table(`{"page":3,"pages":2}`)), "pages"},
		{"paging pages and has_next", doc(table(`{"page":1,"pages":2,"has_next":true}`)), "pages or has_next"},
		{"paging neither pages nor has_next", doc(table(`{"page":1}`)), "pages or has_next"},
		{"paging reserved param", doc(table(`{"page":1,"pages":2,"param":"_job"}`)), "param"},
		{"paging bad param", doc(table(`{"page":1,"pages":2,"param":"Page"}`)), "param"},
		{"paging unknown field", doc(table(`{"page":1,"pages":2,"url":"/x"}`)), "unknown field"},
		{"two tables, one param", doc(table(`{"page":1,"pages":2}`) + "," + table(`{"page":1,"has_next":false}`)), "used by two tables"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeViewAnswer([]byte(tc.raw), testCtx)
			if err == nil {
				t.Fatalf("accepted, want refusal containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A seam draws only text, notice and suggestions (ut-docs#3873).
func TestDecodeViewAnswer_QRNotInSeam_3874(t *testing.T) {
	c := testCtx
	c.Seam = SeamSellIdentify
	for _, comp := range []string{
		`{"type":"qr","data":"x"}`,
		`{"type":"wizard_step","steps":[{"literal":"a"},{"literal":"b"}],"current":1}`,
	} {
		if _, err := DecodeViewAnswer([]byte(doc(comp)), c); err == nil || !strings.Contains(err.Error(), "seam") {
			t.Errorf("%s in a seam: err = %v, want a seam refusal", comp, err)
		}
	}
}

// loadI18n loads core's locales, so core keys (plugin.view.*) resolve.
func loadI18n(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("..", "..", "web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

func prepared(t *testing.T, components, locale string) View {
	t.Helper()
	loadI18n(t)
	d, err := DecodeViewAnswer([]byte(doc(components)), testCtx)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return d.Prepare(locale)
}

func TestPrepare_QR_3874(t *testing.T) {
	v := prepared(t, `{"type":"qr","data":"https://example.com/x","label":{"literal":"Scan <me>"}},{"type":"qr","data":"y"}`, "en")
	q := v.Components[0]
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(string(q.QR), prefix) {
		t.Fatalf("QR = %.40q, want a PNG data URI", q.QR)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(q.QR), prefix))
	if err != nil || len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Fatalf("QR is not a base64 PNG (err %v)", err)
	}
	// The label is the caption; the image's alt is always core's own
	// "QR code", so a screen reader says what it is, then the caption.
	if q.Label != "Scan <me>" || q.Alt != httpx.T("en", "plugin.view.qr_alt") || q.Alt == "plugin.view.qr_alt" {
		t.Errorf("label/alt = %q/%q", q.Label, q.Alt)
	}
	if q2 := v.Components[1]; q2.Label != "" || q2.Alt != q.Alt {
		t.Errorf("unlabelled qr: label %q alt %q", q2.Label, q2.Alt)
	}
}

func TestPrepare_WizardStep_3874(t *testing.T) {
	v := prepared(t, `{"type":"wizard_step","steps":[{"literal":"A"},{"literal":"B"},{"literal":"C"}],"current":2}`, "en")
	w := v.Components[0]
	want := []ViewStep{{Label: "A", State: "done"}, {Label: "B", State: "current"}, {Label: "C", State: "upcoming"}}
	if len(w.Steps) != 3 {
		t.Fatalf("steps = %v", w.Steps)
	}
	for i := range want {
		if w.Steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, w.Steps[i], want[i])
		}
	}
	if w.Text == "" || !strings.Contains(w.Text, "2") || !strings.Contains(w.Text, "3") {
		t.Errorf("step text = %q, want n of m", w.Text)
	}
	// Locale digits (fa): never Latin "2 of 3" in a Persian sentence.
	fa := prepared(t, `{"type":"wizard_step","steps":[{"literal":"A"},{"literal":"B"},{"literal":"C"}],"current":2}`, "fa")
	if strings.ContainsAny(fa.Components[0].Text, "23") {
		t.Errorf("fa step text %q uses Latin digits", fa.Components[0].Text)
	}
}

func TestPrepare_TablePaging_3874(t *testing.T) {
	v := prepared(t, `
		{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":2,"pages":5}},
		{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":1,"has_next":true,"param":"log_page"}},
		{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":3,"has_next":false,"param":"p3"}},
		{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]]}`, "en")
	// Before LinkPaging (a slot panel): the indicator, no links.
	p := v.Components[0].Paging
	if p == nil || p.Text == "" || p.PrevURL != "" || p.NextURL != "" {
		t.Fatalf("unlinked paging = %+v", p)
	}
	if v.Components[3].Paging != nil {
		t.Error("a table without paging got paging")
	}

	v.LinkPaging("/plugin/demo", map[string]string{"q": "tea & milk", "page": "2", "log_page": "1", "p3": "3"})
	cases := []struct {
		i          int
		prev, next string
	}{
		{0, "/plugin/demo?log_page=1&p3=3&page=1&q=tea+%26+milk", "/plugin/demo?log_page=1&p3=3&page=3&q=tea+%26+milk"},
		{1, "", "/plugin/demo?log_page=2&p3=3&page=2&q=tea+%26+milk"},
		{2, "/plugin/demo?log_page=1&p3=2&page=2&q=tea+%26+milk", ""},
	}
	for _, c := range cases {
		p := v.Components[c.i].Paging
		if p.PrevURL != c.prev || p.NextURL != c.next {
			t.Errorf("table %d: prev %q next %q, want %q %q", c.i, p.PrevURL, p.NextURL, c.prev, c.next)
		}
	}
	// Last page of a known total: no Next.
	last := prepared(t, `{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":5,"pages":5}}`, "en")
	last.LinkPaging("/plugin/demo", nil)
	if lp := last.Components[0].Paging; lp.NextURL != "" || lp.PrevURL != "/plugin/demo?page=4" {
		t.Errorf("last page: prev %q next %q", lp.PrevURL, lp.NextURL)
	}
	if !strings.Contains(last.Components[0].Paging.Text, "5") {
		t.Errorf("indicator %q, want page n of m", last.Components[0].Paging.Text)
	}
}

// Review (ut-docs#3874): QR encoding is bounded per document, and done
// once by the validator at 4 px per module.
func TestQR_CapAndEncodedOnce_3874(t *testing.T) {
	qr := `{"type":"qr","data":"` + strings.Repeat("a", MaxQRDataBytes) + `"},`
	over := strings.TrimSuffix(strings.Repeat(qr, MaxQRCodes+1), ",")
	if _, err := DecodeViewAnswer([]byte(doc(over)), testCtx); err == nil || !strings.Contains(err.Error(), "qr components") {
		t.Fatalf("%d qr components: err = %v, want the cap", MaxQRCodes+1, err)
	}
	v := prepared(t, strings.TrimSuffix(strings.Repeat(qr, MaxQRCodes), ","), "en")
	// A 1024-byte code is version 32 (145 modules + 8 quiet) at 4 px each.
	if got := v.Components[0].QRSize; got < 4*109 || got%4 != 0 {
		t.Errorf("QR size = %d px, want 4 px per module", got)
	}
}

// Review (ut-docs#3874): a paging link never carries a reserved "_" key
// and keeps its own param within the forwarded-params cap.
func TestLinkPaging_ParamsCapAndReserved_3874(t *testing.T) {
	v := prepared(t, `{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":1,"has_next":true}}`, "en")
	params := map[string]string{"_job": "abc"}
	for i := 0; i < MaxParams; i++ {
		params["a"+itoa(i+10)] = "v"
	}
	v.LinkPaging("/plugin/demo", params)
	next := v.Components[0].Paging.NextURL
	if strings.Contains(next, "_job") {
		t.Errorf("next %q carries _job", next)
	}
	if n := strings.Count(next, "="); n != MaxParams || !strings.Contains(next, "page=2") {
		t.Errorf("next %q has %d params, want %d including page=2", next, n, MaxParams)
	}
}
