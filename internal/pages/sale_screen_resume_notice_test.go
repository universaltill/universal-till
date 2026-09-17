package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2193: /open-orders' own resume route (POST /open-orders/resume,
// open_orders_page.go) is a full-page 303 redirect with no success-notice
// mechanism of its own -- unlike the sale-screen popup's htmx fragment path
// (POST /api/pos/resume, hold_api.go), which already tells the cashier via
// hold.toast.parked_and_resumed when their own busy basket got auto-parked
// (ut-docs#1919). A cashier resuming from THIS page got zero on-screen cue.
// The fix reuses httpx.QueryMsgKey (ut-docs#2148, previously unused) exactly
// as the existing tseKickoffRejected one-shot banner does: the redirect
// carries "?msg=<key>", the sale screen's first paint renders it as a
// .pos-notice success banner, and a plain revisit with no param shows none.
func TestSaleScreen_ResumeNoticeBannerRendersFromMsgParam(t *testing.T) {
	mux, _ := quickPayTestMux(t)

	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200: %s", target, rec.Code, rec.Body.String())
		}
		return rec
	}

	if body := get("/").Body.String(); strings.Contains(body, "resume-notice-banner") {
		t.Fatalf("no ?msg= param -- expected no resume-notice banner, got: %s", body)
	}

	body := get("/?msg=hold.toast.parked_and_resumed").Body.String()
	if !strings.Contains(body, "resume-notice-banner") {
		t.Fatalf("expected the resume-notice banner with ?msg= set, got: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "hold.toast.parked_and_resumed")) {
		t.Fatalf("expected the translated parked-and-resumed text, got: %s", body)
	}
	if !strings.Contains(body, `class="pos-notice success"`) || !strings.Contains(body, `role="status"`) {
		t.Fatalf("expected a success-styled, role=status notice (matches hold_api.go's own renderBasket convention for a success toast), got: %s", body)
	}

	// An unrecognised key must not render verbatim (the same spoofing
	// concern QueryErrKey/QueryMsgKey already guard against, ut-docs#2148)
	// -- it falls back to the generic key instead.
	garbled := get("/?msg=not-a-real-key").Body.String()
	if strings.Contains(garbled, "not-a-real-key") {
		t.Fatalf("an unrecognised ?msg= value must not render verbatim, got: %s", garbled)
	}
}
