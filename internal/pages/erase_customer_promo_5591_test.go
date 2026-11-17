package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5591: erasing a customer through the real handler must not leave
// their customer-bound promo applying to everyone (NULL customer_id = any).
func TestEraseCustomer_DeactivatesBoundPromo(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO customers(id,name) VALUES('cust-5591','Jane Doe')`,
		`INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES ('ONLYJANE5591','amount',700,'cust-5591',1)`,
	} {
		if _, err := dp.Db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	repo := data.NewPOSRepo(dp.Db)
	if _, _, found := repo.FindActivePromo(ctx, "cust-5591", "ONLYJANE5591"); !found {
		t.Fatal("precondition: bound promo must apply to its customer")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=cust-5591&override_pin="+dataAPITestManagerPIN))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("erase: %d %s", rec.Code, rec.Body.String())
	}
	for _, cust := range []string{"", "cust-5591", "someone-else"} {
		if _, _, found := repo.FindActivePromo(ctx, cust, "ONLYJANE5591"); found {
			t.Fatalf("erased customer's promo still applies for %q", cust)
		}
	}
}
