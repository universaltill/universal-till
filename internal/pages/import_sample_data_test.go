package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3709: the setup wizard no longer offers sample data or a restore
// step; /import is where a new shop picks how to start. These tests pin the
// import page's welcome mode, its Sample data card and the new
// POST /api/import/sample-data endpoint.

// newSampleDataDeps is newRealDBDeps (real migrated schema, real role
// permissions) with the import page registered on the same mux, plus a real
// manager row (audit_log.actor_id is a real FK to users on a migrated DB).
func newSampleDataDeps(t *testing.T) (*http.ServeMux, *data.DemoSeedRepo, func() int) {
	t.Helper()
	mux, d := newRealDBDeps(t)
	registerImport(mux, d)
	if _, err := d.Db.Exec(`INSERT INTO users (id, username, display_name, role) VALUES ('m1', 'mgr-sample', 'Mgr', 'manager')`); err != nil {
		t.Fatalf("seed manager row: %v", err)
	}
	seedRepo := data.NewDemoSeedRepo(d.Db)
	auditRows := func() int {
		var n int
		if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'sample_data_loaded'`).Scan(&n); err != nil {
			t.Fatalf("count audit rows: %v", err)
		}
		return n
	}
	return mux, seedRepo, auditRows
}

func getImportAs(mux *http.ServeMux, path string, user auth.User, fragment bool) *httptest.ResponseRecorder {
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), user)
	if fragment {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestImportSampleData_CashierForbidden(t *testing.T) {
	mux, seedRepo, auditRows := newSampleDataDeps(t)

	rec := postForm(mux, "/api/import/sample-data", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier sample-data: code=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if n, _ := seedRepo.SampleItemCount(t.Context()); n != 0 {
		t.Fatalf("cashier attempt seeded %d sample items", n)
	}
	if n := auditRows(); n != 0 {
		t.Fatalf("cashier attempt wrote %d audit rows", n)
	}
}

func TestImportSampleData_SeedsOnceAndIsIdempotent(t *testing.T) {
	mux, seedRepo, auditRows := newSampleDataDeps(t)

	rec := postForm(mux, "/api/import/sample-data", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("first load: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`data-sample-loaded="1"`, `href="/items"`, `href="/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("first load response missing %s: %s", want, body)
		}
	}
	items, err := seedRepo.SampleItemCount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if items != 52 {
		t.Fatalf("sample items after load = %d, want 52", items)
	}
	custPromo, err := seedRepo.SampleCustomerPromoCount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if custPromo != 6 {
		t.Fatalf("sample customers/promos after load = %d, want 6", custPromo)
	}
	if n := auditRows(); n != 1 {
		t.Fatalf("audit rows after first load = %d, want 1", n)
	}

	// Second call: no duplicate seed, says it is already loaded.
	rec = postForm(mux, "/api/import/sample-data", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("second load: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-sample-already="1"`) {
		t.Errorf("second load did not say the sample data was already loaded: %s", rec.Body.String())
	}
	if n, _ := seedRepo.SampleItemCount(t.Context()); n != items {
		t.Fatalf("sample items after second load = %d, want unchanged %d", n, items)
	}
	if n, _ := seedRepo.SampleCustomerPromoCount(t.Context()); n != custPromo {
		t.Fatalf("sample customers/promos after second load = %d, want unchanged %d", n, custPromo)
	}
	if n := auditRows(); n != 1 {
		t.Fatalf("audit rows after second load = %d, want still 1", n)
	}
}

// ut-docs#3709 review S3: the three seed steps are separate transactions.
// If the catalogue landed but customers/promotions did not, a retry must
// seed the missing half — not answer "already loaded" forever because
// sample items exist.
func TestImportSampleData_RetryAfterPartialLoadSeedsTheRest(t *testing.T) {
	mux, seedRepo, auditRows := newSampleDataDeps(t)
	if err := seedRepo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatal(err)
	}

	// GET /import still offers the button: the load is incomplete.
	if body := getImportAs(mux, "/import", mgrUser, false).Body.String(); !strings.Contains(body, `hx-post="/api/import/sample-data"`) {
		t.Error("half-loaded sample data: /import does not offer Load sample data again")
	}

	rec := postForm(mux, "/api/import/sample-data", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-sample-loaded="1"`) {
		t.Fatalf("retry after a partial load said %s, want data-sample-loaded", rec.Body.String())
	}
	if n, _ := seedRepo.SampleCustomerPromoCount(t.Context()); n != 6 {
		t.Fatalf("sample customers/promos after retry = %d, want 6", n)
	}
	if n, _ := seedRepo.SampleItemCount(t.Context()); n != 52 {
		t.Fatalf("sample items after retry = %d, want unchanged 52", n)
	}
	if n := auditRows(); n != 1 {
		t.Fatalf("audit rows after retry = %d, want 1", n)
	}
}

// ut-docs#3709 review N1: two concurrent taps (two tabs, or two devices on
// the same till) must not both see "nothing loaded yet" and both audit
// sample_data_loaded. sampleDataAfterCountSync pauses each request right
// after its "already loaded?" read: without the lock both requests reach it
// (the second arrives while the first is parked) and both seed + audit;
// with it, the second waits for the first to finish and answers "already
// loaded".
func TestImportSampleData_ConcurrentLoadsAuditOnce(t *testing.T) {
	mux, _, auditRows := newSampleDataDeps(t)
	arrived := make(chan struct{}, 2)
	sampleDataAfterCountSync = func() {
		arrived <- struct{}{}
		// Park until the other request also arrives here, or give up: with
		// the lock held it never can.
		select {
		case <-time.After(300 * time.Millisecond):
		case <-func() chan struct{} {
			done := make(chan struct{})
			go func() {
				for len(arrived) < 2 {
					time.Sleep(5 * time.Millisecond)
				}
				close(done)
			}()
			return done
		}():
		}
	}
	t.Cleanup(func() { sampleDataAfterCountSync = nil })

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = postForm(mux, "/api/import/sample-data", url.Values{}, &mgrUser).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("concurrent load %d: code %d, want 200", i, c)
		}
	}
	if got := auditRows(); got != 1 {
		t.Fatalf("concurrent loads wrote %d sample_data_loaded audit rows, want 1", got)
	}
}

func TestImportPage_WelcomeModeAndSampleCard(t *testing.T) {
	mux, seedRepo, _ := newSampleDataDeps(t)

	// welcome=1: the three ways to start, plus the sample card's load button.
	rec := getImportAs(mux, "/import?welcome=1", mgrUser, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /import?welcome=1 = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-testid="import-welcome"`,
		`data-welcome-option="file"`,
		`data-welcome-option="sample"`,
		`data-welcome-option="skip"`,
		`href="/items"`,
		`data-testid="sample-data-card"`,
		`hx-post="/api/import/sample-data"`,
		`hx-target="#sample-data-msg"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("welcome page missing %s", want)
		}
	}

	// A plain visit: no welcome block, the sample card is still there.
	body = getImportAs(mux, "/import", mgrUser, false).Body.String()
	if strings.Contains(body, `data-testid="import-welcome"`) {
		t.Error("plain /import shows the welcome block")
	}
	if !strings.Contains(body, `hx-post="/api/import/sample-data"`) {
		t.Error("plain /import has no Load sample data button")
	}

	// Inside the catalog's Import dialog (htmx fragment): no welcome block,
	// but the sample card is there — the way to load sample data later.
	body = getImportAs(mux, "/import?welcome=1", mgrUser, true).Body.String()
	if strings.Contains(body, `data-testid="import-welcome"`) {
		t.Error("the /items Import dialog renders the welcome block")
	}
	if !strings.Contains(body, `data-testid="sample-data-card"`) {
		t.Error("the /items Import dialog has no Sample data card")
	}

	// Once loaded (catalogue AND customers/promos): the card says so and
	// points to Settings → Data, no button.
	if err := seedRepo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := seedRepo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatal(err)
	}
	body = getImportAs(mux, "/import", mgrUser, false).Body.String()
	if !strings.Contains(body, `data-sample-present="1"`) || !strings.Contains(body, `href="/settings#settings-data"`) {
		t.Error("sample card does not report loaded sample data with a link to Settings → Data")
	}
	if strings.Contains(body, `hx-post="/api/import/sample-data"`) {
		t.Error("sample card still offers Load sample data after it was loaded")
	}
}

// The setup wizard's restore step was the only caller of POST /api/import's
// first-boot exemption (anonymous preview before an admin exists). With the
// step gone the exemption goes too: an anonymous request is refused even on
// a till still in first boot.
func TestImport_NoAnonymousFirstBootPreview(t *testing.T) {
	mux, _, _ := newSampleDataDeps(t)
	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "0"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous first-boot preview: code=%d, want 403", rec.Code)
	}
}
