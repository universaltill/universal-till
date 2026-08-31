package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Cross-till orders, replica side (ut-docs#1350): when this till is a replica
// (sync.primary_url set), /ui/orders and the one-tap status POST first try the
// primary's /api/sync/orders* endpoints so every till sees and controls the
// whole shop's orders; on ANY failure they fall back — silently — to the
// existing local-DB path, so an offline station keeps working exactly as
// before (offline-first, ADR-0003).

// setReplicaSettings points the test deps' settings at a (fake) primary.
// bearer "" leaves sync.bearer unset — a half-configured replica must behave
// local-only.
func setReplicaSettings(t *testing.T, s interface {
	Set(ctx context.Context, key, value string) error
}, primaryURL, bearer string) {
	t.Helper()
	if err := s.Set(context.Background(), "sync.primary_url", primaryURL); err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		if err := s.Set(context.Background(), "sync.bearer", bearer); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUIOrders_ReplicaRendersPrimaryData(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	// Local DB has its own order — it must NOT render when the primary answers.
	seedOrderStatusTestSale(t, dbase, "sale-local", "LOCAL-1")

	var gotAuth atomic.Value
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sync/orders" {
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"receipt_no":"PRIMARY-1","order_type":"takeaway","status":"ready","status_updated_at":"2026-08-31T09:00:00Z","created_at":"2026-08-31T08:55:00Z","kitchen_print_failed_at":"","receipt_print_failed_at":""}],"error":null}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PRIMARY-1") {
		t.Fatalf("replica must render the PRIMARY's orders, got %q", body)
	}
	if !strings.Contains(body, "Ready") {
		t.Fatalf("replica must render the primary row's status label, got %q", body)
	}
	if strings.Contains(body, "LOCAL-1") {
		t.Fatalf("local rows must not render when the primary answered, got %q", body)
	}
	if auth, _ := gotAuth.Load().(string); auth != "Bearer b-123" {
		t.Fatalf("primary must be called with the sync bearer, got %q", auth)
	}
}

func TestUIOrders_ReplicaFallsBackToLocalWhenPrimaryUnreachable(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "LOCAL-2")

	// A primary that WAS there and is now gone — connection refused.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := primary.URL
	primary.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "LOCAL-2") {
		t.Fatalf("unreachable primary must fall back to the local list, got %q", body)
	}
}

// A replica whose sync.bearer isn't set must not even attempt the primary —
// straight to the local path, no error.
func TestUIOrders_ReplicaWithoutBearerStaysLocal(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "LOCAL-3")

	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "")

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "LOCAL-3") {
		t.Fatalf("must render the local list, got %q", body)
	}
	if calls.Load() != 0 {
		t.Fatalf("must not call the primary without a bearer, got %d calls", calls.Load())
	}
}

func TestOrderStatusPost_ReplicaProxiesWriteToPrimary(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "R-P1")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sync/orders/R-P1/status" {
			t.Errorf("unexpected primary call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer b-123" {
			t.Errorf("bearer = %q, want Bearer b-123", got)
		}
		_ = r.ParseForm()
		if got := r.Form.Get("status"); got != "ready" {
			t.Errorf("proxied status = %q, want ready", got)
		}
		// The proxy must relay this till's own session user (ut-docs#1350
		// review) so the primary can attribute the real operator instead of
		// just the till. No auth middleware in this test harness → "system"
		// (auth.UserID's no-session fallback), same as the existing
		// human-facing tests' own "system" convention.
		if got := r.Form.Get("actor_id"); got != "system" {
			t.Errorf("proxied actor_id = %q, want system", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"applied":true,"tracked":true,"status":"ready","who":"Till 9","when":"2026-08-31T09:00:00Z"},"error":null}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	rec := postOrderStatus(mux, "R-P1", "ready")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Ready") {
		t.Fatalf("fragment must render the primary's resulting status, got %q", body)
	}
	if !strings.Contains(body, "Till 9") {
		t.Fatalf("fragment must show the primary-reported who, got %q", body)
	}

	// The write went to the PRIMARY — the local row stays untouched (the
	// /ui/orders view reads the primary too, so the display stays coherent).
	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-P1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		t.Fatalf("proxied write must not also apply locally, got %q", status)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE receipt_no='R-P1'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("proxied write must not journal locally, got %d events", events)
	}
}

// The production failure mode (ut-docs#1350 review): the primary is fully
// REACHABLE but answers something other than a clean 200 (e.g. 401 — the
// exact shape an auth-middleware misconfiguration produces before syncTill
// ever runs) or a 200 with a body that isn't the expected JSON shape. Both
// must fall back exactly like an unreachable primary — connection-refused
// alone is not the whole "ANY failure" contract.
func TestUIOrders_ReplicaFallsBackWhenPrimaryAnswersNon200(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "LOCAL-4")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "LOCAL-4") {
		t.Fatalf("a non-200 primary answer must fall back to the local list, got %q", body)
	}
}

func TestUIOrders_ReplicaFallsBackWhenPrimaryAnswersMalformedBody(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "LOCAL-5")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not json`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	req := httptest.NewRequest(http.MethodGet, "/ui/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "LOCAL-5") {
		t.Fatalf("a malformed primary body must fall back to the local list, got %q", body)
	}
}

func TestOrderStatusPost_ReplicaFallsBackWhenPrimaryAnswersNon200(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "R-P3")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	rec := postOrderStatus(mux, "R-P3", "preparing")
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Preparing") {
		t.Fatalf("a non-200 primary answer must fall back to the local write, got %q", body)
	}
	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-P3'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "preparing" {
		t.Fatalf("non-200 primary answer must fall back to the LOCAL write, got %q", status)
	}
}

func TestOrderStatusPost_ReplicaFallsBackToLocalApplyWhenPrimaryUnreachable(t *testing.T) {
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-local", "R-P2")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := primary.URL
	primary.Close()
	setReplicaSettings(t, dp.Settings, deadURL, "b-123")

	rec := postOrderStatus(mux, "R-P2", "preparing")
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback must be silent: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Preparing") {
		t.Fatalf("fragment must show the locally-applied status, got %q", body)
	}
	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-P2'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "preparing" {
		t.Fatalf("unreachable primary must fall back to the LOCAL write, got %q", status)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE receipt_no='R-P2'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("local fallback must journal exactly as today, got %d events", events)
	}
}
