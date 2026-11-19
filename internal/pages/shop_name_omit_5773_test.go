package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5773: an unnamed shop shows/prints/sends no name at all — core
// never substitutes "this shop" or "My Store" (same rule as
// data.ShopContextRow).

func TestShopNameOrEmpty(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	ctx := t.Context()
	if got := shopNameOrEmpty(ctx, dp); got != "" {
		t.Fatalf("key as seeded (placeholder) = %q, want empty", got)
	}
	for _, c := range []struct{ set, want string }{
		{"", ""}, {"  ", ""}, {"My Store", ""}, {" my store ", ""},
		{"Universal Till store", ""}, {" Café Linde ", "Café Linde"},
	} {
		if err := dp.Settings.Set(ctx, common.KeyStoreName, c.set); err != nil {
			t.Fatal(err)
		}
		if got := shopNameOrEmpty(ctx, dp); got != c.want {
			t.Errorf("store.name %q -> %q, want %q", c.set, got, c.want)
		}
	}
}

func TestKioskShopName(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	ctx := t.Context()
	for _, c := range []struct{ store, cfg, want string }{
		{"", "My Store", ""},
		{"", "Kiosk Café", "Kiosk Café"},
		{"Corner Bakery", "Anything", "Corner Bakery"},
		{"My Store", "", ""},
		{"My Store", "My Store", ""},
	} {
		if err := dp.Settings.Set(ctx, common.KeyStoreName, c.store); err != nil {
			t.Fatal(err)
		}
		dp.Cfg.StoreName = c.cfg
		if got := kioskShopName(ctx, dp); got != c.want {
			t.Errorf("store %q cfg %q -> %q, want %q", c.store, c.cfg, got, c.want)
		}
	}
}

func TestSelfOrder_UnnamedShowsNoName(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	dp.Cfg.StoreName = "My Store" // UT_STORE_NAME unset: the env default
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `selforder-shop"`) || strings.Contains(body, "My Store") {
		t.Fatalf("unnamed kiosk must show no shop name, got:\n%s", body)
	}
}

func TestReceiptDesigner_UnnamedShopPreviewHasNoNameRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReceiptDesignerTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/receipt-designer/preview", strings.NewReader("header1=1+High+St"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "this shop") || strings.Contains(body, "My Store") {
		t.Fatalf("placeholder in preview: %q", body)
	}
	if first, _, _ := strings.Cut(body, "\n"); strings.TrimSpace(first) != "1 High St" {
		t.Fatalf("preview should start with the header line, first line %q", first)
	}
}

func TestReportsTabs_PlaceOfBusinessOnlyWhenNamed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	for _, tab := range []string{"tips", "yuzde-usulu"} {
		rec := getReportsTab(t, mux, tab, "?days=14")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", tab, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "Place of business") || strings.Contains(rec.Body.String(), "this shop") {
			t.Errorf("%s unnamed must hide the place-of-business card: %s", tab, rec.Body.String())
		}
	}
	if err := dp.Settings.Set(t.Context(), common.KeyStoreName, "Café Linde"); err != nil {
		t.Fatal(err)
	}
	rec := getReportsTab(t, mux, "tips", "?days=14")
	if !strings.Contains(rec.Body.String(), "Place of business: <strong>Café Linde</strong>") {
		t.Errorf("named tips tab lost its place-of-business line: %s", rec.Body.String())
	}
}

func TestSyncEnrolAndPing_UnnamedShopNameIsEmpty(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	code := issueEnrolCode(t, mux, "http://192.168.1.10:8080")
	_, token, err := decodeEnrollCode(code)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "name": "Till 2"})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var resp struct {
		Data  map[string]any `json:"data"`
		Error any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != 200 {
		t.Fatalf("enrol %d %s", rec.Code, rec.Body.String())
	}
	if v, ok := resp.Data["shop_name"]; !ok || v != "" || resp.Error != nil {
		t.Fatalf("enrol shop_name = %#v (present %v) error %v, want \"\"", v, ok, resp.Error)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Data["bearer"].(string))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	resp.Data = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if v, ok := resp.Data["shop_name"]; !ok || v != "" {
		t.Fatalf("ping shop_name = %#v, want \"\"", v)
	}
}

func TestJoinSuccessFragments_NoTrailingColonWhenUnnamed(t *testing.T) {
	initPagesI18n(t)
	chdirRoot(t)
	for name, render := range map[string]func(w http.ResponseWriter, r *http.Request, shop string){
		"join_success": func(w http.ResponseWriter, r *http.Request, shop string) {
			renderJoinSuccess(w, r, shop, "", false)
		},
		"pair_wait": func(w http.ResponseWriter, r *http.Request, shop string) {
			pairWaitViewPolling(w, r, "/s", "joined", "", shop, "", false)
		},
	} {
		rec := httptest.NewRecorder()
		render(rec, httptest.NewRequest(http.MethodGet, "/x", nil), "")
		if b := rec.Body.String(); !strings.Contains(b, "✓ Joined") || strings.Contains(b, "Joined:") {
			t.Errorf("%s unnamed: %s", name, b)
		}
		rec = httptest.NewRecorder()
		render(rec, httptest.NewRequest(http.MethodGet, "/x", nil), "Corner Shop")
		if !strings.Contains(rec.Body.String(), "✓ Joined: Corner Shop") {
			t.Errorf("%s named: %s", name, rec.Body.String())
		}
	}
}

func TestMovePage_UnnamedShopNeverShowsMyStore(t *testing.T) {
	mux, d, _ := moveApprovalDeps(t)
	d.Cfg.StoreName = "My Store"
	rec := getWithUser(mux, "/settings/cloud/move/"+mvID, &mgrUser)
	body := rec.Body.String()
	if strings.Contains(body, "My Store") {
		t.Fatalf("move page shows the placeholder: %s", body)
	}
	if !strings.Contains(body, "<bdi>"+mvStore+"</bdi>") {
		t.Fatalf("From should fall back to the cloud's SourceStore %q: %s", mvStore, body)
	}
}

// The unnamed doc end to end: nothing a customer keeps carries a placeholder.
func TestUnnamedDocRendersNoPlaceholder(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	doc := print.Doc{StoreName: shopNameOrEmpty(t.Context(), dp), Header: []string{"1 High St"}}
	for _, out := range []string{string(print.Render(doc)), print.RenderText(doc)} {
		if strings.Contains(out, "this shop") || strings.Contains(out, "My Store") {
			t.Fatalf("placeholder printed: %q", out)
		}
	}
}
