package pages

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"reflect"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// --- ut-docs#2880: the generic `receipt` object on an approved answer ------
//
// fiscal-sign-ask.md 1.10.0: an "approved" answer may carry
// {"receipt":{"qr_payload":…,"lines":[…]}} — country-neutral; core stores it
// per sale and renders the QR from the stored payload, never re-deriving it.

// fiskalyStyleQR is the shape fiskaly SIGN DE's qr_code_data takes (the
// DSFinV-K/BSI TR-03153 "V0;…" receipt QR) — what ut-plugin-tax-de passes
// through verbatim. Core must treat it as opaque.
const fiskalyStyleQR = "V0;ut-till-1;Kassenbeleg-V1;Beleg^0.00_1.19_0.00_0.00_0.00^1.19:Bar;7;12;2026-09-26T10:00:00.000Z;2026-09-26T10:00:01.000Z;ecdsa-plain-SHA384;unixTime;MEUCIQDsig==;BPubKey=="

func askWithResponse(t *testing.T, response string) fiscalSignResult {
	t.Helper()
	_, dp := newFiscalSignDeps(t)
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-receipt", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(response), nil
	})
	in := pos.SaleInput{
		Currency: "EUR",
		Lines:    []pos.SaleLineInput{{Name: "Thing", Qty: 1, UnitPrice: money.FromMinor(100), TaxRateBasisPoints: 2000}},
	}
	return dispatchFiscalSignAsk(context.Background(), dp, &in)
}

func TestFiscalSignAsk_ReceiptObjectParsedWithinBounds(t *testing.T) {
	tenLines := make([]string, fiscalReceiptMaxLines)
	for i := range tenLines {
		tenLines[i] = strings.Repeat("x", fiscalReceiptLineMaxRunes)
	}
	tenLinesJSON, _ := json.Marshal(tenLines)
	maxQR := strings.Repeat("Q", fiscalReceiptQRMaxBytes)

	cases := []struct {
		name     string
		response string
		want     *fiscalReceiptEvidence
	}{
		{
			name:     "payload and lines",
			response: `{"status":"approved","receipt":{"qr_payload":"` + fiskalyStyleQR + `","lines":["a","b"]}}`,
			want:     &fiscalReceiptEvidence{QRPayload: fiskalyStyleQR, Lines: []string{"a", "b"}},
		},
		{
			name:     "payload only",
			response: `{"status":"approved","receipt":{"qr_payload":"` + fiskalyStyleQR + `"}}`,
			want:     &fiscalReceiptEvidence{QRPayload: fiskalyStyleQR},
		},
		{
			name:     "exactly at every bound",
			response: `{"status":"approved","receipt":{"qr_payload":"` + maxQR + `","lines":` + string(tenLinesJSON) + `}}`,
			want:     &fiscalReceiptEvidence{QRPayload: maxQR, Lines: tenLines},
		},
		{name: "bare approved", response: `{"status":"approved"}`},
		{name: "empty receipt object", response: `{"status":"approved","receipt":{}}`},
		{name: "oversize payload dropped", response: `{"status":"approved","receipt":{"qr_payload":"` + maxQR + `Q"}}`},
		{name: "too many lines dropped", response: `{"status":"approved","receipt":{"qr_payload":"x","lines":` + string(tenLinesJSON[:len(tenLinesJSON)-1]) + `,"eleven"]}}`},
		{name: "overlong line dropped", response: `{"status":"approved","receipt":{"qr_payload":"x","lines":["` + strings.Repeat("y", fiscalReceiptLineMaxRunes+1) + `"]}}`},
		{name: "control character in a line dropped", response: `{"status":"approved","receipt":{"qr_payload":"x","lines":["a\nb"]}}`},
		{name: "control character in the payload dropped", response: `{"status":"approved","receipt":{"qr_payload":"a\u001bb"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := askWithResponse(t, tc.response)
			// The receipt object never changes the outcome — an oversize one
			// is dropped, the sale is still a clean approval.
			if res.Outcome != fiscalSignApproved {
				t.Fatalf("expected fiscalSignApproved, got %v (%s)", res.Outcome, res.Reason)
			}
			if tc.want == nil {
				if res.Receipt != nil {
					t.Fatalf("expected no receipt evidence, got %+v", res.Receipt)
				}
				return
			}
			if res.Receipt == nil || !reflect.DeepEqual(*res.Receipt, *tc.want) {
				t.Fatalf("receipt mismatch:\n got %+v\nwant %+v", res.Receipt, tc.want)
			}
		})
	}
}

// An out-of-bounds receipt object is dropped AND logged — never silently.
func TestFiscalSignAsk_OversizeReceiptIsLogged(t *testing.T) {
	logging.ResetRecent()
	res := askWithResponse(t, `{"status":"approved","receipt":{"qr_payload":"`+strings.Repeat("Q", fiscalReceiptQRMaxBytes+1)+`"}}`)
	if res.Receipt != nil {
		t.Fatalf("oversize receipt must be dropped, got %+v", res.Receipt)
	}
	found := false
	for _, p := range logging.Recent() {
		if p.Level == "WARN" && strings.Contains(p.Msg, "receipt object") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a WARN naming the dropped receipt object; recent: %+v", logging.Recent())
	}
}

// qrDataURIFor is the exact data: URI the HTML receipt must embed for a
// payload — go-qrcode is deterministic, so comparing against it asserts
// the payload that reached the encoder (no QR decoder is in deps).
func qrDataURIFor(t *testing.T, payload string) string {
	t.Helper()
	png, err := qrcode.Encode(payload, qrcode.Medium, 140)
	if err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// unescapedHTML undoes html/template's attribute escaping (it writes '+' in
// a data: URI as &#43;, which the browser decodes back) so a rendered src can
// be compared byte-for-byte with qrDataURIFor.
func unescapedHTML(s string) string { return html.UnescapeString(s) }

// End to end through the tender: the receipt object is persisted keyed on
// the real sale id, and the rendered receipt carries the QR of the stored
// payload plus the lines.
func TestFiscalSignAsk_ReceiptEvidencePersistsAndRenders(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-receipt", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"approved","receipt":{"qr_payload":"` + fiskalyStyleQR + `","lines":["Signer line one","Signer <line> two"]}}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	ev, ok, err := data.NewPOSRepo(dp.Db).GetFiscalReceiptEvidence(context.Background(), saleID)
	if err != nil || !ok {
		t.Fatalf("expected persisted receipt evidence for sale %s, ok=%v err=%v", saleID, ok, err)
	}
	if ev.QRPayload != fiskalyStyleQR || !reflect.DeepEqual(ev.Lines, []string{"Signer line one", "Signer <line> two"}) {
		t.Fatalf("persisted evidence mismatch: %+v", ev)
	}
	body := rec.Body.String()
	if !strings.Contains(unescapedHTML(body), `src="`+qrDataURIFor(t, fiskalyStyleQR)+`"`) {
		t.Fatalf("receipt must embed the QR of the stored payload, got: %s", body)
	}
	// Plugin text is escaped, never raw HTML.
	for _, want := range []string{"Signer line one", "Signer &lt;line&gt; two"} {
		if !strings.Contains(body, want) {
			t.Fatalf("receipt must render signer line %q, got: %s", want, body)
		}
	}
}

// A signer that returns TSE evidence but no receipt object gets NO QR —
// core no longer invents a payload (buildTSEQRPayload is gone).
func TestFiscalSignAsk_TSEWithoutReceiptRendersNoQR(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-tse-only", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"approved","tse":{"serial_number":"TSE-TEST-SERIAL-1","signature":"TESTSIGBASE64=="}}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "TSE-TEST-SERIAL-1") {
		t.Fatalf("TSE text lines must still render: %s", body)
	}
	if strings.Contains(body, "data:image/png;base64,") || strings.Contains(body, "UT-TSE-V0") {
		t.Fatalf("no receipt evidence must mean no QR at all, got: %s", body)
	}
}
