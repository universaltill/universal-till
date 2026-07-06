package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestMarketplaceReporterReports(t *testing.T) {
	var (
		mu                              sync.Mutex
		gotURL                          string
		gotA, gotState, gotErr, gotAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		gotURL = r.URL.Path
		gotA = r.Method
		gotState = r.FormValue("state")
		gotErr = r.FormValue("error")
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewMarketplaceReporter(srv.URL, "secret-token")
	if r == nil {
		t.Fatal("expected a reporter")
	}
	r.Report(context.Background(), "intent-123", InstallStateFailed, "signature invalid")

	mu.Lock()
	defer mu.Unlock()
	if gotA != http.MethodPost {
		t.Fatalf("method = %s", gotA)
	}
	if gotURL != "/ui/api/installs/intent-123/state" {
		t.Fatalf("url = %s", gotURL)
	}
	if gotState != "failed" || gotErr != "signature invalid" {
		t.Fatalf("state=%q err=%q", gotState, gotErr)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestMarketplaceReporterDisabledAndNoop(t *testing.T) {
	// Empty base URL -> nil reporter.
	if NewMarketplaceReporter("", "t") != nil {
		t.Fatal("empty base URL should yield a nil reporter")
	}
	// nil reporter + empty intent id are safe no-ops (must not panic or call out).
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	var nilReporter *MarketplaceReporter
	nilReporter.Report(context.Background(), "x", InstallStateActive, "")

	NewMarketplaceReporter(srv.URL, "t").Report(context.Background(), "", InstallStateActive, "")
	if called {
		t.Fatal("no HTTP call should have been made")
	}
}
