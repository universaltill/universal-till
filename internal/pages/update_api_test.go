package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// The updater endpoints are manager-gated. Without auth-off and without a
// manager in context, both must refuse before doing any work (crucially, apply
// must not reach selfupdate.Apply, and check must not hit the network). These
// tests deliberately leave UT_AUTH unset so the gate is exercised.
func TestUpdateAPI_ManagerGate(t *testing.T) {
	t.Setenv("UT_AUTH", "") // ensure auth is NOT disabled for this test

	dp := &common.Deps{}
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)

	for _, path := range []string{"/api/update/apply", "/api/update/check"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without manager: code %d, want 403 (body %q)",
				path, rec.Code, rec.Body.String())
		}
	}
}
