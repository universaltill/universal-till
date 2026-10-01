package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ut-docs#2913: browsers send CSP violation reports from every page the
// report-only policy covers — the login and setup pages too, before anyone
// signs in — so POST /csp-report must reach its handler without a session. The exemption is POST-only and exact-path: the GET inventory of
// collected violations stays behind auth, and lookalike paths never ride
// along.
func TestCSPReportExemptionIsPostOnly(t *testing.T) {
	cases := []struct {
		method, path string
		wantReached  bool
	}{
		{http.MethodPost, "/csp-report", true},
		{http.MethodGet, "/csp-report", false},
		{http.MethodHead, "/csp-report", false},
		{http.MethodPut, "/csp-report", false},
		{http.MethodDelete, "/csp-report", false},
		{http.MethodPost, "/csp-report/extra", false},
		{http.MethodPost, "/csp-reportx", false},
		{http.MethodPost, "/api/csp-report", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			})
			// No cookie, so svc is only consulted for the exemption seam.
			svc := &Service{}
			svc.EnableCSPReportSink()
			h := Middleware(next, svc)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if reached != tc.wantReached {
				t.Fatalf("%s %s: handler reached = %v, want %v (status %d)", tc.method, tc.path, reached, tc.wantReached, rec.Code)
			}
			if !tc.wantReached && rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s: status = %d, want the middleware's own 303/401", tc.method, tc.path, rec.Code)
			}
		})
	}
}

// With UT_CSP_REPORT_ONLY off pages.Init never enables the sink, and POST
// /csp-report must get the session check like any other path — not fall
// through anonymously to the "/" catch-all (ut-docs#2913 review blocker).
func TestCSPReportExemptionOffWithoutSink(t *testing.T) {
	for name, svc := range map[string]*Service{"sink not enabled": {}, "nil service": nil} {
		t.Run(name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
			rec := httptest.NewRecorder()
			Middleware(next, svc).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/csp-report", nil))
			if reached {
				t.Fatalf("POST /csp-report reached the handler anonymously with the sink off (status %d)", rec.Code)
			}
			if rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want the middleware's own 303/401", rec.Code)
			}
		})
	}
}
