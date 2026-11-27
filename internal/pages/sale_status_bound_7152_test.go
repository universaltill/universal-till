package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#7152: /api/pos/sale/status is POST-only and its body is capped.

func TestSaleStatus7152_RejectsNonPOST(t *testing.T) {
	dp, state, _, _ := voidSetup(t, "m1")
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(m, "/api/pos/sale/status", strings.NewReader(`{"saleId":"m1","status":"voided"}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, rec.Code)
		}
		if a := rec.Header().Get("Allow"); !strings.Contains(a, "POST") {
			t.Errorf("%s Allow = %q, want POST", m, a)
		}
	}
	if s, a := state(); s != "completed" || a != 0 {
		t.Fatalf("status=%q audits=%d, want completed/0", s, a)
	}
}

func TestSaleStatus7152_OversizeBodyIs413(t *testing.T) {
	dp, state, post, _ := voidSetup(t, "m2")
	before := len(elevationApprovals.m)
	rec := post("", strings.Repeat("x", 5000), "")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d %s, want 413", rec.Code, rec.Body.String())
	}
	if len(elevationApprovals.m) != before {
		t.Fatal("a token was minted for an oversize body")
	}
	if s, a := state(); s != "completed" || a != 0 {
		t.Fatalf("status=%q audits=%d", s, a)
	}
	// Malformed JSON still 400.
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	if rec := postSaleStatus(t, mux, nil, `{nope`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed = %d, want 400", rec.Code)
	}
}
