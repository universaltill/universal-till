package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// ut-docs#4260: order tracking is cloud-only. Until the cloud tracking page
// exists (#4238) no receipt carries a tracking link — even on a Shop till
// with a LAN address the old QR pointed at. The gate still passes, so the
// sale keeps its (idempotent) token for #4238 to use.
func TestBuildReceiptDoc_NoTrackingLinkUntilCloudPage(t *testing.T) {
	dp := newTrackingPrintDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.TrackingQR) != 0 || doc.TrackingURL != "" || doc.TrackingCaption != "" {
		t.Fatalf("no receipt may carry a tracking link before the cloud page exists, got url=%q caption=%q qr=%d bytes", doc.TrackingURL, doc.TrackingCaption, len(doc.TrackingQR))
	}
	if strings.Contains(print.RenderText(doc), "192.168.1.20") {
		t.Fatal("the printed receipt must never carry the till's LAN address")
	}
	if len(doc.Lines) != 1 {
		t.Fatalf("receipt body missing: %+v", doc.Lines)
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

// ut-docs#4260: until the cloud tracking page exists (#4238) the designer
// preview shows no tracking block, ticked or not, and saving the designer
// leaves the stored choice alone (the box is disabled, so it never posts).
func TestSampleReceiptDoc_NoTrackingQRUntilCloudPage(t *testing.T) {
	doc := sampleReceiptDoc("My Shop", receiptDesign{ShowTrackingQR: true}, "en")
	txt := print.RenderText(doc)
	if strings.Contains(txt, httpx.T("en", "receipt.tracking.caption")) || len(doc.TrackingQR) != 0 || doc.TrackingURL != "" {
		t.Fatalf("designer preview must not show a tracking block yet:\n%s", txt)
	}
}

func TestReceiptDesignerSave_KeepsShowTrackingQR(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	for _, c := range []struct {
		stored string
		form   string
	}{
		{"true", "footer=x"},
		{"false", "show_tracking_qr=on"},
	} {
		if err := dp.Settings.Set(context.Background(), keyReceiptShowTrackingQR, c.stored); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/save", strings.NewReader(c.form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save = %d %q", rec.Code, rec.Body.String())
		}
		if got := mustSetting(t, dp, keyReceiptShowTrackingQR); got != c.stored {
			t.Fatalf("%s = %q after saving %q, want it kept at %q", keyReceiptShowTrackingQR, got, c.form, c.stored)
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

// End to end through the cashier tender: a Shop till mints the sale's token
// but shows no QR until the cloud page exists (ut-docs#4260); a Local till
// shows none and mints none.
func TestTender_OnScreenReceiptHasNoLANTrackingQR(t *testing.T) {
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
				// ut-docs#4260: the gate passes (token minted) but there is
				// no cloud page yet, so no QR and never the till's address.
				if tok == nil || strings.Contains(body, `data-testid="receipt-tracking-qr"`) || strings.Contains(body, "192.168.1.20") {
					t.Fatalf("Shop till: expected a minted token (%v) and no tracking QR:\n%s", tok, body)
				}
				return
			}
			if tok != nil || strings.Contains(body, `data-testid="receipt-tracking-qr"`) {
				t.Fatalf("Local till must neither mint a token (%v) nor show a tracking QR:\n%s", tok, body)
			}
		})
	}
}

// ut-docs#4260: the designer page greys the tracking box out (disabled, so
// it never posts) and says why, until the cloud tracking page exists.
func TestReceiptDesignerPage_TrackingBoxDisabledWithNote(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReceiptDesignerTestDeps(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/receipt-designer", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /receipt-designer = %d", rec.Code)
	}
	body := rec.Body.String()
	i := strings.Index(body, `name="show_tracking_qr"`)
	if i < 0 {
		t.Fatal("designer must still show the tracking box")
	}
	tag := body[i : i+strings.Index(body[i:], ">")]
	if !strings.Contains(tag, "disabled") || !strings.Contains(tag, `aria-describedby="rd-tracking-note"`) {
		t.Fatalf("tracking box must be disabled and point at its note: %s", tag)
	}
	if !strings.Contains(body, `id="rd-tracking-note"`) || !strings.Contains(body, httpx.T("en", "designer.receipt.tracking_qr_needs_cloud")) {
		t.Fatal("designer must explain why the tracking box is disabled")
	}
}
