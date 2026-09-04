package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// registerSyncAdmin's GET /api/sync/admin dumps every adminTable
// (internal/data/sync_admin_repo.go) — some of those tables (related_items,
// shortcut_buttons, translation_overrides) don't exist in the simplified
// seedForPages schema used elsewhere in this package, so these tests use a
// REAL, fully migrated database (like internal/data/sync_admin_repo_test.go
// does) rather than openPagesTestDB+seedForPages.
func newMigratedSyncDeps(t *testing.T, name string) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		BaseMenu: []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
}

// withinLast already has coverage in sync_api_test.go (TestWithinLast).

// --- registerSyncAdmin: GET /api/sync/admin ---

func TestSyncAdminAPI_RejectsUnauthorized(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/admin", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no bearer, got %d", rec.Code)
	}
}

func TestSyncAdminAPI_FullBundleThenUnchangedOnMatchingFingerprint(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	if _, err := data.NewCatalogRepo(dp.Db).CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Admin Sync Widget", BasePrice: 500, IsActive: true,
	}); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	get := func(query string) (int, adminBundleResponse) {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/admin"+query, nil)
		req.Header.Set("Authorization", "Bearer token-abc")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out struct {
			Data adminBundleResponse `json:"data"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
			}
		}
		return rec.Code, out.Data
	}

	code, first := get("")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if first.Unchanged {
		t.Fatalf("expected a full bundle on first fetch (no ?have=), got Unchanged=true")
	}
	items, ok := first.Bundle.Tables["items"]
	if !ok || len(items) == 0 {
		t.Fatalf("expected the seeded item in the dumped bundle's items table, got %+v", first.Bundle.Tables["items"])
	}

	code, second := get("?have=" + first.Version)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on the matching-fingerprint poll, got %d", code)
	}
	if !second.Unchanged {
		t.Fatalf("expected Unchanged=true when ?have= matches the current fingerprint")
	}
	if len(second.Bundle.Tables) != 0 {
		t.Fatalf("expected no bundle payload on an unchanged poll, got %+v", second.Bundle.Tables)
	}
}

// --- registerSyncAdmin: GET /ui/sync-chip ---

func TestSyncChip_ReplicaMode(t *testing.T) {
	dp := newMigratedSyncDeps(t, "replica.db")
	initPagesI18n(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}
	if err := dp.Settings.Set(ctx, "sync.till_name", "Front Till"); err != nil {
		t.Fatalf("set till_name: %v", err)
	}

	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/sync-chip: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// No sync.last_contact_at ever set -> not fresh -> "warn" class + offline marker.
	if !strings.Contains(body, `sync-chip warn`) {
		t.Fatalf("expected a stale replica (no last_contact_at) to render class=warn, got %q", body)
	}
	if !strings.Contains(body, "Front Till") {
		t.Fatalf("expected the configured till_name label, got %q", body)
	}
	// ut-docs#408: the primary's own URL is cross-device data — it must
	// never leak into the chip's rendered HTML (the #390-class risk this
	// card guards against), regardless of whether a handler still passes
	// it into the template's data map.
	if strings.Contains(body, "http://primary.example") {
		t.Fatalf("replica chip must never render the primary's URL, got %q", body)
	}
	// ut-docs#405: was a bare <span>, not clickable at all.
	// ut-docs#1539: and it is a rail MENU BUTTON now, so the link carries the
	// same nav-toggle dress as every other rail item.
	if !strings.Contains(body, `<a href="/tills"`) {
		t.Fatalf("expected the replica chip to be a clickable link to the local /tills page, got %q", body)
	}
	// ut-docs#1539: the ONE rail item ut-docs#1423 missed — was a bare "⇅"
	// emoji, now the same .nav-toggle + inline-SVG-icon pattern as every
	// other rail control, with an offline badge dot instead of a trailing
	// "⚠" glyph appended to the label text.
	if strings.Contains(body, "⇅") || strings.Contains(body, "⚠") {
		t.Fatalf("expected no emoji glyph left in the rail chip, got %q", body)
	}
	if !strings.Contains(body, `class="nav-toggle"`) {
		t.Fatalf("expected the replica chip to render as a rail .nav-toggle, got %q", body)
	}
	if !strings.Contains(body, `class="nav-badge"`) {
		t.Fatalf("expected an offline badge dot on the icon for a stale/offline replica, got %q", body)
	}

	if err := dp.Settings.Set(ctx, "sync.last_contact_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set last_contact_at: %v", err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `sync-chip ok`) {
		t.Fatalf("expected a fresh replica to render class=ok, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `class="nav-badge"`) {
		t.Fatalf("expected NO badge dot once the replica is fresh/online, got %q", rec.Body.String())
	}
}

func TestSyncChip_PrimaryModeNoTills(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty chip when this till is a primary with no enrolled replicas, got %q", rec.Body.String())
	}
}

func TestSyncChip_PrimaryModeWithTills(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	initPagesI18n(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("set till.name: %v", err)
	}
	tills := data.NewTillsRepo(dp.Db)
	if _, err := tills.InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}

	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	// Never authenticated yet -> last_seen_at is NULL -> stale -> warn.
	req := httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `sync-chip warn`) {
		t.Fatalf("expected an unseen enrolled till to render class=warn, got %q", rec.Body.String())
	}
	// ut-docs#1539 (independent review): the stale-roster warn path reaches
	// the badge through `{{ if or .quarantined (eq .class "warn") }}` — a
	// different branch than the quarantine-driven warn case
	// TestSyncChip_PrimaryModeWarnsAndLinksToQuarantineWhenEntriesExist
	// already covers, so it needs its own assertion.
	if !strings.Contains(rec.Body.String(), `class="nav-badge"`) {
		t.Fatalf("expected a badge dot on the icon for a stale (never-seen) enrolled till, got %q", rec.Body.String())
	}

	// TillByBearerHash bumps last_seen_at as a side effect of authenticating
	// (see syncTill / registerSyncAdmin's admin-bundle endpoint) -- simulate
	// a real replica poll to freshen it.
	if _, ok, err := tills.TillByBearerHash(ctx, hashBearer("token-abc")); err != nil || !ok {
		t.Fatalf("authenticate till: ok=%v err=%v", ok, err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `sync-chip ok`) {
		t.Fatalf("expected a just-seen enrolled till to render class=ok, got %q", rec.Body.String())
	}
	// ut-docs#405: used to render only a bare count ("1 tills"), with no
	// way to read the primary's own name at all.
	if !strings.Contains(rec.Body.String(), "Front Counter") {
		t.Fatalf("expected the primary's own name in the chip label, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<a href="/tills"`) {
		t.Fatalf("expected the primary chip to stay a clickable link to /tills, got %q", rec.Body.String())
	}
	// ut-docs#1539: the ONE rail item ut-docs#1423 missed — was a bare "⇅"
	// emoji, now the same .nav-toggle + inline-SVG-icon pattern as every
	// other rail control.
	if strings.Contains(rec.Body.String(), "⇅") || strings.Contains(rec.Body.String(), "⚠") {
		t.Fatalf("expected no emoji glyph left in the rail chip, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `class="nav-toggle"`) {
		t.Fatalf("expected the primary chip to render as a rail .nav-toggle, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `class="nav-badge"`) {
		t.Fatalf("expected NO badge dot once the enrolled till is fresh/online, got %q", rec.Body.String())
	}
	// ut-docs#1539: "1 Kassen"/"1 tills" — a single enrolled till must use
	// the singular form ("1 till<", checked against the closing tag right
	// after so "1 tills" — which also contains "1 till" as a substring —
	// can't false-pass this).
	if !strings.Contains(rec.Body.String(), "1 till<") {
		t.Fatalf("expected the singular form for a count of 1, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "1 tills") {
		t.Fatalf("expected NOT the plural form for a count of 1, got %q", rec.Body.String())
	}
}

// ut-docs#1539: a second enrolled till must use the plural form — the
// literal "1 Kassen"/"1 tills" bug report was about the count=1 case
// reading wrong, but a bare, unconditional plural key was ALSO wrong for
// English ("1 tills"), so the fix (real one/other keys) needs coverage on
// both sides of the boundary, not just the count=1 case.
func TestSyncChip_PrimaryModeWithMultipleTills(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary-multi.db")
	initPagesI18n(t)
	ctx := t.Context()
	tills := data.NewTillsRepo(dp.Db)
	if _, err := tills.InsertTill(ctx, "Replica 1", hashBearer("token-1")); err != nil {
		t.Fatalf("enrol till 1: %v", err)
	}
	if _, err := tills.InsertTill(ctx, "Replica 2", hashBearer("token-2")); err != nil {
		t.Fatalf("enrol till 2: %v", err)
	}

	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "2 tills") {
		t.Fatalf("expected the plural form for a count of 2, got %q", body)
	}
}

// ut-docs#1133: a quarantined LAN-sync journal entry is otherwise invisible
// to both the replica's own sync chip (which correctly shows fully-synced)
// and the primary's sale counts — the primary-side nav chip is the one
// standing, always-rendered surface this fix adds a signal to. A healthy,
// just-seen till still renders "ok" even with a quarantine present: the
// warn class must come from the quarantine count, not merely coexist with
// the pre-existing staleness check.
func TestSyncChip_PrimaryModeWarnsAndLinksToQuarantineWhenEntriesExist(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary-quarantine.db")
	initPagesI18n(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("set till.name: %v", err)
	}
	tills := data.NewTillsRepo(dp.Db)
	tillID, err := tills.InsertTill(ctx, "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	// Freshen last_seen_at so staleness alone would render "ok" -- isolating
	// the assertion below to the quarantine-driven warn path.
	if _, ok, err := tills.TillByBearerHash(ctx, hashBearer("token-abc")); err != nil || !ok {
		t.Fatalf("authenticate till: ok=%v err=%v", ok, err)
	}
	if err := data.NewPOSRepo(dp.Db).InsertJournalQuarantine(ctx, data.JournalQuarantineEntry{
		TillID: tillID, SaleID: "sale-q1", ReceiptNo: "T2-Q001",
		Reason: "unknown voucher on redemption replay", PayloadJSON: `{}`,
		QuarantinedAt: "2026-08-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("seed quarantine entry: %v", err)
	}

	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `sync-chip warn`) {
		t.Fatalf("expected a nonzero quarantine count to force class=warn even with a fresh till, got %q", body)
	}
	if !strings.Contains(body, `<a href="/sync-quarantine"`) {
		t.Fatalf("expected the chip to link to /sync-quarantine when quarantined entries exist, got %q", body)
	}
	if !strings.Contains(body, `class="nav-badge"`) {
		t.Fatalf("expected a badge dot on the icon when quarantined entries exist, got %q", body)
	}
}

// StartSyncPull must register its goroutine on the caller's wg (ut-docs#153),
// same join shape as cloudsync.Start, so app.Run's shutdown drain can prove
// it exited before database.Close() runs. This is a pure lifecycle test —
// cancel happens well before the 30s ticker fires, so refresh/syncPullTick
// are never exercised here (that's syncPullTick's own tests' job).
func TestStartSyncPull_JoinsWaitGroupAndExitsOnCtxCancel(t *testing.T) {
	dp := newMigratedSyncDeps(t, "replica-join.db")
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartSyncPull(ctx, dp, func(context.Context) {}, &wg)

	// wg.Wait() on a zero counter returns immediately, so without this
	// pre-cancel check the test would pass even if StartSyncPull never
	// called wg.Add at all. Confirm the counter is genuinely non-zero
	// before cancelling.
	registered := make(chan struct{})
	go func() { wg.Wait(); close(registered) }()
	select {
	case <-registered:
		t.Fatal("wg.Wait() returned before ctx was even cancelled — StartSyncPull never called wg.Add, so this test cannot prove the join")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartSyncPull's goroutine did not call wg.Done() within 2s of ctx cancel — not joined to the shutdown drain")
	}
}

// --- syncPullTick ---

func TestSyncPullTick_NoPrimaryConfigured_NoOp(t *testing.T) {
	dp := newMigratedSyncDeps(t, "replica.db")
	ctx := t.Context()
	// A client pointed at nothing reachable -- if syncPullTick ever tried to
	// dial out despite missing sync.primary_url/sync.bearer, this transport
	// would surface it as a hang/timeout rather than silently "working".
	client := &http.Client{Timeout: 1 * time.Second, Transport: failingTransport{}}
	refreshed := false
	syncPullTick(ctx, dp, client, func(context.Context) { refreshed = true })
	if refreshed {
		t.Fatalf("expected no refresh when no primary is configured")
	}
	if v, _, _ := dp.Settings.Get(ctx, "sync.last_contact_at"); v != "" {
		t.Fatalf("expected sync.last_contact_at untouched, got %q", v)
	}
}

// failingTransport errors on any request -- used to prove a code path never
// attempts an HTTP call at all.
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errFailingTransport
}

var errFailingTransport = errors.New("failingTransport: no request should have been made")

// --- syncPullTick against a real primary server ---

type pullTestPrimary struct {
	dp      *common.Deps
	server  *httptest.Server
	visited []string
}

func newPullTestPrimary(t *testing.T) *pullTestPrimary {
	t.Helper()
	dp := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	p := &pullTestPrimary{dp: dp}
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)
	wrapped := http.NewServeMux()
	wrapped.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p.visited = append(p.visited, r.URL.Path)
		mux.ServeHTTP(w, r)
	})
	p.server = httptest.NewServer(wrapped)
	t.Cleanup(p.server.Close)
	return p
}

func (p *pullTestPrimary) hitStock() bool {
	for _, v := range p.visited {
		if v == "/api/sync/stock" {
			return true
		}
	}
	return false
}

func newPullTestReplica(t *testing.T, primaryURL string) *common.Deps {
	t.Helper()
	dp := newMigratedSyncDeps(t, "replica.db")
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", primaryURL); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.bearer", "token-abc"); err != nil {
		t.Fatal(err)
	}
	return dp
}

func TestSyncPullTick_ChangedBundleAppliesAndUpdatesCursorState(t *testing.T) {
	primary := newPullTestPrimary(t)
	ctx := t.Context()
	if _, err := data.NewCatalogRepo(primary.dp.Db).CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Pulled Widget", BasePrice: 750, IsActive: true,
	}); err != nil {
		t.Fatalf("seed primary item: %v", err)
	}

	replica := newPullTestReplica(t, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}
	refreshCalls := 0

	syncPullTick(ctx, replica, client, func(context.Context) { refreshCalls++ })

	if refreshCalls != 1 {
		t.Fatalf("expected refresh called exactly once after applying a changed bundle, got %d", refreshCalls)
	}
	var count int
	if err := replica.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE name = 'Pulled Widget'`).Scan(&count); err != nil {
		t.Fatalf("query replica items: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the primary's item applied to the replica, got count=%d", count)
	}

	pullVersion, _, _ := replica.Settings.Get(ctx, "sync.pull_version")
	if pullVersion == "" {
		t.Fatalf("expected sync.pull_version set after a changed-bundle pull")
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.last_pull_at"); v == "" {
		t.Fatalf("expected sync.last_pull_at set")
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.last_contact_at"); v == "" {
		t.Fatalf("expected sync.last_contact_at set")
	}
	var auditCount int
	if err := replica.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'admin_pulled'`).Scan(&auditCount); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one admin_pulled audit row, got %d", auditCount)
	}

	// A second tick with nothing changed on the primary: Unchanged=true, so
	// no re-apply and no second refresh -- only last_contact_at moves.
	syncPullTick(ctx, replica, client, func(context.Context) { refreshCalls++ })
	if refreshCalls != 1 {
		t.Fatalf("expected refresh NOT called again on an unchanged poll, got %d total calls", refreshCalls)
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.pull_version"); v != pullVersion {
		t.Fatalf("expected sync.pull_version unchanged on an unchanged poll, got %q want %q", v, pullVersion)
	}
}

// TestSyncPullTick_ApplyFailureDoesNotSetLastContactAt confirms ut-docs#807's
// fix: sync.last_contact_at (the sync chip's only freshness signal, see
// GET /ui/sync-chip above) must not be set on a tick whose ApplyAdmin call
// fails, or the chip lies "healthy" while sync.pull_version never advances.
// The failing bundle here (two items sharing a SKU) is deliberately
// unrelated to the plugin_settings dedupe fix (also ut-docs#807, tested in
// internal/data) so this test exercises the ordering fix in isolation, not
// a scenario the dedupe itself would already have prevented.
func TestSyncPullTick_ApplyFailureDoesNotSetLastContactAt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sync/admin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": adminBundleResponse{
				Version:   "bad-bundle-v1",
				Unchanged: false,
				Bundle: data.AdminBundle{Tables: map[string][]map[string]any{
					"items": {
						{"id": "item-a", "sku": "DUP-SKU", "name": "A", "base_price": 100},
						{"id": "item-b", "sku": "DUP-SKU", "name": "B", "base_price": 200},
					},
				}},
			},
			"error": nil,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	replica := newPullTestReplica(t, server.URL)
	ctx := t.Context()
	client := &http.Client{Timeout: 5 * time.Second}
	refreshed := false

	syncPullTick(ctx, replica, client, func(context.Context) { refreshed = true })

	if refreshed {
		t.Fatal("expected no refresh when ApplyAdmin fails")
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.last_contact_at"); v != "" {
		t.Fatalf("expected sync.last_contact_at untouched after a failed apply, got %q", v)
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.pull_version"); v != "" {
		t.Fatalf("expected sync.pull_version untouched after a failed apply, got %q", v)
	}
	var n int
	if err := replica.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("query replica items: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected the failed apply to leave no items behind (rolled back), got %d", n)
	}
}

func TestSyncPullTick_StockCorrections_SkippedWhilePushQueuePending(t *testing.T) {
	primary := newPullTestPrimary(t)
	ctx := t.Context()
	replica := newPullTestReplica(t, primary.server.URL)
	// A local (till_id='') completed sale past the (empty) push cursor means
	// this till's own journal isn't fully pushed yet -- the stock pull must
	// wait so it doesn't briefly re-add stock the primary hasn't counted.
	if _, err := replica.Db.ExecContext(ctx, `
INSERT INTO sales(id, receipt_no, status, sale_type, till_id, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('local-sale-1', 'R-LOCAL-1', 'completed', 'sale', '', 'GBP', 100, 0, 20, 120, '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`); err != nil {
		t.Fatalf("seed local sale: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	syncPullTick(ctx, replica, client, func(context.Context) {})

	if primary.hitStock() {
		t.Fatalf("expected /api/sync/stock NOT to be polled while a local sale is still queued to push")
	}
}

func TestSyncPullTick_StockCorrectionsRunWhenQueueEmpty(t *testing.T) {
	primary := newPullTestPrimary(t)
	ctx := t.Context()
	replica := newPullTestReplica(t, primary.server.URL)
	// No local sales queued -> the push queue is empty -> the stock poll runs.

	client := &http.Client{Timeout: 5 * time.Second}
	syncPullTick(ctx, replica, client, func(context.Context) {})

	if !primary.hitStock() {
		t.Fatalf("expected /api/sync/stock to be polled once the push queue is empty")
	}
	if v, _, _ := replica.Settings.Get(ctx, "sync.stock_version"); v == "" {
		t.Fatalf("expected sync.stock_version set after a stock poll")
	}
}
