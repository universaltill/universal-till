package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

// ut-docs#665: §146a Abs. 4 AO till-notification bookkeeping. These run
// against a real migrated schema (migration 059) — the register_id FK, the
// stock_locations address columns, and the LEFT JOIN shape are all part of
// the behavior under test.

func newFiscalRegisterDETestRepo(t *testing.T) (*POSRepo, *sql.DB) {
	t.Helper()
	d := openMigratedDB(t, "fiscal_register_de.db")
	return NewPOSRepo(d.DB), d.DB
}

func strPtr(s string) *string { return &s }

// A create round-trips through List with every field intact, and List
// resolves the register's name and its location's name+address via the
// join.
func TestFiscalRegisterDE_CreateAndList(t *testing.T) {
	repo, sqldb := newFiscalRegisterDETestRepo(t)
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

	id, err := repo.CreateFiscalRegisterDE(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-serial-1",
		"tse-serial-1", "BSI-cert-1", "cloud-tse", "2026-01-15", strPtr("2026-02-01"))
	if err != nil {
		t.Fatalf("CreateFiscalRegisterDE: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty id")
	}

	list, err := repo.ListFiscalRegisterDE(ctx)
	if err != nil {
		t.Fatalf("ListFiscalRegisterDE: %v", err)
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

	// Sanity: the row really is in the DB with the id CreateFiscalRegisterDE
	// returned.
	var count int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM fiscal_register_de WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row for id %q, got %d", id, count)
	}
}

// commissioned_on is optional: creating without it round-trips as nil, not
// an empty string.
func TestFiscalRegisterDE_CreateWithoutCommissionedOn(t *testing.T) {
	repo, _ := newFiscalRegisterDETestRepo(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Back Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	if _, err := repo.CreateFiscalRegisterDE(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-2",
		"tse-2", "cert-2", "cloud-tse", "2026-03-01", nil); err != nil {
		t.Fatalf("CreateFiscalRegisterDE: %v", err)
	}

	list, err := repo.ListFiscalRegisterDE(ctx)
	if err != nil {
		t.Fatalf("ListFiscalRegisterDE: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0].CommissionedOn != nil {
		t.Fatalf("CommissionedOn = %v, want nil", list[0].CommissionedOn)
	}
}

// A register with no assigned location still appears in the list — LEFT
// JOIN, not INNER — with empty location fields rather than being silently
// dropped.
func TestFiscalRegisterDE_ListIncludesRegisterWithNoLocation(t *testing.T) {
	repo, _ := newFiscalRegisterDETestRepo(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Unassigned Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	if _, err := repo.CreateFiscalRegisterDE(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-3",
		"tse-3", "cert-3", "cloud-tse", "2026-04-01", nil); err != nil {
		t.Fatalf("CreateFiscalRegisterDE: %v", err)
	}

	list, err := repo.ListFiscalRegisterDE(ctx)
	if err != nil {
		t.Fatalf("ListFiscalRegisterDE: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the unassigned register's entry to still be listed, got %d", len(list))
	}
	got := list[0]
	if got.LocationID != "" || got.LocationName != "" || got.LocationStreet != "" {
		t.Fatalf("expected empty location fields for an unassigned register, got %+v", got)
	}
}

// A bad register_id fails at create time with a clear, wrapped error — the
// FK constraint is what actually rejects it, but the caller must not see a
// raw driver error.
func TestFiscalRegisterDE_CreateWithBadRegisterIDFails(t *testing.T) {
	repo, _ := newFiscalRegisterDETestRepo(t)
	ctx := context.Background()

	_, err := repo.CreateFiscalRegisterDE(ctx, "no-such-register", "Tablet-/App-Kassen-Systeme", "AwesomePOS",
		"eas-4", "tse-4", "cert-4", "cloud-tse", "2026-05-01", nil)
	if err == nil {
		t.Fatal("expected an error for a nonexistent register_id")
	}
}

// Decommissioning stamps decommissioned_on and bumps updated_at, but the
// row stays fully visible in the list afterward — the whole point of the
// AO record is that it survives the till going out of service.
func TestFiscalRegisterDE_DecommissionKeepsRowVisible(t *testing.T) {
	repo, _ := newFiscalRegisterDETestRepo(t)
	ctx := context.Background()

	regID, err := repo.CreateRegister(ctx, "Retiring Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	id, err := repo.CreateFiscalRegisterDE(ctx, regID, "Tablet-/App-Kassen-Systeme", "AwesomePOS", "eas-5",
		"tse-5", "cert-5", "cloud-tse", "2026-01-01", nil)
	if err != nil {
		t.Fatalf("CreateFiscalRegisterDE: %v", err)
	}

	before, err := repo.ListFiscalRegisterDE(ctx)
	if err != nil {
		t.Fatalf("ListFiscalRegisterDE (before): %v", err)
	}
	if before[0].UpdatedAt == "" {
		t.Fatal("expected updated_at to be stamped on create")
	}

	if err := repo.DecommissionFiscalRegisterDE(ctx, id, "2026-06-30"); err != nil {
		t.Fatalf("DecommissionFiscalRegisterDE: %v", err)
	}

	after, err := repo.ListFiscalRegisterDE(ctx)
	if err != nil {
		t.Fatalf("ListFiscalRegisterDE (after): %v", err)
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

// Decommissioning an id that doesn't exist is an error, mirroring
// RenameRegister's RowsAffected()==0 pattern.
func TestFiscalRegisterDE_DecommissionNotFound(t *testing.T) {
	repo, _ := newFiscalRegisterDETestRepo(t)
	if err := repo.DecommissionFiscalRegisterDE(context.Background(), "no-such-id", "2026-06-30"); err == nil {
		t.Fatal("expected an error for a nonexistent id")
	}
}

// SetStockLocationAddressDE updates the three address columns and errors
// for an unknown location id.
func TestSetStockLocationAddressDE(t *testing.T) {
	repo, sqldb := newFiscalRegisterDETestRepo(t)
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
// then acquired_on.
func TestFiscalRegisterDE_ListOrdering(t *testing.T) {
	repo, _ := newFiscalRegisterDETestRepo(t)
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
	regUnassigned, err := repo.CreateRegister(ctx, "Unassigned Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister unassigned: %v", err)
	}

	for _, r := range []struct {
		reg, easSerial string
	}{
		{regZ, "eas-z"},
		{regA, "eas-a"},
		{regUnassigned, "eas-u"},
	} {
		if _, err := repo.CreateFiscalRegisterDE(ctx, r.reg, "Tablet-/App-Kassen-Systeme", "AwesomePOS", r.easSerial,
			"tse-x", "cert-x", "cloud-tse", "2026-01-01", nil); err != nil {
			t.Fatalf("CreateFiscalRegisterDE %s: %v", r.reg, err)
		}
	}

	list, err := repo.ListFiscalRegisterDE(ctx)
	if err != nil {
		t.Fatalf("ListFiscalRegisterDE: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	gotOrder := []string{list[0].LocationName, list[1].LocationName, list[2].LocationName}
	wantOrder := []string{"A Branch", "Z Branch", ""}
	if gotOrder[0] != wantOrder[0] || gotOrder[1] != wantOrder[1] || gotOrder[2] != wantOrder[2] {
		t.Fatalf("location order = %v, want %v", gotOrder, wantOrder)
	}
}
