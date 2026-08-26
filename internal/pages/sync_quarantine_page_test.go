package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// A cashier (or no session at all) must not see quarantined sync entries; a
// manager on the primary gets a real render of the seeded entries with the
// till_id resolved to the till's enrolled name (ut-docs#1133).
func TestSyncQuarantinePage_ManagerOnlyAndRendersRealData(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	if _, err := db.Exec(`INSERT INTO tills(id, name, bearer_hash, enrolled_at) VALUES ('till-2','Front Counter','x','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	if err := data.NewPOSRepo(db).InsertJournalQuarantine(context.Background(), data.JournalQuarantineEntry{
		TillID: "till-2", SaleID: "sale-q1", ReceiptNo: "T2-Q001",
		Reason: "unknown voucher on redemption replay", PayloadJSON: `{}`,
		QuarantinedAt: "2026-08-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("seed quarantine entry: %v", err)
	}

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerSyncQuarantinePage(mux, dp)

	get := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/sync-quarantine", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("no session = %d, want 303 redirect", rec.Code)
	}
	if rec := get(&auth.User{ID: "cashier-1", Role: "cashier"}); rec.Code != http.StatusSeeOther {
		t.Fatalf("cashier = %d, want 303 redirect", rec.Code)
	}

	rec := get(&auth.User{ID: "mgr-1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("manager = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Front Counter", "T2-Q001", "unknown voucher on redemption replay"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync-quarantine page missing %q in rendered output", want)
		}
	}
}

// A replica must never render this page — quarantine is a primary-only
// concept (see registerSyncQuarantinePage's doc comment) — even for a
// manager who'd otherwise pass the permission gate.
func TestSyncQuarantinePage_ReplicaRedirectsRegardlessOfRole(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	store := settings.NewStore(db)
	if err := store.Set(context.Background(), "sync.primary_url", "https://primary.example.lan"); err != nil {
		t.Fatalf("set sync.primary_url: %v", err)
	}
	state := common.LoadState(t.Context(), store, cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: store, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerSyncQuarantinePage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/sync-quarantine", nil)
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("replica manager = %d, want 303 redirect to /settings", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/settings" {
		t.Fatalf("redirect target = %q, want /settings", loc)
	}
}
