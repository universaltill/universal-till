package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Customer order tracking via QR (ut-docs#527): /o/{token} is an anonymous,
// token-gated, STATUS-ONLY read surface — these tests pin exactly that shape.

func setupOrderTrackingDeps(t *testing.T) (*common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "tracking.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	return &common.Deps{Cfg: &config.Config{}, Db: d.DB}, d
}

func seedTrackedSale(t *testing.T, d *db.DB, saleID, receiptNo string) string {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES (?,?,'completed','sale','GBP',370,0,0,370,datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	tok, err := data.NewPOSRepo(d.DB).EnsureOrderTrackingToken(context.Background(), receiptNo)
	if err != nil {
		t.Fatalf("EnsureOrderTrackingToken: %v", err)
	}
	return tok
}

func setTrackedStatus(t *testing.T, d *db.DB, receiptNo, status string, at time.Time) {
	t.Helper()
	applied, _, err := data.NewPOSRepo(d.DB).ApplyOrderStatus(context.Background(), receiptNo, status, "u-test",
		at.UTC().Format(time.RFC3339), func(string) bool { return true })
	if err != nil || !applied {
		t.Fatalf("ApplyOrderStatus(%s): applied=%v err=%v", status, applied, err)
	}
}

func trackingGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The page for a valid token: 200, shows the order number and its current
// status (an untracked sale shows "Not started"), carries the polling
// fragment wired to /o/{token}/status, and both the minimal-exposure
// disclosure and the app promo line. It must NOT leak operator-facing
// information: no actor name/id anywhere.
func TestOrderTracking_PageShowsStatusForValidToken(t *testing.T) {
	dp, d := setupOrderTrackingDeps(t)
	tok := seedTrackedSale(t, d, "sale-1", "R-0001")

	mux := http.NewServeMux()
	registerOrderTracking(mux, dp)

	rec := trackingGet(t, mux, "/o/"+tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"R-0001",
		"Not started", // orders.status.none — tracking never touched this sale yet
		`hx-get="/o/` + tok + `/status"`,
		`hx-trigger="every 15s"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q:\n%s", want, body)
		}
	}
	// Disclosure is i18n'd; pin its en.json copy loosely. (Independent
	// review, ut-docs#527: an earlier draft also shipped an "install the
	// app" promo line here — dropped, since the Universal Till Android app
	// IS the till itself (android/README.md), not an order-tracking
	// companion, so the line was factually wrong about what installing it
	// gets a customer.)
	if !strings.Contains(body, "status") {
		t.Errorf("page should carry the status-only disclosure line: %s", body)
	}

	// Status advances → the page (and fragment) shows the new status, still
	// with NO who-changed-it attribution (that's operator-facing).
	setTrackedStatus(t, d, "R-0001", "preparing", time.Now())
	rec = trackingGet(t, mux, "/o/"+tok)
	if !strings.Contains(rec.Body.String(), "Preparing") {
		t.Fatalf("page should show Preparing after the status write: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "u-test") {
		t.Fatalf("anonymous tracking page must never show the actor: %s", rec.Body.String())
	}
}

func TestOrderTracking_UnknownTokenNotFound(t *testing.T) {
	dp, d := setupOrderTrackingDeps(t)
	seedTrackedSale(t, d, "sale-1", "R-0001")

	mux := http.NewServeMux()
	registerOrderTracking(mux, dp)

	rec := trackingGet(t, mux, "/o/deadbeefdeadbeefdeadbeefdeadbeef")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for an unknown token, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "expired") {
		t.Errorf("not-found page should show the friendly expired-link copy: %s", body)
	}
	if strings.Contains(body, "R-0001") {
		t.Errorf("not-found page must not leak any order data: %s", body)
	}
}

// Token freshness (no cron, no extra writes): once the order is terminal
// (collected/cancelled) AND its status timestamp is more than 2 hours old,
// the token behaves exactly like an unknown one.
func TestOrderTracking_TerminalPastCutoffExpires(t *testing.T) {
	dp, d := setupOrderTrackingDeps(t)
	tok := seedTrackedSale(t, d, "sale-1", "R-0001")
	setTrackedStatus(t, d, "R-0001", "collected", time.Now().Add(-3*time.Hour))

	mux := http.NewServeMux()
	registerOrderTracking(mux, dp)

	if rec := trackingGet(t, mux, "/o/"+tok); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for a 3h-old collected order, got %d: %s", rec.Code, rec.Body.String())
	}
	// The fragment endpoint renders the expired state as a 200 swap (htmx
	// only swaps 2xx) WITHOUT the polling trigger, so an open page stops
	// polling instead of erroring forever.
	rec := trackingGet(t, mux, "/o/"+tok+"/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment: want 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hx-trigger") {
		t.Fatalf("expired fragment must not re-arm polling: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("expired fragment should show the expired-link copy: %s", rec.Body.String())
	}
}

// The status fragment re-arms its own 15s poll while the order is live and
// drops the trigger once the status is terminal but still fresh (the
// customer sees the final state; polling just stops).
func TestOrderTracking_FragmentPollsUntilTerminal(t *testing.T) {
	dp, d := setupOrderTrackingDeps(t)
	tok := seedTrackedSale(t, d, "sale-1", "R-0001")
	setTrackedStatus(t, d, "R-0001", "ready", time.Now())

	mux := http.NewServeMux()
	registerOrderTracking(mux, dp)

	rec := trackingGet(t, mux, "/o/"+tok+"/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Ready") || !strings.Contains(body, `hx-trigger="every 15s"`) {
		t.Fatalf("live fragment should show Ready and re-arm the poll: %s", body)
	}

	setTrackedStatus(t, d, "R-0001", "collected", time.Now())
	rec = trackingGet(t, mux, "/o/"+tok+"/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, "Collected") {
		t.Fatalf("terminal fragment should show the final status: %s", body)
	}
	if strings.Contains(body, "hx-trigger") {
		t.Fatalf("terminal fragment must stop polling: %s", body)
	}
}

// End-to-end through the real auth middleware: a customer's phone has no
// session, so /o/{token} must be reachable anonymously while the
// operator-facing /orders page stays gated (redirected to /login).
func TestOrderTracking_ReachableWithoutSession(t *testing.T) {
	dp, d := setupOrderTrackingDeps(t)
	tok := seedTrackedSale(t, d, "sale-1", "R-0001")

	mux := http.NewServeMux()
	registerOrderTracking(mux, dp)
	registerOrderStatus(mux, dp)
	h := auth.Middleware(mux, auth.NewService(d.DB))

	if rec := trackingGet(t, h, "/o/"+tok); rec.Code != http.StatusOK {
		t.Fatalf("anonymous GET /o/{token}: want 200, got %d", rec.Code)
	}
	if rec := trackingGet(t, h, "/o/"+tok+"/status"); rec.Code != http.StatusOK {
		t.Fatalf("anonymous GET /o/{token}/status: want 200, got %d", rec.Code)
	}
	if rec := trackingGet(t, h, "/orders"); rec.Code != http.StatusSeeOther {
		t.Fatalf("control: /orders without a session should redirect to login, got %d", rec.Code)
	}
}
