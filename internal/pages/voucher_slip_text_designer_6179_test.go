package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#6179: the designer's "Text on gift voucher slips" field and its
// three write doors.

// postFormAuthOff posts as the anonymous operator the UT_AUTH=off tests use,
// so the audit row's actor needs no seeded user.
func postFormAuthOff(mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func slipTextForm(text string) url.Values {
	f := url.Values{"header1": {"Corner Shop"}, "footer": {"Thanks!"}}
	f.Set("voucher_slip_text", text)
	return f
}

func TestReceiptDesigner_SaveVoucherSlipText(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	rec := postFormAuthOff(mux, "/api/receipt-designer/save", slipTextForm("Valid in store.\r\nNo cash refund."))
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "Valid in store.\nNo cash refund." {
		t.Fatalf("stored %q", got)
	}
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'receipt_design_saved'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
	if got := receiptDesignFromSettings(t.Context(), dp).VoucherSlipText; got != "Valid in store.\nNo cash refund." {
		t.Fatalf("design VoucherSlipText = %q", got)
	}
	// An empty field clears it.
	if rec := postFormAuthOff(mux, "/api/receipt-designer/save", slipTextForm("  \n ")); rec.Code != http.StatusOK {
		t.Fatalf("clear = %d", rec.Code)
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "" {
		t.Fatalf("cleared value = %q", got)
	}
}

func TestReceiptDesigner_GETShowsSlipTextAfterFooter(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	if err := dp.Settings.Set(t.Context(), keyReceiptVoucherSlipText, "Valid in store.\nNo cash refund."); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/receipt-designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	foot := strings.Index(body, `name="footer"`)
	area := strings.Index(body, `name="voucher_slip_text"`)
	if foot < 0 || area < foot {
		t.Fatalf("textarea must follow the footer field (footer %d, textarea %d)", foot, area)
	}
	for _, want := range []string{`data-osk-enter="newline"`, `dir="auto"`, `rows="4"`, "Valid in store.\nNo cash refund.", "Text on gift voucher slips", "Up to 4 lines and 160 characters."} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	tag := body[area:]
	tag = tag[:strings.Index(tag, "</textarea>")]
	for _, bad := range []string{"maxlength", "autofocus", "placeholder"} {
		if strings.Contains(tag, bad) {
			t.Errorf("textarea must not carry %s", bad)
		}
	}
	// The sample slip shows in the preview once text is saved.
	if !strings.Contains(body, "No cash refund.") || !strings.Contains(body, "GV-SAMPLE") {
		t.Error("preview lacks the sample slip")
	}
}

func TestReceiptDesigner_SaveVoucherSlipTextOverLimit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	if err := dp.Settings.Set(t.Context(), keyReceiptVoucherSlipText, "old"); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"161 runes": strings.Repeat("ä", 161), "5 lines": "a\nb\nc\nd\ne"} {
		rec := postFormAuthOff(mux, "/api/receipt-designer/save", slipTextForm(text))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: code %d body %s", name, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content type %q", name, ct)
		}
		want := html.EscapeString(voucherSlipTextTooLongMsg(httpx.DefaultLocale()))
		if !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), "Not saved") {
			t.Errorf("%s: body %q lacks %q", name, rec.Body.String(), want)
		}
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "old" {
		t.Errorf("slip text changed to %q", got)
	}
	if got := mustSetting(t, dp, keyReceiptHeader1); got != "" {
		t.Errorf("header1 written (%q) although the save was refused", got)
	}
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'receipt_design_saved'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
}

func TestReceiptDesigner_SaveVoucherSlipText_AdditionalTill(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newRestWriteThroughReplica(t, main.srv.URL)
	rec := postForm(mux, "/api/receipt-designer/save", slipTextForm("Valid in store.\r\nNo cash refund."), &mgrUser)
	if rec.Code != http.StatusOK || main.calls.Load() != 1 {
		t.Fatalf("save = %d calls %d %q", rec.Code, main.calls.Load(), rec.Body.String())
	}
	if got := mustSetting(t, main.dp, keyReceiptVoucherSlipText); got != "Valid in store.\nNo cash refund." {
		t.Errorf("main till stored %q", got)
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "Valid in store.\nNo cash refund." {
		t.Errorf("replica mirror %q", got)
	}
	assertSettingSyncAudit(t, main.dp, "m1", keyReceiptVoucherSlipText, "No cash refund.", "Till 2")

	// Over the limit: refused locally, the main till is never contacted.
	rec = postForm(mux, "/api/receipt-designer/save", slipTextForm(strings.Repeat("x", 161)), &mgrUser)
	if rec.Code != http.StatusUnprocessableEntity || main.calls.Load() != 1 {
		t.Fatalf("over-limit = %d, main calls %d", rec.Code, main.calls.Load())
	}
}

func TestSyncSettingsApply_VoucherSlipTextValidated(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(keyReceiptVoucherSlipText, strings.Repeat("x", 161)), ActorID: "m-1"})
	wantSyncSettingsError(t, postSyncSettingsApply(m.mux, body, syncSettingsBearer), http.StatusUnprocessableEntity, "invalid_value")
	if got := mustSetting(t, m.dp, keyReceiptVoucherSlipText); got != "" {
		t.Fatalf("refused value stored: %q", got)
	}
	body = settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(keyReceiptVoucherSlipText, " a\r\nb "), ActorID: "m-1"})
	rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, m.dp, keyReceiptVoucherSlipText); got != "a\nb" {
		t.Errorf("stored %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"value":"a\nb"`) {
		t.Errorf("answer not normalised: %s", rec.Body.String())
	}
}

func TestSettingsUpsert_VoucherSlipTextValidated(t *testing.T) {
	mux, _, dp := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {keyReceiptVoucherSlipText}, "value": {strings.Repeat("x", 161)}}, &mgrUser)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-limit = %d %s", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "" {
		t.Fatalf("refused value stored: %q", got)
	}
	rec = postForm(mux, "/api/settings/upsert", url.Values{"key": {keyReceiptVoucherSlipText}, "value": {" a\r\nb "}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid = %d %s", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "a\nb" {
		t.Errorf("stored %q", got)
	}
}

func TestDesignerPreview_SlipOnlyWithText(t *testing.T) {
	initPagesI18n(t)
	base := print.RenderText(sampleReceiptDoc("Shop", receiptDesign{}, "en"))
	if got := designerPreview("Shop", receiptDesign{}, "en"); got != base {
		t.Fatalf("empty text must leave the preview unchanged:\n%s", got)
	}
	got := designerPreview("Shop", receiptDesign{Header: []string{"H1"}, VoucherSlipText: "Valid in store."}, "en")
	if !strings.HasPrefix(got, print.RenderText(sampleReceiptDoc("Shop", receiptDesign{Header: []string{"H1"}}, "en"))) {
		t.Error("receipt must come first")
	}
	rest := got[len(print.RenderText(sampleReceiptDoc("Shop", receiptDesign{Header: []string{"H1"}}, "en"))):]
	for _, want := range []string{"Gift voucher", "GV-SAMPLE", "Issued ", "Valid in store."} {
		if !strings.Contains(rest, want) {
			t.Errorf("sample slip lacks %q:\n%s", want, rest)
		}
	}
}

func TestReceiptDesignerPreview_PostShowsSlipForUnsavedText(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	rec := postFormAuthOff(mux, "/api/receipt-designer/preview", slipTextForm("a\nb\nc\nd\ne")) // over the limit: lenient
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "GV-SAMPLE") || !strings.Contains(rec.Body.String(), "\ne") && !strings.Contains(rec.Body.String(), "e\n") {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, keyReceiptVoucherSlipText); got != "" {
		t.Fatalf("preview persisted %q", got)
	}
}
