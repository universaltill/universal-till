package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- register stock location from my. (ut-docs#3642) ---

func registerByID(t *testing.T, dp *common.Deps, id string) data.RegisterAdmin {
	t.Helper()
	regs, err := data.NewPOSRepo(dp.Db).ListRegistersForAdmin(t.Context())
	if err != nil {
		t.Fatalf("list registers: %v", err)
	}
	for _, r := range regs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("register %s not found", id)
	return data.RegisterAdmin{}
}

func registerAuditCount(t *testing.T, dp *common.Deps, id string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'register' AND entity_id = ? AND action = 'register_update' AND actor_id = 'system'`,
		id,
	).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func regLoc(r data.RegisterAdmin) string {
	if r.LocationID == nil {
		return ""
	}
	return *r.LocationID
}

// Assign then unassign, each through POSRepo.SetRegisterLocation, audited
// as register/register_update under "system" like the /registers page's
// own edit; asking for the current location again writes nothing.
func TestCloudSetRegisterLocation_AssignUnassignIdempotent(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	backID, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	regID, err := repo.CreateRegister(ctx, "Front till", nil)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := cloudSetRegisterLocation(ctx, dp, " "+regID+" ", " "+backID+" ")
	if err != nil || msg != "register Front till now takes stock from Back room" {
		t.Fatalf("assign: msg=%q err=%v", msg, err)
	}
	if got := regLoc(registerByID(t, dp, regID)); got != backID {
		t.Fatalf("location = %q, want %q", got, backID)
	}
	if registerAuditCount(t, dp, regID) != 1 {
		t.Fatal("assign not audited")
	}

	// Idempotent: the current location again is a no-op success, no audit.
	msg, err = cloudSetRegisterLocation(ctx, dp, regID, backID)
	if err != nil || msg != "register Front till already takes stock from Back room" {
		t.Fatalf("repeat assign: msg=%q err=%v", msg, err)
	}
	if registerAuditCount(t, dp, regID) != 1 {
		t.Fatal("a no-op assign must not audit")
	}

	msg, err = cloudSetRegisterLocation(ctx, dp, regID, "")
	if err != nil || msg != "register Front till unassigned" {
		t.Fatalf("unassign: msg=%q err=%v", msg, err)
	}
	if r := registerByID(t, dp, regID); r.LocationID != nil {
		t.Fatalf("location = %v, want nil", *r.LocationID)
	}
	if registerAuditCount(t, dp, regID) != 2 {
		t.Fatal("unassign not audited")
	}
	msg, err = cloudSetRegisterLocation(ctx, dp, regID, "  ")
	if err != nil || msg != "register Front till already unassigned" {
		t.Fatalf("repeat unassign: msg=%q err=%v", msg, err)
	}
	if registerAuditCount(t, dp, regID) != 2 {
		t.Fatal("a no-op unassign must not audit")
	}
}

func TestCloudSetRegisterLocation_RefusesUnknownRegisterOrLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	regID, err := repo.CreateRegister(ctx, "Front till", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudSetRegisterLocation(ctx, dp, "no-such-register", "loc_main"); err == nil || !strings.Contains(err.Error(), "no-such-register") {
		t.Fatalf("unknown register: err = %v, want one naming it", err)
	}
	if _, err := cloudSetRegisterLocation(ctx, dp, regID, "no-such-location"); err == nil || !strings.Contains(err.Error(), "no-such-location") {
		t.Fatalf("unknown location: err = %v, want one naming it", err)
	}
	if r := registerByID(t, dp, regID); r.LocationID != nil {
		t.Fatal("refused assign wrote")
	}
	if registerAuditCount(t, dp, regID) != 0 {
		t.Fatal("refused assign was audited")
	}
}

// An inactive location is refused — the /registers picker only offers
// active ones — unless it is the register's current location, which is a
// no-op success (the picker keeps the current one too, ut-docs#895).
func TestCloudSetRegisterLocation_InactiveLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	backID, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStockLocationActive(ctx, backID, false); err != nil {
		t.Fatal(err)
	}
	freeID, err := repo.CreateRegister(ctx, "Front till", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cloudSetRegisterLocation(ctx, dp, freeID, backID)
	if err == nil || err.Error() != "stock location Back room is inactive" {
		t.Fatalf("inactive location: err = %v", err)
	}
	if r := registerByID(t, dp, freeID); r.LocationID != nil || registerAuditCount(t, dp, freeID) != 0 {
		t.Fatal("refused assign wrote or audited")
	}

	// A register already on the (since retired) location: re-asking is a
	// no-op success.
	stuckID, err := repo.CreateRegister(ctx, "Back till", &backID)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := cloudSetRegisterLocation(ctx, dp, stuckID, backID)
	if err != nil || msg != "register Back till already takes stock from Back room" {
		t.Fatalf("current inactive location: msg=%q err=%v", msg, err)
	}
	if registerAuditCount(t, dp, stuckID) != 0 {
		t.Fatal("a no-op must not audit")
	}
}

// registers sync primary-wins: a real write is refused on a satellite.
func TestCloudSetRegisterLocation_RefusedOnSatellite(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	regID, err := data.NewPOSRepo(dp.Db).CreateRegister(ctx, "Front till", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudSetRegisterLocation(ctx, dp, regID, "loc_main"); err == nil {
		t.Fatal("assign on a satellite must be refused")
	}
	if r := registerByID(t, dp, regID); r.LocationID != nil || registerAuditCount(t, dp, regID) != 0 {
		t.Fatal("refused satellite assign wrote or audited")
	}
}

func TestBuildCloudHooks_WiresSetRegisterLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.SetRegisterLocation == nil {
		t.Fatal("SetRegisterLocation hook not wired")
	}
	regID, err := data.NewPOSRepo(dp.Db).CreateRegister(ctx, "Front till", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.SetRegisterLocation(ctx, regID, "loc_main"); err != nil {
		t.Fatalf("wired assign: %v", err)
	}
	if got := regLoc(registerByID(t, dp, regID)); got != "loc_main" {
		t.Fatalf("wired assign wrote %q", got)
	}
}
