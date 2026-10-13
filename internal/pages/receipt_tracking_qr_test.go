package pages

import (
	"context"
	"encoding/base64"
	"errors"
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#908: a counter (cashier) sale's receipt carries the customer
// order-tracking QR — printed and on screen — when it is a normal sale, the
// plan allows order tracking (Shop and up, ut-docs#4206) and the receipt
// setting receipt.show_tracking_qr is on. Display-only: never affects the
// sale, and any failure prints the receipt without the QR.

func setOrderTypePromptMode(t *testing.T, mode string) {
	t.Helper()
	httpx.InitOrderTypePromptMode(mode)
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })
}

func saleTrackingToken(t *testing.T, dp *common.Deps, receiptNo string) string {
	t.Helper()
	var tok *string
	if err := dp.Db.QueryRow(`SELECT tracking_token FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	if tok == nil {
		return ""
	}
	return *tok
}

// Default: on, unless the shop switched the dine-in/takeaway prompt off
// (retail/service shops, ut-docs#3632). An explicit stored value wins.
func TestReceiptDesign_ShowTrackingQRDefault(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()

	setOrderTypePromptMode(t, "top")
	if !receiptDesignFromSettings(ctx, dp).ShowTrackingQR {
		t.Fatal("unset receipt.show_tracking_qr must default to on for a shop with the order-type prompt on")
	}
	setOrderTypePromptMode(t, "off")
	if receiptDesignFromSettings(ctx, dp).ShowTrackingQR {
		t.Fatal("unset receipt.show_tracking_qr must default to off when the order type is switched off")
	}
	if err := dp.Settings.Set(ctx, keyReceiptShowTrackingQR, "true"); err != nil {
		t.Fatal(err)
	}
	if !receiptDesignFromSettings(ctx, dp).ShowTrackingQR {
		t.Fatal("an explicit \"true\" must win over the order-type-off default")
	}
	setOrderTypePromptMode(t, "top")
	if err := dp.Settings.Set(ctx, keyReceiptShowTrackingQR, "false"); err != nil {
		t.Fatal(err)
	}
	if receiptDesignFromSettings(ctx, dp).ShowTrackingQR {
		t.Fatal("an explicit \"false\" must win over the default")
	}
}

func TestReceiptTrackingQROffered(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	on := receiptDesign{ShowTrackingQR: true}

	if receiptTrackingQROffered(ctx, dp, "sale", "completed", on) {
		t.Fatal("a Local till (no entitlement) must not offer a receipt tracking QR")
	}
	seedTrackingPlan(t, dp, entitlement.PlanShop)
	if !receiptTrackingQROffered(ctx, dp, "sale", "completed", on) {
		t.Fatal("a Shop till with the setting on must offer the QR on a normal sale")
	}
	if receiptTrackingQROffered(ctx, dp, "return", "completed", on) {
		t.Fatal("a return/refund receipt must never carry a tracking QR")
	}
	if receiptTrackingQROffered(ctx, dp, "sale", "completed", receiptDesign{}) {
		t.Fatal("the receipt setting off must suppress the QR")
	}
	// Review M1: a reprinted voided sale is no order to follow.
	if receiptTrackingQROffered(ctx, dp, "sale", "voided", on) {
		t.Fatal("a voided sale's receipt must never carry a tracking QR")
	}
	// Review M1: on an additional till the status is set on the main till,
	// which never holds this sale's token (the LAN sale journal does not
	// carry it, #4238), so the QR would never leave "untracked".
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	if receiptTrackingQROffered(ctx, dp, "sale", "completed", on) {
		t.Fatal("an additional till (following a main till) must not offer the receipt QR")
	}
}

// newTrackingPrintDeps is a print-test till on the Shop plan with a LAN
// address the receipt QR can point at.
func newTrackingPrintDeps(t *testing.T) *common.Deps {
	t.Helper()
	_, dp := newPrintAPITestDeps(t)
	setOrderTypePromptMode(t, "top")
	seedTrackingPlan(t, dp, entitlement.PlanShop)
	dp.Cfg.ListenAddr = ":8080"
	stubLANIP(t, "192.168.1.20", nil)
	return dp
}

func TestBuildReceiptDoc_TrackingQROnCounterSale(t *testing.T) {
	dp := newTrackingPrintDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	ctx := context.Background()

	doc, err := buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	tok := saleTrackingToken(t, dp, "R001")
	if len(tok) != 32 {
		t.Fatalf("printing the receipt must mint the sale's tracking token, got %q", tok)
	}
	wantURL := "http://192.168.1.20:8080/o/" + tok + "?lang=" + url.QueryEscape(httpx.DefaultLocale())
	if doc.TrackingURL != wantURL {
		t.Fatalf("TrackingURL = %q, want %q", doc.TrackingURL, wantURL)
	}
	if len(doc.TrackingQR) == 0 {
		t.Fatal("expected a tracking QR raster on the printed receipt")
	}
	if doc.TrackingCaption != httpx.T(httpx.DefaultLocale(), "receipt.tracking.caption") || doc.TrackingCaption == "receipt.tracking.caption" {
		t.Fatalf("TrackingCaption = %q, want the translated receipt.tracking.caption", doc.TrackingCaption)
	}

	// A reprint gets the SAME token — never a second link for one order.
	again, err := buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if again.TrackingURL != wantURL || saleTrackingToken(t, dp, "R001") != tok {
		t.Fatalf("reprint changed the tracking link: %q (token %q)", again.TrackingURL, saleTrackingToken(t, dp, "R001"))
	}
}

func TestBuildReceiptDoc_NoTrackingQRWhenGateFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dp *common.Deps)
		sale  string
	}{
		{"local plan", func(t *testing.T, dp *common.Deps) {
			if _, err := dp.Db.Exec(`DELETE FROM settings WHERE key = ?`, entitlement.KeyPlan); err != nil {
				t.Fatal(err)
			}
		}, "sale"},
		{"setting off", func(t *testing.T, dp *common.Deps) {
			if err := dp.Settings.Set(context.Background(), keyReceiptShowTrackingQR, "false"); err != nil {
				t.Fatal(err)
			}
		}, "sale"},
		{"refund", func(*testing.T, *common.Deps) {}, "return"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dp := newTrackingPrintDeps(t)
			c.setup(t, dp)
			seedReceiptSale(t, dp, "sale1", "R001", c.sale, "", 120, 0, 0)
			doc, err := buildReceiptDoc(context.Background(), dp, "R001")
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.TrackingQR) != 0 || doc.TrackingURL != "" || doc.TrackingCaption != "" {
				t.Fatalf("expected no tracking block, got url=%q caption=%q qr=%d bytes", doc.TrackingURL, doc.TrackingCaption, len(doc.TrackingQR))
			}
			if tok := saleTrackingToken(t, dp, "R001"); tok != "" {
				t.Fatalf("no token may be minted when the gate fails, got %q", tok)
			}
		})
	}
}

// No LAN address: the receipt still prints, just without the QR.
func TestBuildReceiptDoc_NoLANAddressPrintsWithoutQR(t *testing.T) {
	dp := newTrackingPrintDeps(t)
	stubLANIP(t, "", errors.New("no lan"))
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatalf("a missing LAN address must never fail the receipt: %v", err)
	}
	if len(doc.TrackingQR) != 0 || doc.TrackingURL != "" {
		t.Fatalf("expected no tracking block without a LAN address, got %q", doc.TrackingURL)
	}
	if len(doc.Lines) != 1 {
		t.Fatalf("receipt body missing: %+v", doc.Lines)
	}
}

// The caption falls back to English when the printer charset cannot encode
// the shop locale's translation (kitchenTicketText's rule).
func TestReceiptTrackingCaption_FallsBackToEnglishOnRestrictedCharset(t *testing.T) {
	en := httpx.T("en", "receipt.tracking.caption")
	if got := kitchenTicketText("fa", "ascii", "receipt.tracking.caption"); got != en {
		t.Fatalf("fa caption on an ascii printer = %q, want the English %q", got, en)
	}
	if got := kitchenTicketText("fa", "utf8", "receipt.tracking.caption"); got == en || got == "receipt.tracking.caption" {
		t.Fatalf("fa caption on a utf8 printer = %q, want the Persian translation", got)
	}
}

func TestDesignFromForm_ShowTrackingQR(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("show_tracking_qr=on"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	if !designFromForm(r).ShowTrackingQR {
		t.Fatal("show_tracking_qr=on must set ShowTrackingQR")
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	if designFromForm(r).ShowTrackingQR {
		t.Fatal("an unticked box must clear ShowTrackingQR")
	}
}

// The designer's sample ticket shows a fixed sample link (deterministic for
// docs-shots) under the caption when the box is ticked, and nothing when not.
func TestSampleReceiptDoc_TrackingQR(t *testing.T) {
	doc := sampleReceiptDoc("My Shop", receiptDesign{ShowTrackingQR: true}, "en")
	txt := print.RenderText(doc)
	if !strings.Contains(txt, httpx.T("en", "receipt.tracking.caption")) || !strings.Contains(txt, sampleTrackingURL) {
		t.Fatalf("designer preview must show the sample tracking block:\n%s", txt)
	}
	if len(doc.TrackingQR) == 0 {
		t.Fatal("the designer's test print must carry a sample QR raster")
	}
	txt = print.RenderText(sampleReceiptDoc("My Shop", receiptDesign{}, "en"))
	if strings.Contains(txt, sampleTrackingURL) {
		t.Fatal("unticked: no tracking block in the preview")
	}
}

func TestReceiptDesignerSave_WritesShowTrackingQR(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	for _, c := range []struct {
		form string
		want string
	}{
		{"show_tracking_qr=on", "true"},
		{"footer=x", "false"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/save", strings.NewReader(c.form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save = %d %q", rec.Code, rec.Body.String())
		}
		if got := mustSetting(t, dp, keyReceiptShowTrackingQR); got != c.want {
			t.Fatalf("%s = %q, want %q", keyReceiptShowTrackingQR, got, c.want)
		}
	}
}

func TestRenderReceipt_TrackingQRBlock(t *testing.T) {
	chdirRoot(t)
	funcs := httpx.FuncsFor("en")
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 100}}
	view := &receiptTrackingView{QRDataURI: "data:image/png;base64,AAAA", URL: "http://192.168.1.20:8080/o/abc?lang=en"}
	html, err := renderReceipt(funcs, "123", lines, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{}, "", nil, "", nil, view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-testid="receipt-tracking-qr"`) || !strings.Contains(html, `src="data:image/png;base64,AAAA"`) {
		t.Fatalf("expected the tracking QR block:\n%s", html)
	}
	if !strings.Contains(html, httpx.T("en", "receipt.tracking.caption")) {
		t.Fatal("expected the tracking caption")
	}
	html, err = renderReceipt(funcs, "123", lines, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{}, "", nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, `data-testid="receipt-tracking-qr"`) {
		t.Fatal("no tracking view = no block")
	}
}

// End to end through the cashier tender: a Shop till's on-screen receipt
// shows the QR for the sale's token; a Local till shows none and mints none.
func TestTender_OnScreenReceiptTrackingQR(t *testing.T) {
	for _, shop := range []bool{true, false} {
		t.Run(map[bool]string{true: "shop", false: "local"}[shop], func(t *testing.T) {
			chdirRoot(t)
			db := openPagesTestDB(t)
			defer db.Close()
			seedForPages(t, db)
			setOrderTypePromptMode(t, "top")
			resolver := stubResolver{"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 250, ItemID: "itm1", TaxRateBP: 2000}}
			engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, resolver)
			if _, err := engine.Scan("ABC"); err != nil {
				t.Fatal(err)
			}
			setStore := settings.NewStore(db)
			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
			pm, err := plugins.Init(t.Context(), cfg, db)
			if err != nil {
				t.Fatal(err)
			}
			dp := &common.Deps{Cfg: cfg, Db: db, State: common.LoadState(t.Context(), setStore, cfg), Engine: engine, Pm: pm, Settings: setStore}
			defer dp.WaitForAsyncWork()
			if shop {
				seedTrackingPlan(t, dp, entitlement.PlanShop)
			}
			mux := http.NewServeMux()
			registerPOSAPI(mux, dp)

			req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":300}],"offline":true}`))
			req.Host = "192.168.1.20:8080"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("tender = %d %s", rec.Code, rec.Body.String())
			}
			var receiptNo string
			if err := db.QueryRow(`SELECT receipt_no FROM sales LIMIT 1`).Scan(&receiptNo); err != nil {
				t.Fatal(err)
			}
			var tok *string
			if err := db.QueryRow(`SELECT tracking_token FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&tok); err != nil {
				t.Fatal(err)
			}
			body := rec.Body.String()
			if shop {
				if tok == nil || !strings.Contains(body, `data-testid="receipt-tracking-qr"`) {
					t.Fatalf("Shop till: expected a tracking QR block and a minted token (%v):\n%s", tok, body)
				}
				// The embedded QR encodes exactly this sale's link.
				png, err := qrcode.Encode("http://192.168.1.20:8080/o/"+*tok+"?lang=en", qrcode.Medium, 200)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(stdhtml.UnescapeString(body), base64.StdEncoding.EncodeToString(png)) {
					t.Fatalf("on-screen QR does not encode /o/%s:\n%s", *tok, body)
				}
				return
			}
			if tok != nil || strings.Contains(body, `data-testid="receipt-tracking-qr"`) {
				t.Fatalf("Local till must neither mint a token (%v) nor show a tracking QR:\n%s", tok, body)
			}
		})
	}
}
