package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExemptBodyLimit(t *testing.T) {
	for _, p := range []string{"/api/self-order/checkout", "/api/sync/pair-request", "/api/sync/enroll", "/api/auth/login", "/csp-report", "/api/sync/vouchers/abc/redeem"} {
		if got := ExemptBodyLimit(p); got != ExemptBodyMax {
			t.Errorf("%s: limit %d, want default %d", p, got, ExemptBodyMax)
		}
	}
	if ExemptBodyMax != 64<<10 {
		t.Fatalf("ExemptBodyMax=%d", ExemptBodyMax)
	}
	want := map[string]int64{
		"/api/sync/sales":             64 << 20,
		"/api/sync/closes":            4 << 20,
		"/api/sync/held-sales/upsert": 4 << 20,
		"/api/sync/catalog/apply":     1 << 20,
		"/api/sync/settings/apply":    256 << 10,
		"/api/sync/no-sales":          256 << 10,
		"/api/sync/cleared-baskets":   256 << 10,
	}
	for p, w := range want {
		if got := ExemptBodyLimit(p); got != w {
			t.Errorf("%s: limit %d, want %d", p, got, w)
		}
	}
	if len(ExemptBodyOverridePaths) != len(want) {
		t.Errorf("override paths = %v", ExemptBodyOverridePaths)
	}
}

func TestMiddlewareRejectsOversizedExemptBody(t *testing.T) {
	for _, path := range []string{"/api/sync/pair-request", "/csp-report", "/self-order/x"} {
		t.Run(path, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
			svc := &Service{}
			svc.EnableCSPReportSink()
			body := strings.NewReader(strings.Repeat("a", ExemptBodyMax+1))
			rec := httptest.NewRecorder()
			Middleware(next, svc).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, body))
			if reached {
				t.Fatal("handler ran for an oversized body")
			}
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status %d", rec.Code)
			}
			if rec.Header().Get("Connection") != "close" {
				t.Fatalf("Connection=%q", rec.Header().Get("Connection"))
			}
			if strings.HasPrefix(path, "/api/") {
				const want = `{"data":null,"error":{"code":"body_too_large","message":"request body too large"}}` + "\n"
				if rec.Body.String() != want || rec.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("body=%q ct=%q", rec.Body.String(), rec.Header().Get("Content-Type"))
				}
			} else if strings.Contains(rec.Body.String(), "{") {
				t.Fatalf("non-api path got JSON: %q", rec.Body.String())
			}
		})
	}
}

func TestMiddlewareCapsChunkedExemptBody(t *testing.T) {
	var gotErr error
	var got []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotErr = io.ReadAll(r.Body)
	})
	h := Middleware(next, &Service{})

	over := httptest.NewRequest(http.MethodPost, "/api/sync/pair-request", io.NopCloser(strings.NewReader(strings.Repeat("a", ExemptBodyMax+10))))
	over.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), over)
	var mbe *http.MaxBytesError
	if !errorsAs(gotErr, &mbe) {
		t.Fatalf("err=%v, want *http.MaxBytesError", gotErr)
	}

	under := httptest.NewRequest(http.MethodPost, "/api/sync/pair-request", io.NopCloser(strings.NewReader("hello")))
	under.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), under)
	if gotErr != nil || string(got) != "hello" {
		t.Fatalf("under-cap body altered: %q err=%v", got, gotErr)
	}
}

func TestMiddlewareDoesNotCapSessionRoutes(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "cashier", "1122")
	svc := NewService(db)
	token, err := svc.CreateSession(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n = len(b)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/import", strings.NewReader(strings.Repeat("a", 2<<20)))
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec := httptest.NewRecorder()
	Middleware(next, svc).ServeHTTP(rec, req)
	if n != 2<<20 {
		t.Fatalf("handler read %d bytes (status %d)", n, rec.Code)
	}
}

func errorsAs(err error, target **http.MaxBytesError) bool {
	for err != nil {
		if e, ok := err.(*http.MaxBytesError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
