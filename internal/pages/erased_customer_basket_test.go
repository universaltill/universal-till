package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3253 review: an erased customer still attached to an open basket
// would be written straight back into held_sales by the next Hold.
func TestEraseCustomer_DetachesCustomerFromOpenBaskets(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	dp.Engine = pos.NewServiceWithResolver(pos.Config{}, nil)
	dp.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, nil)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name) VALUES('cust1','Jane Doe'),('cust2','Other Person')`); err != nil {
		t.Fatal(err)
	}
	dp.Engine.SetCustomer("cust1", "Jane Doe")
	dp.KioskEngine.SetCustomer("cust2", "Other Person")

	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=cust1&override_pin="+dataAPITestManagerPIN))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("erase: %d %s", rec.Code, rec.Body.String())
	}
	if got := dp.Engine.CustomerID(); got != "" {
		t.Errorf("open basket still holds the erased customer %q", got)
	}
	if got := dp.KioskEngine.CustomerID(); got != "cust2" {
		t.Errorf("another customer's basket was cleared: got %q", got)
	}
}

func TestForgetCustomersGoneSince_DetachesOnlyCustomersThatVanished(t *testing.T) {
	_, dp := newDataAPITestDeps(t)
	dp.Engine = pos.NewServiceWithResolver(pos.Config{}, nil)
	dp.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, nil)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name) VALUES('gone','Erased Person'),('kept','Kept Person')`); err != nil {
		t.Fatal(err)
	}
	dp.Engine.SetCustomer("gone", "Erased Person")
	dp.KioskEngine.SetCustomer("kept", "Kept Person")
	repo := data.NewPOSRepo(dp.Db)
	known := basketCustomersKnown(t.Context(), dp, repo)
	if len(known) != 2 {
		t.Fatalf("known = %v, want both", known)
	}
	// What the replica's admin pull does to an erased customer pinned by local sales.
	if _, err := dp.Db.ExecContext(t.Context(), `UPDATE customers SET name='' WHERE id='gone'`); err != nil {
		t.Fatal(err)
	}
	forgetCustomersGoneSince(t.Context(), dp, repo, known)
	if got := dp.Engine.CustomerID(); got != "" {
		t.Errorf("basket kept the erased customer %q", got)
	}
	if got := dp.KioskEngine.CustomerID(); got != "kept" {
		t.Errorf("a still-existing customer was detached: got %q", got)
	}
}
