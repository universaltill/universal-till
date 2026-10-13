package pages

import (
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#4022: a register keeps its till's name. On the main till (where
// registers is written; it is primary-wins synced), renaming the main till
// renames its own register, and a joined till's reported rename renames the
// register enrolment named after it. A name another register holds is left
// alone and never fails the till rename.

func registerName(t *testing.T, dp *common.Deps, id string) string {
	t.Helper()
	var name string
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT name FROM registers WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	return name
}

func insertRegister(t *testing.T, dp *common.Deps, id, name string) {
	t.Helper()
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO registers (id, name, is_active) VALUES (?, ?, 1)`, id, name); err != nil {
		t.Fatal(err)
	}
}

func TestTillNameEndpoint_MainTillRenamesItsOwnRegister(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if _, err := d.Db.ExecContext(ctx, `DELETE FROM registers`); err != nil {
		t.Fatal(err)
	}
	insertRegister(t, d, "reg-main", "Front")
	insertRegister(t, d, "reg-joined", "Terrace")
	if err := d.Settings.Set(ctx, "till.name", "Front"); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "reg-main"); err != nil {
		t.Fatal(err)
	}

	if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"Window"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := registerName(t, d, "reg-main"); got != "Window" {
		t.Fatalf("own register = %q, want Window", got)
	}
	if got := registerName(t, d, "reg-joined"); got != "Terrace" {
		t.Fatalf("another till's register renamed: %q", got)
	}
}

func TestTillNameEndpoint_JoinedTillLeavesRegistersAlone(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	joinedTillWithRoster(t, d)
	ctx := t.Context()
	if _, err := d.Db.ExecContext(ctx, `DELETE FROM registers`); err != nil {
		t.Fatal(err)
	}
	insertRegister(t, d, "reg-back", "Back Office")
	if err := d.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "reg-back"); err != nil {
		t.Fatal(err)
	}
	// The joined till's own rename writes through to the main till, which
	// is unreachable here; whatever the outcome, the replica's synced
	// registers copy must not be written (the main till owns it).
	_ = postForm(mux, "/api/settings/till-name", url.Values{"name": {"Patio"}}, &mgrUser)
	if got := registerName(t, d, "reg-back"); got != "Back Office" {
		t.Fatalf("a joined till wrote its synced registers copy: %q", got)
	}
}

func TestCloudRenameTill_MainTillRenamesItsOwnRegister(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `DELETE FROM registers`); err != nil {
		t.Fatal(err)
	}
	// No persisted identity: the single active register is the main till's.
	insertRegister(t, dp, "reg-default", "Front Counter")
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudRenameTill(ctx, dp, "Bar Till"); err != nil {
		t.Fatalf("cloudRenameTill: %v", err)
	}
	if got := registerName(t, dp, "reg-default"); got != "Bar Till" {
		t.Fatalf("own register = %q, want Bar Till", got)
	}
}

func TestCloudRenameTill_RegisterNameTakenKeepsTillRename(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `DELETE FROM registers`); err != nil {
		t.Fatal(err)
	}
	insertRegister(t, dp, "reg-main", "Front Counter")
	insertRegister(t, dp, "reg-spare", "Bar Till")
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "reg-main"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudRenameTill(ctx, dp, "Bar Till"); err != nil {
		t.Fatalf("cloudRenameTill must not fail over its register: %v", err)
	}
	if got := settingValue(t, dp, "till.name"); got != "Bar Till" {
		t.Fatalf("till.name = %q", got)
	}
	if got := registerName(t, dp, "reg-main"); got != "Front Counter" {
		t.Fatalf("register took a taken name: %q", got)
	}
}

func TestApplyReportedTillName_RenamesTheJoinedTillsRegister(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	tills := data.NewTillsRepo(dp.Db)
	posRepo := data.NewPOSRepo(dp.Db)
	id, err := tills.InsertTill(ctx, "Bar", "hash-bar")
	if err != nil {
		t.Fatal(err)
	}
	// A manager's own "Bar" existed first, so enrolment suffixed the
	// joined till's register (CreateRegisterForEnrolment) — the review
	// finding: a lookup by the till's old name would rename the wrong one.
	insertRegister(t, dp, "reg-manager", "Bar")
	insertRegister(t, dp, "reg-till", "Bar (2)")
	if err := posRepo.InsertAudit(ctx, nil, "system", "till", id, "till_enrolled",
		map[string]any{"name": "Bar", "register_id": "reg-till", "register_name": "Bar (2)"}, "2026-10-13T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	var unusable sync.Map
	applyReportedTillName(tills, posRepo, &unusable, id, "Garden")
	if got := registerName(t, dp, "reg-till"); got != "Garden" {
		t.Fatalf("joined till's register = %q, want Garden", got)
	}
	if got := registerName(t, dp, "reg-manager"); got != "Bar" {
		t.Fatalf("the manager's same-named register was renamed: %q", got)
	}
	// A repeated report changes nothing.
	applyReportedTillName(tills, posRepo, &unusable, id, "Garden")
	if got := registerName(t, dp, "reg-till"); got != "Garden" {
		t.Fatalf("after repeat = %q", got)
	}
}

// An enrolment from before ut-docs#894 recorded no register: nothing is
// renamed.
func TestApplyReportedTillName_NoEnrolledRegisterRenamesNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	tills := data.NewTillsRepo(dp.Db)
	id, err := tills.InsertTill(ctx, "Terrace", "hash-terrace")
	if err != nil {
		t.Fatal(err)
	}
	insertRegister(t, dp, "reg-terrace", "Terrace")
	var unusable sync.Map
	applyReportedTillName(tills, data.NewPOSRepo(dp.Db), &unusable, id, "Garden")
	if got := registerName(t, dp, "reg-terrace"); got != "Terrace" {
		t.Fatalf("renamed without an enrolment record: %q", got)
	}
}
