package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefuseTextMarksAPlainTextRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	RefuseText(rec, "Can't reach the main till.", http.StatusBadGateway)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("X-UT-Response"); got != "refused" {
		t.Fatalf("X-UT-Response = %q, want refused", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "Can't reach the main till." {
		t.Fatalf("body = %q", got)
	}
}
