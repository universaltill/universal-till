package data

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	_ "modernc.org/sqlite"
)

// newFiscalTSETestDB creates a minimal in-memory schema with just the table
// RecordFiscalTSESignature/GetFiscalTSESignature need — this package's
// convention of a small hand-rolled schema per test file (see audit_test.go),
// kept column-identical to internal/db/migrations/048_fiscal_tse_signatures.sql.
func newFiscalTSETestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE fiscal_tse_signatures (
	sale_id             TEXT PRIMARY KEY,
	transaction_number  INTEGER NOT NULL DEFAULT 0,
	signature_counter   INTEGER NOT NULL DEFAULT 0,
	serial_number       TEXT NOT NULL DEFAULT '',
	start_time          TEXT NOT NULL DEFAULT '',
	log_time            TEXT NOT NULL DEFAULT '',
	signature           TEXT NOT NULL,
	signature_algorithm TEXT NOT NULL DEFAULT '',
	created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		t.Fatalf("setup stmt failed: %v", err)
	}
	return db
}

// ut-docs#585: a signed sale's §6 KassenSichV evidence (contract
// fiscal-sign-ask.md v1.1.0's `tse` object) round-trips through the
// repository intact.
func TestFiscalTSESignature_RoundTrip(t *testing.T) {
	db := newFiscalTSETestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	in := FiscalTSESignature{
		SaleID:             "sale-1",
		TransactionNumber:  4711,
		SignatureCounter:   12345,
		SerialNumber:       "9d5e-serial",
		StartTime:          "2026-08-15T10:31:00Z",
		LogTime:            "2026-08-15T10:31:02Z",
		Signature:          "MEQCIFsig==",
		SignatureAlgorithm: "ecdsa-plain-SHA256",
	}
	if err := repo.RecordFiscalTSESignature(ctx, in); err != nil {
		t.Fatalf("RecordFiscalTSESignature: %v", err)
	}

	got, ok, err := repo.GetFiscalTSESignature(ctx, "sale-1")
	if err != nil {
		t.Fatalf("GetFiscalTSESignature: %v", err)
	}
	if !ok || got == nil {
		t.Fatal("expected the recorded signature to be found")
	}
	if got.CreatedAt == "" {
		t.Fatal("expected CreatedAt stamped by the DB")
	}
	got.CreatedAt = ""
	if *got != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", *got, in)
	}
}

// A missing sale is (nil, false, nil) — not an error: the caller renders no
// TSE block, never placeholders.
func TestFiscalTSESignature_MissingIsNotAnError(t *testing.T) {
	db := newFiscalTSETestDB(t)
	repo := NewPOSRepo(db)

	got, ok, err := repo.GetFiscalTSESignature(context.Background(), "nope")
	if err != nil {
		t.Fatalf("expected no error for a missing sale, got %v", err)
	}
	if ok || got != nil {
		t.Fatalf("expected (nil, false) for a missing sale, got (%+v, %v)", got, ok)
	}
}

// Idempotency: recording the same sale twice (e.g. a duplicated tender
// retry) neither errors nor double-inserts nor overwrites the first
// recorded evidence — the first write wins, matching the contract's
// "a re-sign of the same sale never duplicates or overwrites".
func TestFiscalTSESignature_RecordIsIdempotent(t *testing.T) {
	db := newFiscalTSETestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	first := FiscalTSESignature{SaleID: "sale-1", TransactionNumber: 1, Signature: "sig-a"}
	second := FiscalTSESignature{SaleID: "sale-1", TransactionNumber: 2, Signature: "sig-b"}
	if err := repo.RecordFiscalTSESignature(ctx, first); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := repo.RecordFiscalTSESignature(ctx, second); err != nil {
		t.Fatalf("second record must not error: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fiscal_tse_signatures WHERE sale_id='sale-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one row, got %d", n)
	}
	got, ok, err := repo.GetFiscalTSESignature(ctx, "sale-1")
	if err != nil || !ok {
		t.Fatalf("get after double record: ok=%v err=%v", ok, err)
	}
	if got.Signature != "sig-a" || got.TransactionNumber != 1 {
		t.Fatalf("first recorded evidence must win, got %+v", got)
	}
}

// ut-docs#665/#1106: §146a Abs. 4 AO till-notification bookkeeping. Since
// ADR-0072 the entries live as JSON blobs in plugin_storage (namespaced
// under the German tax plugin's id), not their own table — these run against
// a real migrated schema (migration 075 applied), with the register/location
// join data still coming from live SQL via ListRegisterLocations.

// fiscalTestPluginID mirrors internal/pages' taxDePluginID (that constant is
// package-private to pages; the store takes the id as a parameter).
const fiscalTestPluginID = "com.universaltill.tax-de"

func newFiscalRegisterDETestStore(t *testing.T) (*FiscalRegisterDEStore, *POSRepo, *sql.DB) {
	t.Helper()
	d := openMigratedDB(t, "fiscal_register_de.db")
	return NewFiscalRegisterDEStore(d.DB, fiscalTestPluginID), NewPOSRepo(d.DB), d.DB
}

func strPtr(s string) *string { return &s }

// A create round-trips through List with every field intact, and List
// resolves the register's name and its location's name+address via the
// live register/location lookup.
func TestFiscalRegisterDE_CreateAndList(t *testing.T) {
	store, repo, sqldb := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	locID, err := repo.CreateStockLocation(ctx, "Main Shop")
	if err != nil {
		t.Fatalf("CreateStockLocation: %v", err)
	}
	if err := repo.SetStockLocationAddressDE(ctx, locID, "Hauptstraße 1", "10115", "Berlin"); err != nil {
		t.Fatalf("SetStockLocationAddressDE: %v", err)
	}
	regID, err := repo.CreateRegister(ctx, "Front Till", &locID)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}

	id, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-serial-1",
		"tse-serial-1", "BSI-cert-1", "cloud-tse", "2026-01-15", strPtr("2026-02-01"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty id")
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	got := list[0]
	if got.ID != id || got.RegisterID != regID || got.RegisterName != "Front Till" {
		t.Fatalf("register identity mismatch: %+v", got)
	}
	if got.LocationID != locID || got.LocationName != "Main Shop" {
		t.Fatalf("location identity mismatch: %+v", got)
	}
	if got.LocationStreet != "Hauptstraße 1" || got.LocationPostcode != "10115" || got.LocationCity != "Berlin" {
		t.Fatalf("location address mismatch: %+v", got)
	}
	if got.EasType != "Tablet-/App-Kassen-Systeme" || got.EasSoftware != "AwesomePOS" || got.EasSerial != "eas-serial-1" {
		t.Fatalf("eas fields mismatch: %+v", got)
	}
	if got.TSESerial != "tse-serial-1" || got.TSECertificationID != "BSI-cert-1" || got.TSEType != "cloud-tse" {
		t.Fatalf("tse fields mismatch: %+v", got)
	}
	if got.AcquiredOn != "2026-01-15" {
		t.Fatalf("AcquiredOn = %q", got.AcquiredOn)
	}
	if got.CommissionedOn == nil || *got.CommissionedOn != "2026-02-01" {
		t.Fatalf("CommissionedOn = %v, want 2026-02-01", got.CommissionedOn)
	}
	if got.DecommissionedOn != nil {
		t.Fatalf("DecommissionedOn = %v, want nil", got.DecommissionedOn)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("expected created_at/updated_at to be stamped, got %+v", got)
	}

	// Sanity: the entry really landed in plugin_storage under the German tax
	// plugin's namespace, keyed by the id Create returned (ADR-0072).
	var count int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ? AND key = 'fiscal_register:' || ?`,
		fiscalTestPluginID, id).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one plugin_storage row for id %q, got %d", id, count)
	}
}

// commissioned_on is optional: creating without it round-trips as nil, not
// an empty string.
func TestFiscalRegisterDE_CreateWithoutCommissionedOn(t *testing.T) {
	store, repo, _ := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Back Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	if _, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-2",
		"tse-2", "cert-2", "cloud-tse", "2026-03-01", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0].CommissionedOn != nil {
		t.Fatalf("CommissionedOn = %v, want nil", list[0].CommissionedOn)
	}
}

// A register with no assigned location still appears in the list — the
// lookup is a LEFT JOIN, not INNER — with empty location fields rather than
// being silently dropped.
func TestFiscalRegisterDE_ListIncludesRegisterWithNoLocation(t *testing.T) {
	store, repo, _ := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Unassigned Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	if _, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-3",
		"tse-3", "cert-3", "cloud-tse", "2026-04-01", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the unassigned register's entry to still be listed, got %d", len(list))
	}
	got := list[0]
	if got.LocationID != "" || got.LocationName != "" || got.LocationStreet != "" {
		t.Fatalf("expected empty location fields for an unassigned register, got %+v", got)
	}
}

// A bad register_id fails at create time with a clear error. The old table's
// FK used to reject this; plugin_storage has no FK onto registers, so the
// store's own existence check must preserve the behavior — and nothing may
// be written for the rejected create.
func TestFiscalRegisterDE_CreateWithBadRegisterIDFails(t *testing.T) {
	store, _, sqldb := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	_, err := store.Create(ctx, "no-such-register", "Tablet-/App-Kassen-Systeme", "AwesomePOS",
		"eas-4", "tse-4", "cert-4", "cloud-tse", "2026-05-01", nil)
	if err == nil {
		t.Fatal("expected an error for a nonexistent register_id")
	}
	var n int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ?`, fiscalTestPluginID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("rejected create must not leave a storage row behind, got %d", n)
	}
}

// An INACTIVE register still accepts a create — the old FK never checked
// is_active, and the page's own picker (active-only) is the UI-level filter;
// the data layer must not silently tighten that contract.
func TestFiscalRegisterDE_CreateAgainstInactiveRegisterSucceeds(t *testing.T) {
	store, repo, _ := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Dormant Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	if err := repo.SetRegisterActive(ctx, regID, false); err != nil {
		t.Fatalf("SetRegisterActive: %v", err)
	}
	if _, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-inactive",
		"tse-inactive", "cert-inactive", "cloud-tse", "2026-05-01", nil); err != nil {
		t.Fatalf("Create against inactive register: %v", err)
	}
}

// A decommissioned till's register is routinely deactivated afterwards — its
// history must still list with the register's real name. ListRegisters
// filters is_active=1 and is deliberately NOT what backs the join lookup.
func TestFiscalRegisterDE_ListIncludesInactiveRegisterName(t *testing.T) {
	store, repo, _ := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Retired Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	id, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-retired",
		"tse-retired", "cert-retired", "cloud-tse", "2026-01-01", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Decommission(ctx, id, "2026-06-30"); err != nil {
		t.Fatalf("Decommission: %v", err)
	}
	if err := repo.SetRegisterActive(ctx, regID, false); err != nil {
		t.Fatalf("SetRegisterActive: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the deactivated register's entry to still be listed, got %d", len(list))
	}
	if list[0].RegisterName != "Retired Till" {
		t.Fatalf("RegisterName = %q, want %q (inactive register must keep its name)", list[0].RegisterName, "Retired Till")
	}
}

// Decommissioning stamps decommissioned_on and bumps updated_at, but the
// row stays fully visible in the list afterward — the whole point of the
// AO record is that it survives the till going out of service.
func TestFiscalRegisterDE_DecommissionKeepsRowVisible(t *testing.T) {
	store, repo, _ := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Retiring Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	id, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-5",
		"tse-5", "cert-5", "cloud-tse", "2026-01-01", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	before, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List (before): %v", err)
	}
	if before[0].UpdatedAt == "" {
		t.Fatal("expected updated_at to be stamped on create")
	}

	if err := store.Decommission(ctx, id, "2026-06-30"); err != nil {
		t.Fatalf("Decommission: %v", err)
	}

	after, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List (after): %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("expected the decommissioned row to stay listed, got %d entries", len(after))
	}
	got := after[0]
	if got.DecommissionedOn == nil || *got.DecommissionedOn != "2026-06-30" {
		t.Fatalf("DecommissionedOn = %v, want 2026-06-30", got.DecommissionedOn)
	}
	// updated_at is re-stamped on decommission (RFC3339 second-resolution,
	// so within the same second it can legitimately equal the create-time
	// value -- what matters is it's still a valid, non-empty timestamp, not
	// left over/blanked by the update).
	if got.UpdatedAt == "" {
		t.Fatal("expected updated_at to remain stamped after decommission")
	}
	if _, err := time.Parse(time.RFC3339, got.UpdatedAt); err != nil {
		t.Fatalf("updated_at not a valid RFC3339 timestamp: %q (%v)", got.UpdatedAt, err)
	}
}

// Decommissioning an id that doesn't exist is an error naming the id,
// equivalent to the old zero-rows-UPDATE behavior.
func TestFiscalRegisterDE_DecommissionNotFound(t *testing.T) {
	store, _, _ := newFiscalRegisterDETestStore(t)
	err := store.Decommission(context.Background(), "no-such-id", "2026-06-30")
	if err == nil {
		t.Fatal("expected an error for a nonexistent id")
	}
	if !strings.Contains(err.Error(), "no-such-id not found") {
		t.Fatalf("error should name the missing id, got %v", err)
	}
}

// List skips a malformed entry instead of failing the whole page (ADR-0072/
// ut-docs#1106 review finding S3) — a plugin's own storage namespace is
// writable by that plugin's WASM guest code, so one bad key must not make
// every other genuine entry unreachable.
func TestFiscalRegisterDE_ListSkipsMalformedEntry(t *testing.T) {
	store, repo, sqldb := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Front Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	id, err := store.Create(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-serial-1",
		"tse-serial-1", "BSI-cert-1", "cloud-tse", "2026-01-15", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Plant a malformed value directly, as a buggy/malicious plugin guest
	// writing under its own namespace could via storage_set.
	if _, err := sqldb.ExecContext(ctx,
		`INSERT INTO plugin_storage (plugin_id, key, value, updated_at) VALUES (?, ?, ?, datetime('now'))`,
		fiscalTestPluginID, "fiscal_register:bad", []byte("not json")); err != nil {
		t.Fatalf("seed malformed entry: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List must not fail on one malformed entry, got: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("expected the one genuine entry to still be listed, got %+v", list)
	}
}

// SetStockLocationAddressDE updates the three address columns and errors
// for an unknown location id.
func TestSetStockLocationAddressDE(t *testing.T) {
	_, repo, sqldb := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	locID, err := repo.CreateStockLocation(ctx, "Branch")
	if err != nil {
		t.Fatalf("CreateStockLocation: %v", err)
	}
	if err := repo.SetStockLocationAddressDE(ctx, locID, "Bahnhofstr. 5", "80331", "München"); err != nil {
		t.Fatalf("SetStockLocationAddressDE: %v", err)
	}

	var street, postcode, city string
	if err := sqldb.QueryRow(`SELECT address_street, address_postcode, address_city FROM stock_locations WHERE id = ?`, locID).
		Scan(&street, &postcode, &city); err != nil {
		t.Fatalf("select address: %v", err)
	}
	if street != "Bahnhofstr. 5" || postcode != "80331" || city != "München" {
		t.Fatalf("address mismatch: street=%q postcode=%q city=%q", street, postcode, city)
	}

	if err := repo.SetStockLocationAddressDE(ctx, "no-such-location", "x", "y", "z"); err == nil {
		t.Fatal("expected an error for a nonexistent location id")
	}
}

// The list orders by location name (unassigned last), then register name,
// then acquired_on — now an in-memory sort (ADR-0072), so the tie-breaks
// are pinned explicitly: two registers in one location order by register
// name, and one register's multiple entries order by acquired_on.
func TestFiscalRegisterDE_ListOrdering(t *testing.T) {
	store, repo, _ := newFiscalRegisterDETestStore(t)
	ctx := context.Background()

	locZ, err := repo.CreateStockLocation(ctx, "Z Branch")
	if err != nil {
		t.Fatalf("CreateStockLocation Z: %v", err)
	}
	locA, err := repo.CreateStockLocation(ctx, "A Branch")
	if err != nil {
		t.Fatalf("CreateStockLocation A: %v", err)
	}

	regZ, err := repo.CreateRegister(ctx, "Z Till", &locZ)
	if err != nil {
		t.Fatalf("CreateRegister Z: %v", err)
	}
	regA, err := repo.CreateRegister(ctx, "A Till", &locA)
	if err != nil {
		t.Fatalf("CreateRegister A: %v", err)
	}
	regA2, err := repo.CreateRegister(ctx, "B Till", &locA)
	if err != nil {
		t.Fatalf("CreateRegister B: %v", err)
	}
	regUnassigned, err := repo.CreateRegister(ctx, "Unassigned Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister unassigned: %v", err)
	}

	// Seeded deliberately out of every expected order (locations Z before A,
	// the later acquired_on first, B Till before A Till) so the sort itself
	// does the work, not insertion order.
	for _, r := range []struct {
		reg, easSerial, acquiredOn string
	}{
		{regZ, "eas-z", "2026-01-01"},
		{regA2, "eas-b", "2026-01-01"},
		{regA, "eas-a2", "2026-03-01"}, // A Till's SECOND acquisition (TSE swap)
		{regA, "eas-a1", "2026-01-01"},
		{regUnassigned, "eas-u", "2026-01-01"},
	} {
		if _, err := store.Create(ctx, r.reg, "Tablet-/App-Kassen-Systeme", "AwesomePOS", r.easSerial,
			"tse-x", "cert-x", "cloud-tse", r.acquiredOn, nil); err != nil {
			t.Fatalf("Create %s: %v", r.easSerial, err)
		}
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(list))
	}
	var got []string
	for _, e := range list {
		got = append(got, e.EasSerial)
	}
	// A Branch first (A Till's two entries by acquired_on, then B Till),
	// then Z Branch, then the unassigned register LAST.
	want := []string{"eas-a1", "eas-a2", "eas-b", "eas-z", "eas-u"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// Migration 075 round-trip (ADR-0072): a row written by the old
// fiscal_register_de table must come out of the new storage-backed List
// with every field intact after the migration runs — including the NULL
// commissioned_on/decommissioned_on shape — and the old table must be gone.
// Simulated the same way this repo's other upgrade tests do it: open (all
// migrations apply), physically rewind 075 (recreate the 059-shape table),
// un-record it in schema_migrations, seed a pre-migration row, reopen.
func TestFiscalRegisterDE_Migration075RoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "m075.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// The entry's register + location, created through the live schema —
	// they survive the migration untouched.
	repo := NewPOSRepo(d.DB)
	locID, err := repo.CreateStockLocation(ctx, "Main Shop")
	if err != nil {
		t.Fatalf("CreateStockLocation: %v", err)
	}
	if err := repo.SetStockLocationAddressDE(ctx, locID, "Hauptstraße 1", "10115", "Berlin"); err != nil {
		t.Fatalf("SetStockLocationAddressDE: %v", err)
	}
	regID, err := repo.CreateRegister(ctx, "Front Till", &locID)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}

	// Rewind 075: recreate the 059-shape table (075 dropped it) and
	// un-record the migration so the next Open replays exactly it.
	if _, err := d.DB.Exec(`CREATE TABLE fiscal_register_de (
	id TEXT PRIMARY KEY, register_id TEXT NOT NULL REFERENCES registers(id),
	eas_type TEXT NOT NULL, eas_software TEXT NOT NULL, eas_serial TEXT NOT NULL,
	tse_serial TEXT NOT NULL, tse_certification_id TEXT NOT NULL, tse_type TEXT NOT NULL,
	acquired_on TEXT NOT NULL, commissioned_on TEXT, decommissioned_on TEXT,
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("recreate pre-075 table: %v", err)
	}
	// 078 (sale_lines.order_type, ut-docs#1181) replays too after a >= 75
	// rewind — undo its non-idempotent ALTER TABLEs first, same as the
	// internal/db rewind tests' helpers.
	for _, tbl := range []string{"sale_lines", "sale_lines_archive"} {
		if _, err := d.DB.Exec(`ALTER TABLE ` + tbl + ` DROP COLUMN order_type`); err != nil {
			t.Fatalf("rewind 078 (%s): %v", tbl, err)
		}
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 75`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	// Two pre-migration rows: one fully populated, one with both optional
	// dates NULL — both shapes must round-trip.
	if _, err := d.DB.Exec(`INSERT INTO fiscal_register_de
	(id, register_id, eas_type, eas_software, eas_serial, tse_serial, tse_certification_id, tse_type,
	 acquired_on, commissioned_on, decommissioned_on, created_at, updated_at)
	VALUES
	('old-1', ?, 'Tablet-/App-Kassen-Systeme', 'AwesomePOS', 'eas-old-1', 'tse-old-1', 'cert-old-1', 'cloud-tse',
	 '2025-11-01', '2025-12-01', '2026-02-01', '2025-11-01T09:00:00Z', '2026-02-01T09:00:00Z'),
	('old-2', ?, 'Tablet-/App-Kassen-Systeme', 'AwesomePOS', 'eas-old-2', 'tse-old-2', 'cert-old-2', 'cloud-tse',
	 '2026-01-15', NULL, NULL, '2026-01-15T09:00:00Z', '2026-01-15T09:00:00Z')`, regID, regID); err != nil {
		t.Fatalf("seed pre-migration rows: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	d, err = db.Open(path) // replays migration 075
	if err != nil {
		t.Fatalf("reopen (075 replay): %v", err)
	}
	t.Cleanup(func() { d.Close() })

	store := NewFiscalRegisterDEStore(d.DB, fiscalTestPluginID)
	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List after migration: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected both migrated entries, got %d: %+v", len(list), list)
	}
	byID := map[string]FiscalRegisterDE{list[0].ID: list[0], list[1].ID: list[1]}
	full, ok := byID["old-1"]
	if !ok {
		t.Fatalf("old-1 missing after migration: %+v", byID)
	}
	if full.RegisterID != regID || full.RegisterName != "Front Till" ||
		full.LocationID != locID || full.LocationName != "Main Shop" ||
		full.LocationStreet != "Hauptstraße 1" || full.LocationPostcode != "10115" || full.LocationCity != "Berlin" {
		t.Fatalf("old-1 register/location join mismatch: %+v", full)
	}
	if full.EasType != "Tablet-/App-Kassen-Systeme" || full.EasSoftware != "AwesomePOS" ||
		full.EasSerial != "eas-old-1" || full.TSESerial != "tse-old-1" ||
		full.TSECertificationID != "cert-old-1" || full.TSEType != "cloud-tse" {
		t.Fatalf("old-1 eas/tse fields mismatch: %+v", full)
	}
	if full.AcquiredOn != "2025-11-01" ||
		full.CommissionedOn == nil || *full.CommissionedOn != "2025-12-01" ||
		full.DecommissionedOn == nil || *full.DecommissionedOn != "2026-02-01" ||
		full.CreatedAt != "2025-11-01T09:00:00Z" || full.UpdatedAt != "2026-02-01T09:00:00Z" {
		t.Fatalf("old-1 dates/timestamps mismatch: %+v", full)
	}
	bare, ok := byID["old-2"]
	if !ok {
		t.Fatalf("old-2 missing after migration: %+v", byID)
	}
	if bare.CommissionedOn != nil || bare.DecommissionedOn != nil {
		t.Fatalf("old-2's NULL optional dates must round-trip as nil, got %+v", bare)
	}

	// The migrated entry stays fully operable through the new backend.
	if err := store.Decommission(ctx, "old-2", "2026-08-30"); err != nil {
		t.Fatalf("Decommission migrated entry: %v", err)
	}

	// And the old table is really gone.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM fiscal_register_de`).Scan(&n); err == nil {
		t.Fatalf("fiscal_register_de still queryable after 075 (n=%d), want no-such-table error", n)
	}
}
