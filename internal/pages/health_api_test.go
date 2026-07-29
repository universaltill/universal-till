package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterHealth(t *testing.T) {
	mux := http.NewServeMux()
	registerHealth(mux)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: code %d", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("GET /healthz body = %q, want %q", got, "ok")
	}
}
