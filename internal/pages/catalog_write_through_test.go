package pages

import (
	"bytes"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2817 end to end: an additional till (its own database, every
// catalogue screen's real handlers) following a REAL main till
// (newSyncCatalogMain: the same handlers plus POST /api/sync/catalog/apply)
// behind an httptest server. A catalogue save on the additional till lands
// on the main till, is audited there with the additional till's name, and
// is relayed back; nothing is written on the additional till itself — the
// main till's link nudge brings the change back with the next pull.

const (
	catalogUnreachableEN = "Can't reach the main till — change the catalog when the main till is reachable again."
	catalogConflictEN    = "This was changed on another till since you opened it"
	catalogPhotoEN       = "Photos can only be added or replaced on the main till."
)

var (
	catalogMgr     = auth.User{ID: "m-1", Username: "mia", DisplayName: "Mia", Role: "manager"}
	catalogCashier = auth.User{ID: "c-1", Username: "cara", DisplayName: "Cara", Role: "cashier"}
)

type catalogWriteThroughPair struct {
	main    *syncCatalogMain
	replica *syncCatalogMain
	calls   *atomic.Int64
}

func newCatalogWriteThroughPair(t *testing.T) catalogWriteThroughPair {
	t.Helper()
	main := newSyncCatalogMain(t)
	calls := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		main.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	replica := newCatalogTill(t)
	setReplicaSettings(t, replica.dp.Settings, srv.URL, syncCatalogBearer)
	return catalogWriteThroughPair{main: main, replica: replica, calls: calls}
}

func newCatalogReplicaOf(t *testing.T, mainURL string) *syncCatalogMain {
	t.Helper()
	replica := newCatalogTill(t)
	setReplicaSettings(t, replica.dp.Settings, mainURL, syncCatalogBearer)
	return replica
}

func catalogCategoryName(t *testing.T, dp *common.Deps, id string) string {
	t.Helper()
	var name string
	if err := dp.Db.QueryRow(`SELECT name FROM categories WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("read category %s: %v", id, err)
	}
	return name
}

// A manager on the additional till changes an item's price: the main till
// holds it, audits it naming the additional till and the actor, and the
// operator gets the main till's own row fragment back.
func TestCatalogWriteThrough_ManagerPriceChangeLandsOnMain(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	rec := postFormHtmx(p.replica.mux, "/api/catalog/item/update", itemUpdateForm("Flat White", "395"), &catalogMgr)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "catalog-row-itm1") {
		t.Fatalf("replica save = %d %q, want the main till's row fragment", rec.Code, rec.Body.String())
	}
	if p.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", p.calls.Load())
	}
	if _, price, _ := syncCatalogItem(t, p.main.dp, "itm1"); price != 395 {
		t.Fatalf("main till price = %d, want 395 — the change must land on the main till", price)
	}
	if _, price, stamp := syncCatalogItem(t, p.replica.dp, "itm1"); price != 320 || stamp != "2026-01-01 08:00:00" {
		t.Fatalf("replica wrote locally: price %d stamp %q — only the next pull may change it", price, stamp)
	}
	assertCatalogSyncAudit(t, p.main.dp, "m-1", data.CatalogKindItem, "itm1", "Till 2")
}

// A cashier (no catalog_management) is refused on the additional till
// itself, exactly as before — the main till is never asked.
func TestCatalogWriteThrough_CashierRefusedLocally(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	rec := postFormHtmx(p.replica.mux, "/api/catalog/item/update", itemUpdateForm("Flat White", "1"), &catalogCashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier save = %d %q, want 403", rec.Code, rec.Body.String())
	}
	rec = postFormHtmx(p.replica.mux, "/api/categories/cat1", url.Values{"name": {"X"}}, &catalogCashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier category save = %d, want 403", rec.Code)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("a locally refused change reached the main till (%d calls)", p.calls.Load())
	}
	if _, price, _ := syncCatalogItem(t, p.main.dp, "itm1"); price != 320 {
		t.Fatalf("main till changed: %d", price)
	}
}

// Main till unreachable: refused with "change it when the main till is
// reachable", and neither database changes.
func TestCatalogWriteThrough_MainUnreachableRefusesWithoutLocalWrite(t *testing.T) {
	replica := newCatalogReplicaOf(t, deadPrimaryURL())
	rec := postFormHtmx(replica.mux, "/api/catalog/item/update", itemUpdateForm("Flat White", "395"), &catalogMgr)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), catalogUnreachableEN) {
		t.Fatalf("unreachable = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}
	if _, price, _ := syncCatalogItem(t, replica.dp, "itm1"); price != 320 {
		t.Fatalf("refused change wrote locally: price %d", price)
	}
	rec = postFormHtmx(replica.mux, "/api/categories/cat1", url.Values{"name": {"Hot drinks"}}, &catalogMgr)
	// The category dialog's error fragment is HTML: the message arrives
	// escaped.
	if !strings.Contains(rec.Body.String(), html.EscapeString(catalogUnreachableEN)) || catalogCategoryName(t, replica.dp, "cat1") != "Drinks" {
		t.Fatalf("category save while unreachable = %d %q", rec.Code, rec.Body.String())
	}
	if n := countCatalogSyncAudits(t, replica.dp); n != 0 {
		t.Fatalf("refused change audited locally (%d)", n)
	}
}

// The item changed on the main till after the additional till's copy was
// pulled: the save is refused with the reload message and the main till's
// newer value survives.
func TestCatalogWriteThrough_ConflictKeepsMainTillsNewerChange(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	// Someone changed the price on the main till; the additional till has
	// not pulled it yet (its copy still carries the old stamp).
	if _, err := p.main.dp.Db.Exec(`UPDATE items SET base_price = 350, updated_at = '2026-01-01 09:00:00' WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	rec := postFormHtmx(p.replica.mux, "/api/catalog/item/update", itemUpdateForm("Flat White", "395"), &catalogMgr)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), catalogConflictEN) {
		t.Fatalf("conflict = %d %q, want 409 with the reload message", rec.Code, rec.Body.String())
	}
	if _, price, _ := syncCatalogItem(t, p.main.dp, "itm1"); price != 350 {
		t.Fatalf("main till price = %d, want 350 — a stale save must not overwrite it", price)
	}
	if _, price, _ := syncCatalogItem(t, p.replica.dp, "itm1"); price != 320 {
		t.Fatalf("replica wrote locally: %d", price)
	}
}

// Categories (the /categories dialog and the Designer), quick-sale buttons
// and the category reorder all travel the same way.
func TestCatalogWriteThrough_CategoriesButtonsAndDesigner(t *testing.T) {
	p := newCatalogWriteThroughPair(t)

	rec := postFormHtmx(p.replica.mux, "/api/categories/cat1", url.Values{"name": {"Hot drinks"}}, &catalogMgr)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/categories" {
		t.Fatalf("category save = %d %q (HX-Redirect %q)", rec.Code, rec.Body.String(), rec.Header().Get("HX-Redirect"))
	}
	if got := catalogCategoryName(t, p.main.dp, "cat1"); got != "Hot drinks" {
		t.Fatalf("main till category = %q", got)
	}
	if got := catalogCategoryName(t, p.replica.dp, "cat1"); got != "Drinks" {
		t.Fatalf("replica wrote the category locally: %q", got)
	}

	rec = postForm(p.replica.mux, "/api/designer/categories", url.Values{"name": {"Pastries"}}, &catalogMgr)
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Trigger") != "buttons-changed" {
		t.Fatalf("designer create = %d %q", rec.Code, rec.Body.String())
	}
	var n int
	if err := p.main.dp.Db.QueryRow(`SELECT count(*) FROM categories WHERE name = 'Pastries'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("main till Pastries count = %d (%v)", n, err)
	}
	if err := p.replica.dp.Db.QueryRow(`SELECT count(*) FROM categories WHERE name = 'Pastries'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("replica created the category locally (%d)", n)
	}

	rec = postForm(p.replica.mux, "/api/categories/reorder", url.Values{"ids": {"cat1"}}, &catalogMgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("category reorder = %d %q", rec.Code, rec.Body.String())
	}

	rec = postFormHtmx(p.replica.mux, "/api/buttons/hide", url.Values{"itemId": {"itm1"}}, &catalogMgr)
	if rec.Code >= 300 {
		t.Fatalf("button hide = %d %q", rec.Code, rec.Body.String())
	}
	var hidden int
	if err := p.main.dp.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id = 'itm1'`).Scan(&hidden); err != nil || hidden != 1 {
		t.Fatalf("main till sell_screen_hidden = %d (%v)", hidden, err)
	}
	if err := p.replica.dp.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id = 'itm1'`).Scan(&hidden); err != nil || hidden != 0 {
		t.Fatalf("replica hid the item locally (%d)", hidden)
	}
	assertCatalogSyncAudit(t, p.main.dp, "m-1", data.CatalogKindItem, "itm1", "Till 2")
}

// A cashier with a manager's PIN: the elevation is decided on the
// additional till (as before), the approver travels and the main till
// re-checks the approver's role; the audit row records both.
func TestCatalogWriteThrough_ElevatedButtonChangeCarriesApprover(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	rec := postFormHtmx(p.replica.mux, "/api/buttons/hide", url.Values{"itemId": {"itm1"}, "override_pin": {"7391"}}, &catalogCashier)
	if rec.Code >= 300 {
		t.Fatalf("elevated hide = %d %q", rec.Code, rec.Body.String())
	}
	var hidden int
	if err := p.main.dp.Db.QueryRow(`SELECT sell_screen_hidden FROM items WHERE id = 'itm1'`).Scan(&hidden); err != nil || hidden != 1 {
		t.Fatalf("main till sell_screen_hidden = %d (%v)", hidden, err)
	}
	var actor, approver string
	if err := p.main.dp.Db.QueryRow(`SELECT COALESCE(blocked_actor_id, ''), actor_id FROM audit_log WHERE action = 'catalog_changed_via_till'`).Scan(&actor, &approver); err != nil {
		t.Fatalf("read elevated audit: %v", err)
	}
	if actor != "c-1" || approver != "m-1" {
		t.Fatalf("audit actor/approver = %q/%q, want c-1 elevated by m-1", actor, approver)
	}
	// Without a PIN the cashier gets the elevation prompt locally; the main
	// till is not asked.
	before := p.calls.Load()
	rec = postFormHtmx(p.replica.mux, "/api/buttons/unhide", url.Values{"itemId": {"itm1"}}, &catalogCashier)
	if !isElevationPrompt(rec) || p.calls.Load() != before {
		t.Fatalf("unelevated cashier: prompt=%v calls %d -> %d", isElevationPrompt(rec), before, p.calls.Load())
	}
}

// A category photo upload never travels (photos go through the asset
// ledger); the additional till refuses it without asking the main till.
func TestCatalogWriteThrough_CategoryPhotoUploadStaysRefused(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", "Drinks")
	fw, _ := mw.CreateFormFile("image", "photo.png")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\nnot-really-a-png"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/categories/cat1", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	req = auth.WithUser(req, catalogMgr)
	rec := httptest.NewRecorder()
	p.replica.mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), catalogPhotoEN) {
		t.Fatalf("photo upload on replica = %d %q, want the photos-on-main message", rec.Code, rec.Body.String())
	}
	if p.calls.Load() != 0 {
		t.Fatalf("a photo upload reached the main till (%d calls)", p.calls.Load())
	}
	// The same dialog without a file (name, icon) travels.
	rec = postFormHtmx(p.replica.mux, "/api/categories/cat1", url.Values{"name": {"Cold drinks"}}, &catalogMgr)
	if rec.Code != http.StatusOK || catalogCategoryName(t, p.main.dp, "cat1") != "Cold drinks" {
		t.Fatalf("file-less category save = %d %q", rec.Code, rec.Body.String())
	}
}

// The case the editor-carried stamp exists for: the main till changed the
// item, and the link's pull has ALREADY brought that change to this till
// (its copy now carries the main till's new stamp), but the dialog the
// operator is saving from was opened before — its base_updated_at is the
// old stamp. The save is still a conflict.
func TestCatalogWriteThrough_ConflictFromTheEditorsLoadedStampAfterAPull(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	for _, dp := range []*common.Deps{p.main.dp, p.replica.dp} {
		if _, err := dp.Db.Exec(`UPDATE items SET base_price = 350, updated_at = '2026-01-01 09:00:00' WHERE id = 'itm1'`); err != nil {
			t.Fatal(err)
		}
	}
	form := itemUpdateForm("Flat White", "395")
	form.Set("base_updated_at", "2026-01-01 08:00:00")
	rec := postFormHtmx(p.replica.mux, "/api/catalog/item/update", form, &catalogMgr)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), catalogConflictEN) {
		t.Fatalf("stale editor save = %d %q, want 409 with the reload message", rec.Code, rec.Body.String())
	}
	if _, price, _ := syncCatalogItem(t, p.main.dp, "itm1"); price != 350 {
		t.Fatalf("main till price = %d, want 350", price)
	}
	// After a reload the dialog carries the fresh stamp and the save lands.
	form.Set("base_updated_at", "2026-01-01 09:00:00")
	if rec := postFormHtmx(p.replica.mux, "/api/catalog/item/update", form, &catalogMgr); rec.Code != http.StatusOK {
		t.Fatalf("fresh-stamp save = %d %q", rec.Code, rec.Body.String())
	}
	if _, price, _ := syncCatalogItem(t, p.main.dp, "itm1"); price != 395 {
		t.Fatalf("main till price = %d, want 395", price)
	}
}
