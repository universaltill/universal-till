package pages

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5589: the promotion dialog's customer picker takes the Promotions
// page's own gate ("settings"), not the GDPR erasure picker's
// "data_management". Real role grants, never UT_AUTH=off, for the gate cases.

const pickerPath = "/api/promotions/customers"

// grantOnly creates a custom (cloud) role holding exactly the given actions.
func grantOnly(t *testing.T, db *sql.DB, roleKey string, actions ...string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO roles (role, label, origin) VALUES (?, 'Test', 'cloud')`, roleKey); err != nil {
		t.Fatalf("insert role: %v", err)
	}
	for _, a := range actions {
		if _, err := db.Exec(`INSERT INTO role_permissions(role, action, granted) VALUES(?,?,1)`, roleKey, a); err != nil {
			t.Fatalf("grant %s: %v", a, err)
		}
	}
}

func seedPickerCustomer(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO customers(id, name, phone, email) VALUES ('c-jane','Jane Doe','555-0100','jane@example.com')`); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
}

func pickerGET(mux *http.ServeMux, path, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if role != "" {
		req = auth.WithUser(req, auth.User{ID: role + "-1", Role: role})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type pickerBody struct {
	Data *struct {
		Customers []map[string]string `json:"customers"`
	} `json:"data"`
	Error *string `json:"error"`
}

func TestPromoCustomerPicker_SettingsOnlyRole(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	registerDataAPI(mux, d)
	seedPickerCustomer(t, d.Db)
	grantOnly(t, d.Db, "c_5589settings", "settings")

	if rec := pickerGET(mux, "/promotions", "c_5589settings"); rec.Code != http.StatusOK {
		t.Fatalf("GET /promotions = %d", rec.Code)
	}
	rec := pickerGET(mux, pickerPath+"?q=Jane", "c_5589settings")
	if rec.Code != http.StatusOK {
		t.Fatalf("picker = %d: %s", rec.Code, rec.Body.String())
	}
	var b pickerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || b.Data == nil || b.Error != nil {
		t.Fatalf("envelope: %v %s", err, rec.Body.String())
	}
	if len(b.Data.Customers) != 1 {
		t.Fatalf("customers = %+v", b.Data.Customers)
	}
	c := b.Data.Customers[0]
	if c["id"] != "c-jane" || c["name"] != "Jane Doe" || c["phone"] != "555-0100" || len(c) != 3 {
		t.Fatalf("row = %+v", c)
	}
	// AC5: no email key, no email value.
	body := rec.Body.String()
	if strings.Contains(body, "jane@example.com") || strings.Contains(strings.ToLower(body), `"email"`) {
		t.Fatalf("picker leaks email: %s", body)
	}
	// Review: a LIKE wildcard is not a way round the blank-query guard.
	for _, q := range []string{"%25", "_"} {
		rec := pickerGET(mux, pickerPath+"?q="+q, "c_5589settings")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"customers":[]`) {
			t.Fatalf("q=%s lists customers: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	// AC2: the GDPR picker's gate is unchanged for the same role.
	if rec := pickerGET(mux, "/api/data/customers?q=Jane", "c_5589settings"); rec.Code != http.StatusForbidden {
		t.Fatalf("GDPR picker for settings-only role = %d, want 403", rec.Code)
	}
}

func TestPromoCustomerPicker_DataManagementOnlyDenied(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPickerCustomer(t, d.Db)
	grantOnly(t, d.Db, "c_5589datamgmt", "data_management")

	rec := pickerGET(mux, pickerPath+"?q=Jane", "c_5589datamgmt")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("picker = %d, want 403", rec.Code)
	}
	var b pickerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || b.Error == nil {
		t.Fatalf("deny body: %v %s", err, rec.Body.String())
	}
	if want := httpx.T("en", "common.error.manager_or_admin_required"); *b.Error != want {
		t.Fatalf("error = %q, want %q", *b.Error, want)
	}
}

func TestPromoCustomerPicker_BuiltInRoles(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	seedPickerCustomer(t, d.Db)
	for role, want := range map[string]int{
		"cashier": http.StatusForbidden, "manager": http.StatusOK,
		"admin": http.StatusOK, "super_admin": http.StatusOK,
	} {
		if rec := pickerGET(mux, pickerPath+"?q=Jane", role); rec.Code != want {
			t.Errorf("%s = %d, want %d", role, rec.Code, want)
		}
	}
}

func TestPromoCustomerPicker_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newPromotionsTestMux(t)
	seedPickerCustomer(t, d.Db)
	if rec := pickerGET(mux, pickerPath+"?q=Jane", ""); rec.Code != http.StatusOK {
		t.Fatalf("under UT_AUTH=off = %d", rec.Code)
	}
}

func TestPromoCustomerPicker_EmptyResultIsArray(t *testing.T) {
	mux, _ := newPromotionsTestMux(t)
	for _, q := range []string{"?q=nobody-matches", "?q=%20%20", ""} {
		rec := pickerGET(mux, pickerPath+q, "manager")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"customers":[]`) {
			t.Fatalf("%q: %d %s", q, rec.Code, rec.Body.String())
		}
	}
}

// AC7: the dialog uses the promotions lookup; the GDPR UI keeps its own.
func TestPromoCustomerPicker_TemplatesWired(t *testing.T) {
	chdirRoot(t)
	promo, err := os.ReadFile("web/ui/pages/promotions.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(promo), "/api/data/customers") || !strings.Contains(string(promo), pickerPath) {
		t.Fatal("promotions.html must call " + pickerPath + " and not /api/data/customers")
	}
	settings, err := os.ReadFile("web/ui/pages/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "/api/data/customers") {
		t.Fatal("settings.html GDPR picker must keep /api/data/customers")
	}
}
