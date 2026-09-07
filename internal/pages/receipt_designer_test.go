package pages

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestDesignFromForm(t *testing.T) {
	form := url.Values{
		"header1":      {"Task Runner"},
		"header2":      {""},
		"header3":      {"123 High Street"},
		"footer":       {" Thanks for visiting! "},
		"show_sku":     {"on"},
		"show_tax":     {"on"},
		"show_barcode": {"on"},
		"show_logo":    {"on"},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/preview", nil)
	r.Form = form
	rd := designFromForm(r)

	if len(rd.Header) != 2 || rd.Header[0] != "Task Runner" || rd.Header[1] != "123 High Street" {
		t.Fatalf("expected blank header2 skipped, non-blank headers kept in order, got %+v", rd.Header)
	}
	if rd.Footer != "Thanks for visiting!" {
		t.Fatalf("expected footer trimmed, got %q", rd.Footer)
	}
	if !rd.ShowSKU || !rd.ShowTax || !rd.ShowBarcode || !rd.ShowLogo {
		t.Fatalf("expected all checkbox flags true, got %+v", rd)
	}
}

func TestDesignFromForm_AbsentCheckboxesDefaultFalse(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/preview", nil)
	r.Form = url.Values{"header1": {"X"}}
	rd := designFromForm(r)
	if rd.ShowSKU || rd.ShowTax || rd.ShowBarcode || rd.ShowLogo {
		t.Fatalf("expected all checkbox flags false when absent from the form, got %+v", rd)
	}
}

func newReceiptDesignerTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	isolatePluginsDir(t) // repoints paths.DataDir() at a throwaway temp dir
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/settings", Label: "Settings"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerReceiptDesigner(mux, dp)
	return mux, dp
}

func TestReceiptDesignerPage_GET_RequiresManager(t *testing.T) {
	mux, _ := newReceiptDesignerTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/receipt-designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a 303 redirect without a manager session, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/settings" {
		t.Fatalf("expected redirect to /settings, got %q", loc)
	}
}

func TestReceiptDesignerPage_GET_RendersSavedDesign(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, keyReceiptHeader1, "Task Runner Cafe"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, keyReceiptShowTax, "true"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/receipt-designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /receipt-designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Task Runner Cafe") {
		t.Fatalf("expected the saved header rendered in the page, got %q", body)
	}
	if !strings.Contains(body, "Tax") {
		t.Fatalf("expected the tax line in the sample preview since show_tax=true, got %q", body)
	}
}

func TestReceiptDesignerPreview_ReflectsUnsavedFormWithoutPersisting(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	ctx := context.Background()

	form := "header1=Live+Preview+Header&footer=Live+Footer"
	req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/preview", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Live Preview Header") {
		t.Fatalf("expected the unsaved form value reflected in the preview text, got %q", rec.Body.String())
	}
	if v, ok, _ := dp.Settings.Get(ctx, keyReceiptHeader1); ok && v != "" {
		t.Fatalf("preview must NOT persist -- expected no saved header1 setting, got %q", v)
	}
}

func TestReceiptDesignerSave_PersistsSettingsAndWritesAudit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	ctx := context.Background()

	form := "header1=Saved+Header&footer=Saved+Footer&show_sku=on&show_barcode=on"
	req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/save", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: code %d body %s", rec.Code, rec.Body.String())
	}

	if v, _, _ := dp.Settings.Get(ctx, keyReceiptHeader1); v != "Saved Header" {
		t.Fatalf("expected header1 persisted, got %q", v)
	}
	if v, _, _ := dp.Settings.Get(ctx, keyReceiptShowSKU); v != "true" {
		t.Fatalf("expected show_sku persisted as true, got %q", v)
	}
	if v, _, _ := dp.Settings.Get(ctx, keyReceiptShowTax); v != "false" {
		t.Fatalf("expected show_tax persisted as false (absent from form), got %q", v)
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'receipt_design_saved'`).Scan(&auditCount); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one receipt_design_saved audit row, got %d", auditCount)
	}
}

func newValidPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

func logoUploadRequest(t *testing.T, fields map[string]string, fileBytes []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fileBytes != nil {
		fw, err := mw.CreateFormFile("logo", "logo.png")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatalf("write logo bytes: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/logo", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestReceiptDesignerLogo_RejectsInvalidImage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReceiptDesignerTestDeps(t)
	req := logoUploadRequest(t, nil, []byte("not a real image"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an invalid image, got %d: %s", rec.Code, rec.Body.String())
	}
}

// newOversizedPNGBytes is a real, fully valid, fully decodable PNG whose
// pixel count sits just over imaging.MaxPixels — the actual pixel-bomb
// shape (ut-docs#1328/#1417): a solid color compresses to a tiny file
// while still being genuinely decodable, proving the guard rejects it via
// the cheap dimension check rather than some unrelated "corrupt file"
// reason.
func newOversizedPNGBytes(t *testing.T) []byte {
	t.Helper()
	const w, h = 7000, 6000
	if int64(w)*int64(h) <= imaging.MaxPixels {
		t.Fatalf("test fixture %dx%d must exceed imaging.MaxPixels (%d)", w, h, imaging.MaxPixels)
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode oversized test png: %v", err)
	}
	return buf.Bytes()
}

// TestReceiptDesignerLogo_RejectsPixelBombImage is the ut-docs#1417
// regression at this handler's actual real-world reachable point: before
// this fix, the validation gate here (print.RasterLogo(raw) == nil) used
// raw image.Decode with no dimension check, so a small, well-formed file
// declaring an enormous width×height would still be decoded in full
// before being written verbatim to receiptLogoPath() — and re-decoded on
// every subsequent receipt print. Must now be rejected the same way an
// undecodable file already is.
func TestReceiptDesignerLogo_RejectsPixelBombImage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReceiptDesignerTestDeps(t)
	req := logoUploadRequest(t, nil, newOversizedPNGBytes(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a pixel-bomb image, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(receiptLogoPath()); !os.IsNotExist(err) {
		t.Fatalf("expected no logo file written for a rejected upload, stat err: %v", err)
	}
}

// TestReceiptDesignerLogo_AcceptsGIF is the compatibility guard for the
// fix above: web/ui/pages/receipt_designer.html's file picker advertises
// image/gif, so bounding this upload's decode must not silently narrow it
// to png/jpeg only.
func TestReceiptDesignerLogo_AcceptsGIF(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReceiptDesignerTestDeps(t)
	img := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode test gif: %v", err)
	}
	req := logoUploadRequest(t, nil, buf.Bytes())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a valid GIF logo to be accepted, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(receiptLogoPath()); err != nil {
		t.Fatalf("expected the gif logo file written: %v", err)
	}
}

func TestReceiptDesignerLogo_UploadThenRemove(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReceiptDesignerTestDeps(t)
	ctx := context.Background()

	req := logoUploadRequest(t, nil, newValidPNGBytes(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logo upload: code %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(receiptLogoPath()); err != nil {
		t.Fatalf("expected the logo file written to %s: %v", receiptLogoPath(), err)
	}
	var uploadedCount int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'receipt_logo_uploaded'`).Scan(&uploadedCount); err != nil {
		t.Fatal(err)
	}
	if uploadedCount != 1 {
		t.Fatalf("expected one receipt_logo_uploaded audit row, got %d", uploadedCount)
	}

	req = logoUploadRequest(t, map[string]string{"remove": "1"}, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logo remove: code %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(receiptLogoPath()); err == nil {
		t.Fatalf("expected the logo file removed from %s", receiptLogoPath())
	}
	var removedCount int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'receipt_logo_removed'`).Scan(&removedCount); err != nil {
		t.Fatal(err)
	}
	if removedCount != 1 {
		t.Fatalf("expected one receipt_logo_removed audit row, got %d", removedCount)
	}
}

func TestReceiptDesignerTest_PrinterNotConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReceiptDesignerTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/test", strings.NewReader("header1=X"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when no printer is configured, got %d: %s", rec.Code, rec.Body.String())
	}
}
