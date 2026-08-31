package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Cross-till orders (ut-docs#1350): the primary-side sync surface. GET
// /api/sync/orders serves the same recent-orders rows the local /ui/orders
// fragment renders, and POST /api/sync/orders/{receipt_no}/status applies the
// SAME guarded write as the human-facing one-tap endpoint — bearer-authed via
// syncTill, JSON envelope in and out (a replica re-renders its own HTML
// fragment from the structured response).

func newSyncOrdersTestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "sync_orders.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })

	dp := &common.Deps{Db: dbase.DB, OrderStatus: pos.NewOrderStatusBroadcaster()}
	mux := http.NewServeMux()
	registerSyncOrders(mux, dp)
	return mux, dp, dbase
}

// seedSyncOrdersTill enrols a fake replica and returns the bearer its calls
// must present.
func seedSyncOrdersTill(t *testing.T, dp *common.Deps, name, bearer string) {
	t.Helper()
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(context.Background(), name, hashBearer(bearer)); err != nil {
		t.Fatal(err)
	}
}

func postSyncOrderStatus(mux *http.ServeMux, receiptNo, status, bearer string) *httptest.ResponseRecorder {
	form := url.Values{"status": {status}}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/orders/"+receiptNo+"/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type syncOrderStatusResp struct {
	Data struct {
		Applied bool   `json:"applied"`
		Tracked bool   `json:"tracked"`
		Status  string `json:"status"`
		Who     string `json:"who"`
		When    string `json:"when"`
	} `json:"data"`
	Error any `json:"error"`
}

func TestSyncOrdersGet_RequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	// Missing bearer.
	req := httptest.NewRequest(http.MethodGet, "/api/sync/orders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status = %d, want 401", rec.Code)
	}

	// Wrong bearer.
	req = httptest.NewRequest(http.MethodGet, "/api/sync/orders", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", rec.Code)
	}
}

func TestSyncOrdersGet_ReturnsRecentOrders(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s1", "R-S1")
	seedOrderStatusTestSale(t, dbase, "sale-s2", "R-S2")

	repo := data.NewPOSRepo(dp.Db)
	applied, found, err := repo.ApplyOrderStatus(context.Background(), "R-S1", "preparing", "system",
		"2026-08-30T10:00:00Z", func(string) bool { return true })
	if err != nil || !applied || !found {
		t.Fatalf("seed status: applied=%v found=%v err=%v", applied, found, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sync/orders", nil)
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ReceiptNo            string `json:"receipt_no"`
			OrderType            string `json:"order_type"`
			Status               string `json:"status"`
			StatusUpdatedAt      string `json:"status_updated_at"`
			CreatedAt            string `json:"created_at"`
			KitchenPrintFailedAt string `json:"kitchen_print_failed_at"`
			ReceiptPrintFailedAt string `json:"receipt_print_failed_at"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("error = %v, want null", resp.Error)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("want 2 orders, got %d (%q)", len(resp.Data), rec.Body.String())
	}
	byReceipt := map[string]string{}
	for _, row := range resp.Data {
		byReceipt[row.ReceiptNo] = row.Status
		if row.CreatedAt == "" {
			t.Fatalf("row %s missing created_at", row.ReceiptNo)
		}
	}
	if byReceipt["R-S1"] != "preparing" {
		t.Fatalf("R-S1 status = %q, want preparing", byReceipt["R-S1"])
	}
	if got, ok := byReceipt["R-S2"]; !ok || got != "" {
		t.Fatalf("R-S2 must list as untracked (\"\"), got %q ok=%v", got, ok)
	}
}

func TestSyncOrdersStatusPost_AppliesForwardMove(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s1", "R-S3")

	ch, cancel := dp.OrderStatus.Subscribe()
	defer cancel()

	rec := postSyncOrderStatus(mux, "R-S3", "preparing", "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncOrderStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if !resp.Data.Applied || !resp.Data.Tracked || resp.Data.Status != "preparing" {
		t.Fatalf("resp = %+v, want applied/tracked with status preparing", resp.Data)
	}
	// Who: the calling TILL, not a human session — there is none on a
	// machine-to-machine call.
	if resp.Data.Who != "Till 2" {
		t.Fatalf("who = %q, want the calling till's name", resp.Data.Who)
	}
	if resp.Data.When == "" {
		t.Fatal("when must carry the write's timestamp")
	}

	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-S3'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "preparing" {
		t.Fatalf("stored status = %q, want preparing", status)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE receipt_no='R-S3'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("want 1 journal event, got %d", events)
	}
	var audits int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='sale' AND entity_id='R-S3' AND action='order_status_changed'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("want 1 audit entry, got %d", audits)
	}

	select {
	case ev := <-ch:
		if ev.ReceiptNo != "R-S3" || ev.Status != "preparing" {
			t.Fatalf("broadcast = %+v, want R-S3/preparing", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("applied status change never broadcast")
	}
}

func TestSyncOrdersStatusPost_StaleMoveSilentlyNoOps(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s1", "R-S4")
	if rec := postSyncOrderStatus(mux, "R-S4", "ready", "bearer-t2"); rec.Code != http.StatusOK {
		t.Fatalf("setup move: %d %q", rec.Code, rec.Body.String())
	}

	ch, cancel := dp.OrderStatus.Subscribe()
	defer cancel()

	rec := postSyncOrderStatus(mux, "R-S4", "preparing", "bearer-t2")
	if rec.Code != http.StatusOK {
		t.Fatalf("stale move must not error: %d (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncOrderStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Applied {
		t.Fatal("stale backward move must report applied=false")
	}
	if resp.Data.Status != "ready" {
		t.Fatalf("resp status = %q, must keep showing ready", resp.Data.Status)
	}

	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-S4'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Fatalf("status regressed to %q, must stay ready", status)
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE receipt_no='R-S4'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("dropped write must not journal: want 1 event, got %d", events)
	}
	select {
	case ev := <-ch:
		t.Fatalf("dropped write must not broadcast, got %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// A resolvable actor_id (a real user on THIS till) is honored as the
// journal AND audit actor — accountability for who actually tapped it, not
// just which till relayed the tap (ut-docs#1350 review: attribution was
// lost on every cross-till write until this).
func TestSyncOrdersStatusPost_ResolvableActorIDAttributesRealOperator(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s7", "R-S7")
	if _, err := dbase.DB.Exec(`INSERT INTO users(id,username,display_name,role,is_active) VALUES('op-ayse','ayse','Ayşe','cashier',1)`); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"status": {"preparing"}, "actor_id": {"op-ayse"}}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/orders/R-S7/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncOrderStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Who != "Ayşe" {
		t.Fatalf("who = %q, want the RESOLVED real operator, not the till", resp.Data.Who)
	}

	var journalActor string
	if err := dbase.DB.QueryRow(`SELECT actor_id FROM order_status_events WHERE receipt_no='R-S7'`).Scan(&journalActor); err != nil {
		t.Fatal(err)
	}
	if journalActor != "op-ayse" {
		t.Fatalf("journal actor = %q, want op-ayse", journalActor)
	}
	var auditActor, payload string
	if err := dbase.DB.QueryRow(`SELECT actor_id, data_json FROM audit_log WHERE entity_type='sale' AND entity_id='R-S7' AND action='order_status_changed'`).Scan(&auditActor, &payload); err != nil {
		t.Fatal(err)
	}
	if auditActor != "op-ayse" {
		t.Fatalf("audit actor = %q, want the resolved real operator op-ayse (not \"system\")", auditActor)
	}
	if !strings.Contains(payload, "source_till") || !strings.Contains(payload, "Till 2") {
		t.Fatalf("audit payload must still name the relaying till even when the operator resolved, got %q", payload)
	}
}

// An UNRESOLVABLE actor_id — a peer till claiming an id that isn't a real
// user on THIS (primary) till — must never be trusted at face value: the
// write still applies, but attribution falls back to the till, exactly as
// if no actor_id had been sent at all.
func TestSyncOrdersStatusPost_UnresolvableActorIDFallsBackToTill(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s8", "R-S8")

	form := url.Values{"status": {"preparing"}, "actor_id": {"not-a-real-user-id"}}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/orders/R-S8/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer bearer-t2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp syncOrderStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Who != "Till 2" {
		t.Fatalf("who = %q, want the calling till (unresolved claimed actor must never be trusted)", resp.Data.Who)
	}
	var auditActor string
	if err := dbase.DB.QueryRow(`SELECT actor_id FROM audit_log WHERE entity_type='sale' AND entity_id='R-S8' AND action='order_status_changed'`).Scan(&auditActor); err != nil {
		t.Fatal(err)
	}
	if auditActor != "system" {
		t.Fatalf("audit actor = %q, want \"system\" — an unresolved claimed id must never reach the users-FK column", auditActor)
	}
}

func TestSyncOrdersStatusPost_RequiresBearer(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s1", "R-S5")

	if rec := postSyncOrderStatus(mux, "R-S5", "preparing", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: status = %d, want 401", rec.Code)
	}
	if rec := postSyncOrderStatus(mux, "R-S5", "preparing", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: status = %d, want 401", rec.Code)
	}
	var status string
	if err := dbase.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-S5'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		t.Fatalf("unauthorized call must not write, got status %q", status)
	}
}

// Unknown receipt mirrors the human-facing endpoint's behavior: 404, no panic.
func TestSyncOrdersStatusPost_UnknownReceiptNotFound(t *testing.T) {
	mux, dp, _ := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	rec := postSyncOrderStatus(mux, "R-NOPE", "preparing", "bearer-t2")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestSyncOrdersStatusPost_UnknownStatusRejected(t *testing.T) {
	mux, dp, dbase := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	seedOrderStatusTestSale(t, dbase, "sale-s1", "R-S6")
	rec := postSyncOrderStatus(mux, "R-S6", "bogus", "bearer-t2")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
	var events int
	if err := dbase.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected input must not journal, got %d events", events)
	}
}
