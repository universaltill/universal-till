package pages

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// Cross-till held-sale write-through, replica side (ADR-0093, ut-docs#1920),
// proven against a REAL registerSyncHeldSales mux acting as the primary
// over its own migrated database, with a REAL replica till (registerHoldAPI
// + registerOpenOrders, also a real migrated database) pointed at it — the
// same both-ends-real shape hold_cross_till_test.go uses, so the assertions
// are the actual ADR outcomes ("an order parked on till A is listed on and
// resumable from till B") through the HTTP surface both sides use, not a
// mocked stand-in for either. The fallback cases use the same fake-primary
// shapes as tables_claim_proxy_test.go; setReplicaSettings is shared.

// newHeldSaleSyncPrimary boots a real primary: its own migrated DB, the
// held-sale trio (plus the table endpoints hold/resume also touch) mounted,
// one till enrolled under bearer. The raw DB is returned for the one test
// that must seed a `tables` row there (held_sales.table_id is an FK).
func newHeldSaleSyncPrimary(t *testing.T, bearer string) (*httptest.Server, *data.HeldSalesRepo, *sql.DB) {
	t.Helper()
	dbase, err := db.Open(filepath.Join(t.TempDir(), "primary.db"))
	if err != nil {
		t.Fatalf("open primary db: %v", err)
	}
	t.Cleanup(func() { dbase.Close() })
	dp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncTables(mux, dp)
	registerSyncTablesClaim(mux, dp)
	registerSyncHeldSales(mux, dp)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if _, err := data.NewTillsRepo(dbase.DB).InsertTill(context.Background(), "Replica", hashBearer(bearer)); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	return srv, data.NewHeldSalesRepo(dbase.DB), dbase.DB
}

// newHeldSaleSyncReplica boots a real replica till over its own migrated
// database with the hold API and the open-orders page mounted, pointed at
// primaryURL/bearer — or, with primaryURL "", left a plain standalone till
// (never a replica, never a call out).
func newHeldSaleSyncReplica(t *testing.T, primaryURL, bearer string) (*http.ServeMux, *common.Deps, *data.HeldSalesRepo) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	dbase, err := db.Open(filepath.Join(t.TempDir(), "replica.db"))
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	t.Cleanup(func() { dbase.Close() })

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	dp := &common.Deps{
		Db:       dbase.DB,
		Engine:   engine,
		State:    common.RuntimeState{Currency: "GBP", TaxRatePct: 20},
		Settings: settings.NewStore(dbase.DB),
		Menu:     []common.MenuItem{{Href: "/", Label: "nav.till"}},
	}
	if primaryURL != "" {
		setReplicaSettings(t, dp.Settings, primaryURL, bearer)
	}
	mux := http.NewServeMux()
	registerHoldAPI(mux, dp)
	registerOpenOrders(mux, dp)
	return mux, dp, data.NewHeldSalesRepo(dbase.DB)
}

func sampleHeldSale(id, label string) data.HeldSale {
	return data.HeldSale{ID: id, Label: label, TotalMinor: 1250, LineCount: 3, Payload: `{"lines":[]}`}
}

func TestHeldSaleWriteThrough_NotAReplicaIsLocalOnly(t *testing.T) {
	_, dp, repo := newHeldSaleSyncReplica(t, "", "")
	ctx := context.Background()
	// sync.primary_url unset: never a replica, the write is exactly the
	// local one.
	if err := heldSaleWriteThrough(ctx, dp, repo, sampleHeldSale("h1", "Table 4")); err != nil {
		t.Fatalf("local-only write-through: %v", err)
	}
	got, found, err := repo.Get(ctx, "h1")
	if err != nil || !found || got.Label != "Table 4" {
		t.Fatalf("the local row must be written, got %+v found=%v err=%v", got, found, err)
	}
	if got.UpdatedAt == "" {
		t.Fatal("the local write must stamp updated_at")
	}
}

func TestHeldSaleWriteThrough_ReplicaWritesToPrimaryAndMirrorsItsStamp(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	_, dp, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()

	h := sampleHeldSale("h1", "Table 4")
	h.CreatedAt = "2026-09-15 10:00:00"
	if err := heldSaleWriteThrough(ctx, dp, repo, h); err != nil {
		t.Fatalf("write-through: %v", err)
	}
	onPrimary, found, err := primaryRepo.Get(ctx, "h1")
	if err != nil || !found || onPrimary.Label != "Table 4" || onPrimary.Payload != `{"lines":[]}` {
		t.Fatalf("the primary must hold the order, got %+v found=%v err=%v", onPrimary, found, err)
	}
	if onPrimary.UpdatedAt == "" || onPrimary.CreatedAt != "2026-09-15 10:00:00" {
		t.Fatalf("the primary must stamp updated_at on its own clock and honour created_at, got %+v", onPrimary)
	}
	// Mirrored locally VERBATIM — the primary's stamp, not a second clock
	// a network hop later — so the open-orders merge can never prefer this
	// copy over a genuinely newer primary write.
	local, found, err := repo.Get(ctx, "h1")
	if err != nil || !found {
		t.Fatalf("the local mirror must exist, got found=%v err=%v", found, err)
	}
	if local.UpdatedAt != onPrimary.UpdatedAt || local.Label != onPrimary.Label {
		t.Fatalf("local mirror must carry the primary's row and stamp: local=%+v primary=%+v", local, onPrimary)
	}
}

// ANY failure reaching the primary is the local write, silently — a till
// that cannot reach the primary parks exactly as it did before this ADR.
func TestHeldSaleWriteThrough_ReplicaFallsBackToLocalOnAnyPrimaryFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	cases := []struct {
		name    string
		handler http.HandlerFunc
		url     string
	}{
		{name: "unreachable", url: deadURL},
		{name: "non-200", handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
		}},
		{name: "malformed body", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `not json`)
		}},
		{name: "null data object", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":null,"error":null}`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int64
			url := c.url
			if c.handler != nil {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					c.handler(w, r)
				}))
				defer srv.Close()
				url = srv.URL
			}
			_, dp, repo := newHeldSaleSyncReplica(t, url, "b-123")
			ctx := context.Background()
			if err := heldSaleWriteThrough(ctx, dp, repo, sampleHeldSale("h1", "Table 4")); err != nil {
				t.Fatalf("fallback must be silent, got %v", err)
			}
			got, found, err := repo.Get(ctx, "h1")
			if err != nil || !found || got.Label != "Table 4" || got.UpdatedAt == "" {
				t.Fatalf("the local row must be written exactly as today, got %+v found=%v err=%v", got, found, err)
			}
			if c.handler != nil && calls.Load() != 1 {
				t.Fatalf("the primary must actually have been attempted once, got %d", calls.Load())
			}
		})
	}
}

// The core ADR-0093 Decision 2 outcome from the replica's side: the primary
// already holds a NEWER version of this order (another till updated it),
// so this till's write is refused — not an error — and the primary's
// version is what ends up in the local row.
func TestHeldSaleWriteThrough_RefusedByPrimaryAdoptsPrimaryVersion(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	_, dp, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()
	if err := primaryRepo.Upsert(ctx, data.HeldSale{ID: "h1", Label: "Newer", TotalMinor: 900, LineCount: 2, Payload: `{"v":2}`, UpdatedAt: "2999-01-01 00:00:00"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	if err := heldSaleWriteThrough(ctx, dp, repo, sampleHeldSale("h1", "Older")); err != nil {
		t.Fatalf("a refused write must not be an error, got %v", err)
	}
	if got, _, _ := primaryRepo.Get(ctx, "h1"); got.Label != "Newer" || got.Payload != `{"v":2}` {
		t.Fatalf("the primary's newer row must be untouched, got %+v", got)
	}
	local, found, _ := repo.Get(ctx, "h1")
	if !found || local.Label != "Newer" || local.UpdatedAt != "2999-01-01 00:00:00" || local.Payload != `{"v":2}` {
		t.Fatalf("the local row must adopt the primary's version, got %+v found=%v", local, found)
	}
}

func TestHeldSaleDeleteWriteThrough_DeletesOnPrimaryAndLocally(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	_, dp, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()
	if err := primaryRepo.Insert(ctx, sampleHeldSale("h1", "Table 4")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, sampleHeldSale("h1", "Table 4")); err != nil {
		t.Fatal(err)
	}
	if err := heldSaleDeleteWriteThrough(ctx, dp, repo, "h1"); err != nil {
		t.Fatalf("delete write-through: %v", err)
	}
	if _, found, _ := primaryRepo.Get(ctx, "h1"); found {
		t.Fatal("the primary's row must be gone")
	}
	if _, found, _ := repo.Get(ctx, "h1"); found {
		t.Fatal("the local row must be gone")
	}
	// Idempotent end to end: nothing left anywhere is still not an error.
	if err := heldSaleDeleteWriteThrough(ctx, dp, repo, "h1"); err != nil {
		t.Fatalf("second delete must be a no-op, got %v", err)
	}
}

func TestHeldSaleDeleteWriteThrough_StillDeletesLocallyWhenPrimaryUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	_, dp, repo := newHeldSaleSyncReplica(t, deadURL, "b-123")
	ctx := context.Background()
	if err := repo.Insert(ctx, sampleHeldSale("h1", "Table 4")); err != nil {
		t.Fatal(err)
	}
	if err := heldSaleDeleteWriteThrough(ctx, dp, repo, "h1"); err != nil {
		t.Fatalf("local delete must proceed when the primary is unreachable, got %v", err)
	}
	if _, found, _ := repo.Get(ctx, "h1"); found {
		t.Fatal("the local row must be gone even when the primary is unreachable")
	}
}

// End to end through the real HTTP surface: parking on a replica lists the
// order on the primary; resuming it there removes it again.
func TestHoldOnReplica_ParkListsOnPrimaryAndResumeRemovesIt(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	mux, dp, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rec := posPostForm(mux, "/api/pos/hold", "label=Alice"); rec.Code != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	onPrimary, err := primaryRepo.List(ctx)
	if err != nil || len(onPrimary) != 1 || onPrimary[0].Label != "Alice" || onPrimary[0].LineCount != 1 {
		t.Fatalf("the primary must list the parked order, got %+v err=%v", onPrimary, err)
	}
	local, err := repo.List(ctx)
	if err != nil || len(local) != 1 || local[0].ID != onPrimary[0].ID {
		t.Fatalf("the local mirror must carry the same id, got %+v err=%v", local, err)
	}

	if rec := posPostForm(mux, "/api/pos/resume", "id="+local[0].ID); rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !dp.Engine.HasItems() {
		t.Fatal("resume must restore the basket")
	}
	if onPrimary, _ = primaryRepo.List(ctx); len(onPrimary) != 0 {
		t.Fatalf("the primary must no longer list a resumed order, got %+v", onPrimary)
	}
	if local, _ = repo.List(ctx); len(local) != 0 {
		t.Fatalf("the local row must be gone after resume, got %+v", local)
	}
}

// A held/table move (POST /api/pos/held/table) is a content change under
// the ordering key: the primary's copy must follow the move.
func TestHoldOnReplica_MoveTablePushesMovedRowToPrimary(t *testing.T) {
	primary, primaryRepo, primaryDB := newHeldSaleSyncPrimary(t, "b-123")
	mux, dp, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()
	// The floor plan is admin-bundle-synced (ut-docs#1546); seed the one
	// table on BOTH ends by hand rather than re-deriving that sync — the
	// replica's IsTableFree/claim path reads its local `tables`, and the
	// primary's held_sales.table_id is an FK to its own.
	for _, dbc := range []*sql.DB{dp.Db, primaryDB} {
		if _, err := dbc.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES ('tbl-2','T2','',4,'rect',0,0,1,datetime('now'),datetime('now'))`); err != nil {
			t.Fatalf("seed table: %v", err)
		}
	}
	if err := primaryRepo.Upsert(ctx, data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`, UpdatedAt: "2026-09-15 10:00:00"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := repo.Upsert(ctx, data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`, UpdatedAt: "2026-09-15 10:00:00"}); err != nil {
		t.Fatalf("seed replica: %v", err)
	}

	if rec := posPostForm(mux, "/api/pos/held/table", "id=h1&table_id=tbl-2"); rec.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	local, _, _ := repo.Get(ctx, "h1")
	if local.TableID != "tbl-2" {
		t.Fatalf("the local row must have moved, got %+v", local)
	}
	onPrimary, _, _ := primaryRepo.Get(ctx, "h1")
	if onPrimary.TableID != "tbl-2" {
		t.Fatalf("the primary's row must follow the move, got %+v", onPrimary)
	}
	if onPrimary.UpdatedAt <= "2026-09-15 10:00:00" || local.UpdatedAt != onPrimary.UpdatedAt {
		t.Fatalf("the move must advance the primary's stamp and mirror it locally: local=%q primary=%q", local.UpdatedAt, onPrimary.UpdatedAt)
	}
}

// The open-orders page merge (ADR-0093 Decision 4): a primary-only row is
// listed; a row on both sides shows whichever is newer; a local-only row
// stays; every primary winner is mirrored locally so it is resumable here.
func TestOpenOrdersOnReplica_MergesPrimaryRowsWithoutMirroringLocally(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	mux, _, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()
	seed := func(r *data.HeldSalesRepo, id, label, stamp string) {
		t.Helper()
		if err := r.Upsert(ctx, data.HeldSale{ID: id, Label: label, TotalMinor: 700, LineCount: 2, Payload: `{}`, CreatedAt: stamp, UpdatedAt: stamp}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed(primaryRepo, "p1", "From till A", "2026-09-15 09:00:00")
	seed(primaryRepo, "h1", "Shared primary newer", "2026-09-15 12:00:00")
	seed(repo, "h1", "Shared local older", "2026-09-15 11:00:00")
	seed(primaryRepo, "h3", "Shared primary older", "2026-09-15 12:00:00")
	seed(repo, "h3", "Shared local newer", "2026-09-15 13:00:00")
	seed(repo, "h2", "Only here", "2026-09-15 11:30:00")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /open-orders = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"From till A", "Shared primary newer", "Shared local newer", "Only here"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the merged page, got: %s", want, body)
		}
	}
	for _, unwanted := range []string{"Shared local older", "Shared primary older"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("the losing version %q must not render, got: %s", unwanted, body)
		}
	}
	// Age ordering: p1 (09:00) was parked before h1 (11:00 locally, but the
	// merge adopts the primary's 12:00 created_at) — the merged-in row sits
	// by age, not tacked on at the end.
	if strings.Index(body, `data-held-id="p1"`) > strings.Index(body, `data-held-id="h2"`) {
		t.Fatalf("merged rows must keep List's created_at order, got: %s", body)
	}

	// Display-only, deliberately (independent review, ut-docs#1920: an
	// earlier draft mirrored a merged-in row locally on every render, which
	// left a permanent ghost after the order was resumed and paid
	// elsewhere — see held_sale_sync_proxy.go's own comment on
	// mergeHeldSalesWithPrimary). A render must change NEITHER side.
	if _, found, _ := repo.Get(ctx, "p1"); found {
		t.Fatal("a render must never write a primary-only row into this till's own held_sales")
	}
	if got, _, _ := repo.Get(ctx, "h1"); got.Label != "Shared local older" {
		t.Fatalf("a render must never overwrite the local copy with the primary's newer version, got %+v", got)
	}
	if got, _, _ := repo.Get(ctx, "h3"); got.Label != "Shared local newer" {
		t.Fatalf("the local row must be untouched either way, got %+v", got)
	}
	// A render is read-only towards the primary too.
	if _, found, _ := primaryRepo.Get(ctx, "h2"); found {
		t.Fatal("listing must never push local rows to the primary")
	}
	if got, _, _ := primaryRepo.Get(ctx, "h3"); got.Label != "Shared primary older" {
		t.Fatalf("listing must never change the primary's row, got %+v", got)
	}
}

// TestResumeHeldSaleFallback_FetchesFromPrimaryOnLocalMiss (ut-docs#1920,
// independent review): the merge above is deliberately display-only, so
// resumability across tills is this fallback's job alone — exercised via
// the shared resumeHeldSale, not the merge, since that's the only path a
// real resume tap takes.
func TestResumeHeldSaleFallback_FetchesFromPrimaryOnLocalMiss(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	_, deps, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()
	if err := primaryRepo.Upsert(ctx, data.HeldSale{ID: "away", Label: "Parked elsewhere", TotalMinor: 500, LineCount: 1, Payload: `{"lines":[]}`, CreatedAt: "2026-09-15 09:00:00", UpdatedAt: "2026-09-15 09:00:00"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if _, found, _ := repo.Get(ctx, "away"); found {
		t.Fatal("precondition: must not already be local")
	}

	posRepo := data.NewPOSRepo(deps.Db)
	outcome := resumeHeldSale(ctx, deps, repo, posRepo, "away")
	if outcome != resumeOK {
		t.Fatalf("resume of a primary-only order = %v, want resumeOK", outcome)
	}
	if deps.Engine.HeldOrigin().ID != "away" {
		t.Fatalf("basket did not restore the primary-only order, HeldOrigin=%+v", deps.Engine.HeldOrigin())
	}
	// The on-demand fallback mirrors the row locally ONLY at this point (not
	// at any earlier render) -- and the resume's own delete write-through
	// then removes it again, same as any other resume.
	if _, found, _ := repo.Get(ctx, "away"); found {
		t.Fatal("resume deletes the row after restoring it, same as any other order")
	}
	if _, found, _ := primaryRepo.Get(ctx, "away"); found {
		t.Fatal("resume must remove the order from the primary too")
	}
}

func TestResumeHeldSaleFallback_NotFoundLocallyOrOnPrimaryStaysNotFound(t *testing.T) {
	primary, _, _ := newHeldSaleSyncPrimary(t, "b-123")
	_, deps, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	posRepo := data.NewPOSRepo(deps.Db)
	if outcome := resumeHeldSale(context.Background(), deps, repo, posRepo, "nowhere"); outcome != resumeNotFound {
		t.Fatalf("resume of an id nobody has = %v, want resumeNotFound", outcome)
	}
}

func TestOpenOrdersOnReplica_UnreachablePrimaryRendersLocalOnly(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	mux, _, repo := newHeldSaleSyncReplica(t, deadURL, "b-123")
	if err := repo.Insert(context.Background(), sampleHeldSale("h2", "Only here")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Only here") {
		t.Fatalf("an unreachable primary must render the local list exactly as today: %d %s", rec.Code, rec.Body.String())
	}
}

// The one primary row the merge must NOT list or mirror: the order that is
// currently live in this till's own basket — a stale parked copy of the
// order the cashier is ringing up right now would be an invitation to
// resume it over itself.
func TestOpenOrdersOnReplica_SkipsTheOrderLiveInThisBasket(t *testing.T) {
	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	mux, dp, repo := newHeldSaleSyncReplica(t, primary.URL, "b-123")
	ctx := context.Background()
	if err := primaryRepo.Upsert(ctx, data.HeldSale{ID: "hold-live", Label: "Live right here", Payload: `{}`, UpdatedAt: "2026-09-15 10:00:00"}); err != nil {
		t.Fatal(err)
	}
	if err := primaryRepo.Upsert(ctx, data.HeldSale{ID: "p1", Label: "From till A", Payload: `{}`, UpdatedAt: "2026-09-15 10:00:00"}); err != nil {
		t.Fatal(err)
	}
	dp.Engine.RestoreHeld(pos.BasketSnapshot{}, pos.HeldOrigin{ID: "hold-live", Label: "Live right here", CreatedAt: "2026-09-15 09:00:00"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open-orders", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /open-orders = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Live right here") {
		t.Fatalf("the order live in this basket must not be listed as parked, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "From till A") {
		t.Fatalf("other primary rows must still merge in, got: %s", rec.Body.String())
	}
	if _, found, _ := repo.Get(ctx, "hold-live"); found {
		t.Fatal("the live order's primary row must not be mirrored locally")
	}
}

// Direct unit coverage of the read proxy's ok contract, without a page in
// the way — mirrors TestClaimTableOnPrimary_Contract.
func TestFetchHeldSalesFromPrimary_Contract(t *testing.T) {
	_, dp, _ := newHeldSaleSyncReplica(t, "", "")
	ctx := context.Background()
	if _, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient); ok {
		t.Fatal("not a replica must report ok=false")
	}

	primary, primaryRepo, _ := newHeldSaleSyncPrimary(t, "b-123")
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")
	rows, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient)
	if !ok || len(rows) != 0 {
		t.Fatalf("an empty primary must report ok=true with no rows, got ok=%v rows=%+v", ok, rows)
	}
	if err := primaryRepo.Insert(ctx, sampleHeldSale("h1", "Table 4")); err != nil {
		t.Fatal(err)
	}
	rows, ok = fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient)
	if !ok || len(rows) != 1 || rows[0].ID != "h1" || rows[0].UpdatedAt == "" {
		t.Fatalf("a reachable primary must report its rows with stamps, got ok=%v rows=%+v", ok, rows)
	}

	// A 200 whose data is null is a failure, not "no orders".
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":null,"error":null}`)
	}))
	defer bad.Close()
	setReplicaSettings(t, dp.Settings, bad.URL, "b-123")
	if _, ok := fetchHeldSalesFromPrimary(ctx, dp, heldSaleProxyClient); ok {
		t.Fatal("a 200 with a null data array must report ok=false")
	}
}
