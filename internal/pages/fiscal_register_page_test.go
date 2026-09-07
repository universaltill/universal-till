package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// seedTaxDeCatalogRow inserts the plugin_catalog row seedActiveTaxDePlugin/
// seedDisabledTaxDePlugin's own plugins row now needs: openPagesTestDB runs
// the real migrated schema (ut-docs#1657/#1676), where plugins has a
// composite FOREIGN KEY (id, version) REFERENCES plugin_catalog (id,
// version) and entrypoint NOT NULL on both tables -- this file's own
// fixture/schema gap note (below) predates that swap and no longer applies.
func seedTaxDeCatalogRow(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at) VALUES ('com.universaltill.tax-de', '1.0.0', 'German Tax', 'desc', 'go', 'entry', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`); err != nil {
		t.Fatalf("seed tax-de plugin_catalog: %v", err)
	}
}

// seedActiveTaxDePlugin inserts a minimal com.universaltill.tax-de row
// into `plugins` (used by menu_page_test.go's tile-gate tests, ut-docs#1084).
func seedActiveTaxDePlugin(t *testing.T, db *sql.DB) {
	t.Helper()
	seedTaxDeCatalogRow(t, db)
	if _, err := db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES ('com.universaltill.tax-de', 'German Tax', '1.0.0', 'entry', 1)`); err != nil {
		t.Fatalf("seed active tax-de plugin: %v", err)
	}
}

// seedDisabledTaxDePlugin is seedActiveTaxDePlugin's installed-but-disabled
// counterpart (ut-docs#531's precedent, import_page_test.go's
// installTaxDePluginDisabled) -- used by menu_page_test.go to prove the
// tile gate actually checks is_active, not merely row existence.
func seedDisabledTaxDePlugin(t *testing.T, db *sql.DB) {
	t.Helper()
	seedTaxDeCatalogRow(t, db)
	if _, err := db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES ('com.universaltill.tax-de', 'German Tax', '1.0.0', 'entry', 0)`); err != nil {
		t.Fatalf("seed disabled tax-de plugin: %v", err)
	}
}

// §146a Abs. 4 AO fiscal register page (ut-docs#665). Data capture only —
// no export, no XML, no filing (that's ut-docs#937). Structural mirror of
// registers_page_test.go's setup.

func newFiscalRegisterTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	// ut-docs#1682: every test in this file acts as manager "m1" via
	// auth.WithUser; audit_log.actor_id has a real FK to users(id) now that
	// openPagesTestDB runs real migrations (ut-docs#1676), so "m1" needs a
	// real seeded row.
	if _, err := db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES('m1','m1','Manager','','manager')`); err != nil {
		t.Fatalf("seed manager m1: %v", err)
	}

	// Settings backs the replica check (SyncPrimaryURL) -- left unset, this
	// till is a primary/standalone, matching every pre-existing test in
	// this file, same convention as registers_page_test.go.
	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db), Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerFiscalRegisterDE(mux, d)
	return mux, d
}

// ut-docs#1670: fiscal_register: rows are §146a Abs. 4 AO shop-wide
// compliance data that now syncs primary->satellite via plugin_storage's
// scoped adminTables entry (sync_admin_repo.go). A write accepted on a
// satellite would either be silently overwritten by the next admin pull, or
// (before this fix, since plugin_storage wasn't synced at all) diverge
// forever -- same #1546 shape as registers_page.go's own replica gate. All
// three mutating routes must refuse on a replica.
func TestFiscalRegisterPage_MutationsRefusedOnReplica(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	regID := createRegisterForFiscalTest(t, d, "Front Till")

	// An entry that existed before this till became a replica (e.g. synced
	// down from the primary) -- used below to check decommission/address,
	// since create itself is refused and can't produce one.
	createForm := url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-existing"},
		"tse_serial":           {"tse-existing"},
		"tse_certification_id": {"cert-existing"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-01"},
	}
	if rec := postForm(mux, "/api/fiscal-register", createForm, &manager); rec.Code != http.StatusSeeOther {
		t.Fatalf("seed entry: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var entryID string
	if err := d.Db.QueryRow(`SELECT json_extract(value, '$.id') FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND json_extract(value, '$.eas_serial') = 'eas-existing'`).Scan(&entryID); err != nil {
		t.Fatalf("lookup seeded entry: %v", err)
	}
	var locID string
	if err := d.Db.QueryRow(`SELECT id FROM stock_locations LIMIT 1`).Scan(&locID); err != nil {
		t.Fatalf("lookup any location: %v", err)
	}

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	// Create.
	rec := postForm(mux, "/api/fiscal-register", createForm, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.replica_use_primary" {
		t.Fatalf("create on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND json_extract(value, '$.eas_serial') = 'eas-existing'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("a second entry must not be created on a replica, found %d rows", count)
	}

	// Decommission.
	rec = postForm(mux, "/api/fiscal-register/"+entryID+"/decommission", url.Values{}, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.replica_use_primary" {
		t.Fatalf("decommission on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var decommissionedOn *string
	if err := d.Db.QueryRow(`SELECT json_extract(value, '$.decommissioned_on') FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'fiscal_register:' || ?`, entryID).Scan(&decommissionedOn); err != nil {
		t.Fatalf("lookup entry after blocked decommission: %v", err)
	}
	if decommissionedOn != nil {
		t.Fatalf("blocked replica decommission still took effect: decommissioned_on = %q", *decommissionedOn)
	}

	// Address update.
	rec = postForm(mux, "/api/fiscal-register/locations/"+locID+"/address", url.Values{"street": {"Nope"}, "postcode": {"00000"}, "city": {"Nope"}}, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.replica_use_primary" {
		t.Fatalf("address update on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var city string
	if err := d.Db.QueryRow(`SELECT COALESCE(address_city, '') FROM stock_locations WHERE id = ?`, locID).Scan(&city); err != nil {
		t.Fatalf("lookup location after blocked address update: %v", err)
	}
	if city == "Nope" {
		t.Fatalf("blocked replica address update still took effect")
	}
}

// ut-docs#1670 review (N5): requireManager must run BEFORE requirePrimary,
// same order as registers_page.go — a non-manager on a replica must still
// get 403, never the primary-only redirect, or the redirect's presence
// would leak "this till is a replica" to an unauthorized caller. Every
// other test in this file uses a manager actor, so this is the one case
// that actually pins the ordering (swapping the two checks wouldn't fail
// any of them).
func TestFiscalRegisterPage_NonManagerOnReplicaGetsManagerError(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	regID := createRegisterForFiscalTest(t, d, "Front Till")

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := postForm(mux, "/api/fiscal-register", url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-cashier-replica"},
		"tse_serial":           {"tse-cashier-replica"},
		"tse_certification_id": {"cert-cashier-replica"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-01"},
	}, &cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create on replica: code=%d, want 403 (manager gate must win, not the replica redirect); loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestFiscalRegisterPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newFiscalRegisterTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/fiscal-register", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fiscal-register under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestFiscalRegisterPage_ManagerGate(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-register", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /fiscal-register = %d, want 403", rec.Code)
	}
	// ut-docs#1458: GET /fiscal-register must render the full layout on
	// 403 too, not a bare rail-less body (same fix class as #1455).
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /fiscal-register has no nav rail:\n%s", body)
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-register", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET /fiscal-register = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// All three mutating routes must gate a cashier too (2026-08-24 review,
	// S1) -- the GET check above proves nothing about them, and this exact
	// gap (a page's GET gated, its POST routes not separately tested) is
	// precisely the class of regression this repo's own review standard
	// exists to catch.
	regID := createRegisterForFiscalTest(t, d, "Gate Test Till")
	createForm := url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-gate"},
		"tse_serial":           {"tse-gate"},
		"tse_certification_id": {"cert-gate"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-01"},
	}
	if rec := postForm(mux, "/api/fiscal-register", createForm, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}

	// Seed one real entry (as manager) so the decommission/address routes
	// have a target id to attempt against.
	if rec := postForm(mux, "/api/fiscal-register", createForm, &manager); rec.Code != http.StatusSeeOther {
		t.Fatalf("seed entry: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var entryID string
	if err := d.Db.QueryRow(`SELECT json_extract(value, '$.id') FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND json_extract(value, '$.eas_serial') = 'eas-gate'`).Scan(&entryID); err != nil {
		t.Fatalf("lookup seeded entry: %v", err)
	}
	var locID string
	if err := d.Db.QueryRow(`SELECT id FROM stock_locations LIMIT 1`).Scan(&locID); err != nil {
		t.Fatalf("lookup any location: %v", err)
	}

	if rec := postForm(mux, "/api/fiscal-register/"+entryID+"/decommission", url.Values{}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier decommission = %d, want 403", rec.Code)
	}
	addrForm := url.Values{"street": {"Nope"}, "postcode": {"00000"}, "city": {"Nope"}}
	if rec := postForm(mux, "/api/fiscal-register/locations/"+locID+"/address", addrForm, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier address update = %d, want 403", rec.Code)
	}

	// The cashier's blocked decommission/address attempts must not have
	// taken effect.
	var decommissionedOn *string
	if err := d.Db.QueryRow(`SELECT json_extract(value, '$.decommissioned_on') FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'fiscal_register:' || ?`, entryID).Scan(&decommissionedOn); err != nil {
		t.Fatalf("lookup entry after blocked decommission: %v", err)
	}
	if decommissionedOn != nil {
		t.Fatalf("blocked cashier decommission still took effect: decommissioned_on = %q", *decommissionedOn)
	}
	var city string
	if err := d.Db.QueryRow(`SELECT COALESCE(address_city, '') FROM stock_locations WHERE id = ?`, locID).Scan(&city); err != nil {
		t.Fatalf("lookup location after blocked address update: %v", err)
	}
	if city == "Nope" {
		t.Fatalf("blocked cashier address update still took effect")
	}
}

// createRegisterForFiscalTest seeds a register the create-entry form can
// target, returning its id.
func createRegisterForFiscalTest(t *testing.T, d *common.Deps, name string) string {
	t.Helper()
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = ?`, name).Scan(&id); err == nil {
		return id
	}
	id = "reg-" + name
	if _, err := d.Db.Exec(`INSERT INTO registers (id, name, is_active) VALUES (?, ?, 1)`, id, name); err != nil {
		t.Fatalf("seed register %s: %v", name, err)
	}
	return id
}

func TestFiscalRegisterPageCreate_Success(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	regID := createRegisterForFiscalTest(t, d, "Front Till")

	form := url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-1"},
		"tse_serial":           {"tse-1"},
		"tse_certification_id": {"cert-1"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-15"},
	}
	rec := postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/fiscal-register" {
		t.Fatalf("create: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-register", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fiscal-register = %d", rec.Code)
	}
	if !strings.Contains(body, "eas-1") || !strings.Contains(body, "tse-1") {
		t.Fatalf("expected new entry to render, got: %s", body)
	}
	// eas_type defaults server-side when blank.
	if !strings.Contains(body, "Tablet-/App-Kassen-Systeme") {
		t.Fatalf("expected default eas_type to be applied, got: %s", body)
	}

	// Audit was recorded.
	var action string
	if err := d.Db.QueryRow(`SELECT action FROM audit_log WHERE entity_type = 'fiscal_register_de' ORDER BY created_at DESC LIMIT 1`).Scan(&action); err != nil {
		t.Fatalf("audit lookup: %v", err)
	}
	if action != "fiscal_register_de_create" {
		t.Fatalf("audit action = %q, want fiscal_register_de_create", action)
	}
}

func TestFiscalRegisterPageCreate_RequiredFieldsValidated(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	regID := createRegisterForFiscalTest(t, d, "Front Till")

	// Missing register_id.
	form := url.Values{
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-1"},
		"tse_serial":           {"tse-1"},
		"tse_certification_id": {"cert-1"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-15"},
	}
	rec := postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.required" {
		t.Fatalf("missing register_id: loc=%q", rec.Header().Get("Location"))
	}

	// Whitespace-only eas_serial.
	form = url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"   "},
		"tse_serial":           {"tse-1"},
		"tse_certification_id": {"cert-1"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-15"},
	}
	rec = postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.required" {
		t.Fatalf("whitespace eas_serial: loc=%q", rec.Header().Get("Location"))
	}
}

func TestFiscalRegisterPageCreate_BadDateValidated(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	regID := createRegisterForFiscalTest(t, d, "Front Till")

	form := url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-1"},
		"tse_serial":           {"tse-1"},
		"tse_certification_id": {"cert-1"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"15-01-2026"}, // wrong format
	}
	rec := postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.invalid_date" {
		t.Fatalf("bad acquired_on: loc=%q", rec.Header().Get("Location"))
	}

	form = url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-1"},
		"tse_serial":           {"tse-1"},
		"tse_certification_id": {"cert-1"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-15"},
		"commissioned_on":      {"not-a-date"},
	}
	rec = postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Header().Get("Location") != "/fiscal-register?err=fiscalregister.error.invalid_date" {
		t.Fatalf("bad commissioned_on: loc=%q", rec.Header().Get("Location"))
	}
}

func TestFiscalRegisterPageDecommission(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	regID := createRegisterForFiscalTest(t, d, "Front Till")

	form := url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-9"},
		"tse_serial":           {"tse-9"},
		"tse_certification_id": {"cert-9"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-01"},
	}
	rec := postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var id string
	if err := d.Db.QueryRow(`SELECT json_extract(value, '$.id') FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND json_extract(value, '$.eas_serial') = 'eas-9'`).Scan(&id); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	rec = postForm(mux, "/api/fiscal-register/"+id+"/decommission", url.Values{}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/fiscal-register" {
		t.Fatalf("decommission: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// The date is server-stamped to today, not client-supplied.
	var decommissionedOn string
	if err := d.Db.QueryRow(`SELECT json_extract(value, '$.decommissioned_on') FROM plugin_storage WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'fiscal_register:' || ?`, id).Scan(&decommissionedOn); err != nil {
		t.Fatalf("lookup decommissioned_on: %v", err)
	}
	wantToday := time.Now().UTC().Format("2006-01-02")
	if decommissionedOn != wantToday {
		t.Fatalf("decommissioned_on = %q, want %q (today, server-stamped)", decommissionedOn, wantToday)
	}

	// The row stays listed with status flipped, not removed.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-register", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "eas-9") {
		t.Fatalf("decommissioned entry must stay listed, got: %s", body)
	}

	// Audit was recorded.
	var action string
	if err := d.Db.QueryRow(`SELECT action FROM audit_log WHERE entity_type = 'fiscal_register_de' AND action = 'fiscal_register_de_decommission'`).Scan(&action); err != nil {
		t.Fatalf("audit lookup: %v", err)
	}
}

func TestFiscalRegisterPageAddressUpdate(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	var locID string
	if err := d.Db.QueryRow(`SELECT id FROM stock_locations WHERE name = 'Main'`).Scan(&locID); err != nil {
		t.Fatalf("lookup seeded location: %v", err)
	}
	// A location only gets a heading (and so its address form) once it has
	// at least one fiscal register entry -- give it a register + entry
	// first, same as a real shop would have before ever touching this page.
	if _, err := d.Db.Exec(`INSERT INTO registers (id, name, location_id, is_active) VALUES ('reg-main', 'Main Till', ?, 1)`, locID); err != nil {
		t.Fatalf("seed register for loc_main: %v", err)
	}
	createForm := url.Values{
		"register_id":          {"reg-main"},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-addr"},
		"tse_serial":           {"tse-addr"},
		"tse_certification_id": {"cert-addr"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-01"},
	}
	if rec := postForm(mux, "/api/fiscal-register", createForm, &manager); rec.Code != http.StatusSeeOther {
		t.Fatalf("seed entry for loc_main: code=%d body=%s", rec.Code, rec.Body.String())
	}

	form := url.Values{"street": {"Hauptstraße 1"}, "postcode": {"10115"}, "city": {"Berlin"}}
	rec := postForm(mux, "/api/fiscal-register/locations/"+locID+"/address", form, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/fiscal-register" {
		t.Fatalf("address update: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	var street, postcode, city string
	if err := d.Db.QueryRow(`SELECT address_street, address_postcode, address_city FROM stock_locations WHERE id = ?`, locID).
		Scan(&street, &postcode, &city); err != nil {
		t.Fatalf("lookup address: %v", err)
	}
	if street != "Hauptstraße 1" || postcode != "10115" || city != "Berlin" {
		t.Fatalf("address mismatch: street=%q postcode=%q city=%q", street, postcode, city)
	}

	// It renders on the page.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-register", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Berlin") {
		t.Fatalf("expected updated address to render, got: %s", rec.Body.String())
	}
}

// The list is grouped/joined correctly, including an entry whose register
// has no location assigned (it must still render, under a "no location"
// grouping, not be silently dropped).
func TestFiscalRegisterPage_ListsUnassignedRegisterEntry(t *testing.T) {
	mux, d := newFiscalRegisterTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	regID := createRegisterForFiscalTest(t, d, "Unassigned Till")

	form := url.Values{
		"register_id":          {regID},
		"eas_software":         {"AwesomePOS"},
		"eas_serial":           {"eas-unassigned"},
		"tse_serial":           {"tse-unassigned"},
		"tse_certification_id": {"cert-unassigned"},
		"tse_type":             {"cloud-tse"},
		"acquired_on":          {"2026-01-01"},
	}
	rec := postForm(mux, "/api/fiscal-register", form, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-register", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fiscal-register = %d", rec.Code)
	}
	if !strings.Contains(body, "eas-unassigned") {
		t.Fatalf("expected the unassigned register's entry to be listed, got: %s", body)
	}
}
