package data

// Register management (universaltill/ut-docs#651): create/rename/deactivate
// registers, so multi-till shops can reach the two-register topology
// ut-docs#268 already supports. Mirrors pos_repo_stock_location_test.go's
// pattern.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openRegTestDB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

func TestCreateRegister(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateRegister(ctx, "Front Till", nil)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	if id == "" {
		t.Fatal("CreateRegister returned empty id")
	}

	regs, err := repo.ListRegisters(ctx)
	if err != nil {
		t.Fatalf("ListRegisters: %v", err)
	}
	found := false
	for _, reg := range regs {
		if reg.ID == id && reg.Name == "Front Till" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new register not present in ListRegisters: %+v", regs)
	}
}

func TestCreateRegister_WithLocation(t *testing.T) {
	d, repo := openRegTestDB(t)
	ctx := context.Background()

	locID, err := repo.CreateStockLocation(ctx, "Back Room")
	if err != nil {
		t.Fatalf("create stock location: %v", err)
	}

	id, err := repo.CreateRegister(ctx, "Back Till", &locID)
	if err != nil {
		t.Fatalf("CreateRegister: %v", err)
	}
	var gotLocationID string
	if err := d.DB.QueryRow(`SELECT location_id FROM registers WHERE id = ?`, id).Scan(&gotLocationID); err != nil {
		t.Fatal(err)
	}
	if gotLocationID != locID {
		t.Fatalf("register location_id = %q, want %q", gotLocationID, locID)
	}
}

func TestCreateRegister_DuplicateNameRejected(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	if _, err := repo.CreateRegister(ctx, "Front Till", nil); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := repo.CreateRegister(ctx, "Front Till", nil); err == nil {
		t.Fatal("duplicate name must error (registers.name is UNIQUE)")
	}
}

// ut-docs#894: enrolment auto-provisions a register named after the joining
// till. A name collision (a register with the till's name already exists)
// must NOT fail the enrolment — it retries with a numeric suffix instead.
func TestCreateRegisterForEnrolment_FreshName(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	id, name, err := repo.CreateRegisterForEnrolment(ctx, "Till 2")
	if err != nil {
		t.Fatalf("CreateRegisterForEnrolment: %v", err)
	}
	if id == "" {
		t.Fatal("returned empty id")
	}
	if name != "Till 2" {
		t.Fatalf("fresh name must be used verbatim, got %q", name)
	}
	regs, err := repo.ListRegisters(ctx)
	if err != nil {
		t.Fatalf("ListRegisters: %v", err)
	}
	found := false
	for _, reg := range regs {
		if reg.ID == id && reg.Name == "Till 2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto-provisioned register not in ListRegisters: %+v", regs)
	}
}

func TestCreateRegisterForEnrolment_CollidingNameSuffixed(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	if _, err := repo.CreateRegister(ctx, "till", nil); err != nil {
		t.Fatalf("seed colliding register: %v", err)
	}
	id, name, err := repo.CreateRegisterForEnrolment(ctx, "till")
	if err != nil {
		t.Fatalf("CreateRegisterForEnrolment: %v", err)
	}
	if name != "till (2)" {
		t.Fatalf("colliding name must be suffixed, got %q", name)
	}
	if id == "" {
		t.Fatal("returned empty id")
	}
	// And the next collision walks on to (3).
	_, name3, err := repo.CreateRegisterForEnrolment(ctx, "till")
	if err != nil {
		t.Fatalf("second CreateRegisterForEnrolment: %v", err)
	}
	if name3 != "till (3)" {
		t.Fatalf("second collision must take the next suffix, got %q", name3)
	}
}

func TestCreateRegisterForEnrolment_ExhaustedBoundErrors(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	// Occupy the base name and every suffix the bound allows.
	if _, err := repo.CreateRegister(ctx, "till", nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 2; i <= 50; i++ {
		if _, err := repo.CreateRegister(ctx, fmt.Sprintf("till (%d)", i), nil); err != nil {
			t.Fatalf("seed suffix %d: %v", i, err)
		}
	}
	if _, _, err := repo.CreateRegisterForEnrolment(ctx, "till"); err == nil {
		t.Fatal("expected an error once every candidate name is taken")
	}
}

func TestRenameRegister(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateRegister(ctx, "Old Name", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.RenameRegister(ctx, id, "New Name"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	regs, err := repo.ListRegisters(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, reg := range regs {
		if reg.ID == id {
			if reg.Name != "New Name" {
				t.Fatalf("rename did not take effect: got %q", reg.Name)
			}
			return
		}
	}
	t.Fatalf("renamed register %s not found", id)
}

func TestRenameRegister_UnknownID(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	if err := repo.RenameRegister(ctx, "ghost", "whatever"); err == nil {
		t.Fatal("RenameRegister(unknown) must error")
	}
}

func TestSetRegisterActive(t *testing.T) {
	d, repo := openRegTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateRegister(ctx, "Kiosk Till", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.SetRegisterActive(ctx, id, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	var active int
	if err := d.DB.QueryRow(`SELECT is_active FROM registers WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("register not deactivated in DB")
	}

	if err := repo.SetRegisterActive(ctx, id, true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if err := d.DB.QueryRow(`SELECT is_active FROM registers WHERE id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatal("register not reactivated in DB")
	}

	if err := repo.SetRegisterActive(ctx, "ghost", false); err == nil {
		t.Fatal("SetRegisterActive(unknown) must error")
	}
}

// ut-docs#895: a register's stock location was previously fixed at creation
// time -- there was no fix short of recreating the register.
func TestSetRegisterLocation(t *testing.T) {
	d, repo := openRegTestDB(t)
	ctx := context.Background()

	backID, err := repo.CreateStockLocation(ctx, "Back Room")
	if err != nil {
		t.Fatalf("create stock location: %v", err)
	}
	id, err := repo.CreateRegister(ctx, "Kiosk Till", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Assign it.
	if err := repo.SetRegisterLocation(ctx, id, &backID); err != nil {
		t.Fatalf("set location: %v", err)
	}
	var gotLocationID string
	if err := d.DB.QueryRow(`SELECT location_id FROM registers WHERE id = ?`, id).Scan(&gotLocationID); err != nil {
		t.Fatal(err)
	}
	if gotLocationID != backID {
		t.Fatalf("location_id = %q, want %q", gotLocationID, backID)
	}

	// Move it.
	frontID, err := repo.CreateStockLocation(ctx, "Front Yard")
	if err != nil {
		t.Fatalf("create second stock location: %v", err)
	}
	if err := repo.SetRegisterLocation(ctx, id, &frontID); err != nil {
		t.Fatalf("re-set location: %v", err)
	}
	if err := d.DB.QueryRow(`SELECT location_id FROM registers WHERE id = ?`, id).Scan(&gotLocationID); err != nil {
		t.Fatal(err)
	}
	if gotLocationID != frontID {
		t.Fatalf("location_id = %q, want %q", gotLocationID, frontID)
	}

	// Clear it back to unassigned.
	if err := repo.SetRegisterLocation(ctx, id, nil); err != nil {
		t.Fatalf("clear location: %v", err)
	}
	var nullable sql.NullString
	if err := d.DB.QueryRow(`SELECT location_id FROM registers WHERE id = ?`, id).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable.Valid {
		t.Fatalf("location_id = %q, want NULL", nullable.String)
	}

	if err := repo.SetRegisterLocation(ctx, "ghost", nil); err == nil {
		t.Fatal("SetRegisterLocation(unknown) must error")
	}
}

func TestRegisterInUse(t *testing.T) {
	d, repo := openRegTestDB(t)
	ctx := context.Background()

	freeID, err := repo.CreateRegister(ctx, "Empty Till", nil)
	if err != nil {
		t.Fatalf("create free: %v", err)
	}
	inUse, err := repo.RegisterInUse(ctx, freeID)
	if err != nil {
		t.Fatalf("RegisterInUse(free): %v", err)
	}
	if inUse {
		t.Fatal("brand-new register must not be reported in-use")
	}

	// A register with a shift row counts as in-use.
	mustExec(t, d, `INSERT INTO users (id, username, display_name) VALUES ('usr-t651', 'cashier1', 'Cashier One')`)
	mustExec(t, d, `INSERT INTO shifts (id, register_id, cashier_id, opening_cash) VALUES ('shift-t651', ?, 'usr-t651', 0)`, freeID)
	inUse, err = repo.RegisterInUse(ctx, freeID)
	if err != nil {
		t.Fatalf("RegisterInUse(via shift): %v", err)
	}
	if !inUse {
		t.Fatal("register referenced by a shift must be reported in-use")
	}

	// A register referenced only by a sale (no shift) must also count as in-use.
	viaSaleID, err := repo.CreateRegister(ctx, "Sale-only Till", nil)
	if err != nil {
		t.Fatalf("create via-sale: %v", err)
	}
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at)
		VALUES ('sale-t651', 'R-T651', 'completed', 'sale', 'GBP', 0, 0, 0, 0, ?, datetime('now'))`, viaSaleID)
	inUse, err = repo.RegisterInUse(ctx, viaSaleID)
	if err != nil {
		t.Fatalf("RegisterInUse(via sale): %v", err)
	}
	if !inUse {
		t.Fatal("register referenced only by a sale must be reported in-use")
	}
}

func TestCountActiveRegisters(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	// EnsureRegister leaves the pre-existing default register untouched;
	// the fresh test DB starts with none.
	n, err := repo.CountActiveRegisters(ctx)
	if err != nil {
		t.Fatalf("CountActiveRegisters: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 active registers on a fresh DB, got %d", n)
	}

	id, err := repo.CreateRegister(ctx, "Front Till", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	n, err = repo.CountActiveRegisters(ctx)
	if err != nil {
		t.Fatalf("CountActiveRegisters: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 active register, got %d", n)
	}

	if err := repo.SetRegisterActive(ctx, id, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	n, err = repo.CountActiveRegisters(ctx)
	if err != nil {
		t.Fatalf("CountActiveRegisters: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 active registers after deactivate, got %d", n)
	}
}

func TestListRegistersForAdmin(t *testing.T) {
	_, repo := openRegTestDB(t)
	ctx := context.Background()

	locID, err := repo.CreateStockLocation(ctx, "Front Yard")
	if err != nil {
		t.Fatalf("create stock location: %v", err)
	}
	activeID, err := repo.CreateRegister(ctx, "Active Till", &locID)
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	inactiveID, err := repo.CreateRegister(ctx, "Inactive Till", nil)
	if err != nil {
		t.Fatalf("create inactive: %v", err)
	}
	if err := repo.SetRegisterActive(ctx, inactiveID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	regs, err := repo.ListRegistersForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListRegistersForAdmin: %v", err)
	}
	var gotActive, gotInactive *RegisterAdmin
	for i := range regs {
		switch regs[i].ID {
		case activeID:
			gotActive = &regs[i]
		case inactiveID:
			gotInactive = &regs[i]
		}
	}
	if gotActive == nil || !gotActive.IsActive {
		t.Fatalf("active register not returned as active: %+v", gotActive)
	}
	if gotActive.LocationID == nil || *gotActive.LocationID != locID {
		t.Fatalf("active register location_id = %v, want %q", gotActive.LocationID, locID)
	}
	if gotInactive == nil || gotInactive.IsActive {
		t.Fatalf("inactive register not returned as inactive: %+v", gotInactive)
	}
	if gotInactive.LocationID != nil {
		t.Fatalf("inactive register location_id = %v, want nil", gotInactive.LocationID)
	}
}
