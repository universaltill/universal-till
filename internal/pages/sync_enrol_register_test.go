package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const (
	devA = "till-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	devB = "till-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func rowCount(t *testing.T, dp *common.Deps, q string, args ...any) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

type enrolOut struct {
	TillID     string
	RegisterID string
}

func parseEnrol(t *testing.T, rec *httptest.ResponseRecorder) enrolOut {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			TillID     string `json:"till_id"`
			RegisterID string `json:"register_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return enrolOut{out.Data.TillID, out.Data.RegisterID}
}

func revokeTill(t *testing.T, mux *http.ServeMux, id string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+id+"/revoke", nil))
	if rec.Code >= 300 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
}

func linkOf(t *testing.T, dp *common.Deps, dev string) data.RegisterDevice {
	t.Helper()
	l, ok, err := data.NewPOSRepo(dp.Db).RegisterByDevice(t.Context(), dev)
	if err != nil || !ok {
		t.Fatalf("no register link for %s (%v)", dev, err)
	}
	return l
}

// AC1/AC3: a device never seen gets exactly one register, linked to it.
func TestEnrolNewDeviceCreatesOneRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	out := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Phone", devA))
	// the main till's own register (pinned at enrolment) + the phone's = 2
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM registers`); n != 2 {
		t.Fatalf("registers = %d, want 2 (main + phone)", n)
	}
	if l := linkOf(t, dp, devA); l.RegisterID != out.RegisterID || l.TillID != out.TillID {
		t.Fatalf("link = %+v, want register %s till %s", l, out.RegisterID, out.TillID)
	}
}

// AC3: the same device re-joins after its till was removed and a manager
// approved the request naming it: its register is taken over, never doubled.
func TestEnrolSameDeviceNewNameAfterRevokeTakesOverRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	first := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "phone till", devA))
	revokeTill(t, mux, first.TillID)
	if got := rowCount(t, dp, `SELECT is_active FROM registers WHERE id = ?`, first.RegisterID); got != 0 {
		t.Fatalf("revoke left the register active (is_active=%d)", got)
	}
	second := parseEnrol(t, enrolApprovedForDevice(t, mux, dp, "phone-till2", devA))
	if second.RegisterID != first.RegisterID {
		t.Fatalf("register after rejoin = %s, want the same %s", second.RegisterID, first.RegisterID)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM registers`); n != 2 {
		t.Fatalf("registers = %d, want 2 (no second register for the device)", n)
	}
	var active int
	var name, reason string
	if err := dp.Db.QueryRow(`SELECT r.is_active, r.name, d.retired_reason FROM registers r JOIN register_devices d ON d.register_id = r.id WHERE r.id = ?`, first.RegisterID).Scan(&active, &name, &reason); err != nil {
		t.Fatal(err)
	}
	if active != 1 || name != "phone-till2" || reason != "" {
		t.Fatalf("active=%d name=%q retired_reason=%q, want 1 phone-till2 \"\"", active, name, reason)
	}
	if l := linkOf(t, dp, devA); l.TillID != second.TillID {
		t.Fatalf("link till = %s, want the new till %s", l.TillID, second.TillID)
	}
}

// AC13: a self-asserted device id never moves a register. A second enrol
// asserting dev-A while its till is live (no approval) gets its own register,
// unlinked; the live device's register and link are untouched.
func TestEnrolSameDeviceLiveRowNoProofDoesNotTakeOver(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	first := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Phone", devA))
	second := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Spoof", devA))
	if second.RegisterID == first.RegisterID || second.RegisterID == "" {
		t.Fatalf("spoofing enrol got register %q, want a register of its own (not %s)", second.RegisterID, first.RegisterID)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM register_devices WHERE device_id = ?`, devA); n != 1 {
		t.Fatalf("link rows for dev-A = %d, want 1", n)
	}
	if l := linkOf(t, dp, devA); l.RegisterID != first.RegisterID || l.TillID != first.TillID {
		t.Fatalf("the live device's link moved: %+v", l)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM registers WHERE id = ? AND is_active = 1`, first.RegisterID); n != 1 {
		t.Fatal("the live device's register was touched")
	}
}

// A takeover also needs the owning till to be gone: a live owner with a
// manager approval is the rename path, never a second link.
func TestEnrolWithoutDeviceIdBehavesAsToday(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	out := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Old build", ""))
	if out.RegisterID == "" {
		t.Fatal("no register created")
	}
	var dev string
	if err := dp.Db.QueryRow(`SELECT device_id FROM register_devices WHERE register_id = ?`, out.RegisterID).Scan(&dev); err != nil || dev != "" {
		t.Fatalf("link device_id = %q (%v), want \"\"", dev, err)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM registers`); n != 2 {
		t.Fatalf("registers = %d, want 2", n)
	}
}

// AC2/AC6: the replace branch keeps the register; the link follows the new
// device and remembers the old one.
func TestReEnrolReplaceKeepsRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	first := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Phone", devA))
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339), first.TillID); err != nil {
		t.Fatal(err)
	}
	// same name, a manager approved THIS name (the replace rule), new device
	rec := enrolApprovedForDevice(t, mux, dp, "Phone", devB)
	second := parseEnrol(t, rec)
	if second.TillID != first.TillID || second.RegisterID != first.RegisterID {
		t.Fatalf("replace gave till %s register %s, want %s / %s", second.TillID, second.RegisterID, first.TillID, first.RegisterID)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM registers`); n != 2 {
		t.Fatalf("registers = %d, want 2", n)
	}
	l := linkOf(t, dp, devB)
	if l.RegisterID != first.RegisterID || l.SupersededBy != devA {
		t.Fatalf("link = %+v, want register %s superseded_by %s", l, first.RegisterID, devA)
	}
}

// AC8: revoking a till retires its register; the till security action never
// fails because of the register half.
func TestRevokeRetiresItsRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	a := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Phone", devA))
	if _, err := dp.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES ('s1','R1',0,0,?,'synced')`, a.RegisterID); err != nil {
		t.Fatal(err)
	}
	revokeTill(t, mux, a.TillID)
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills WHERE id = ?`, a.TillID); n != 0 {
		t.Fatal("till still enrolled")
	}
	var active int
	var reason string
	if err := dp.Db.QueryRow(`SELECT r.is_active, d.retired_reason FROM registers r JOIN register_devices d ON d.register_id = r.id WHERE r.id = ?`, a.RegisterID).Scan(&active, &reason); err != nil || active != 0 || reason != "revoked" {
		t.Fatalf("active=%d reason=%q err=%v, want 0 revoked", active, reason, err)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM sales WHERE register_id = ?`, a.RegisterID); n != 1 {
		t.Fatalf("the register's sale count = %d, want 1 (history stays)", n)
	}
}

// A guarded refusal (unsent sales) is audited and the till is revoked anyway.
func TestRevokeStillRevokesWhenTheRegisterCannotBeRetired(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	a := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Phone", devA))
	if _, err := dp.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES ('s1','R1',0,0,?,'queued')`, a.RegisterID); err != nil {
		t.Fatal(err)
	}
	revokeTill(t, mux, a.TillID)
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills WHERE id = ?`, a.TillID); n != 0 {
		t.Fatal("till still enrolled")
	}
	if n := rowCount(t, dp, `SELECT is_active FROM registers WHERE id = ?`, a.RegisterID); n != 1 {
		t.Fatal("a register with unsent sales was retired")
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'register_retire_skipped' AND entity_id = ?`, a.RegisterID); n != 1 {
		t.Fatalf("register_retire_skipped audit rows = %d, want 1", n)
	}
}

// Withdrawing a join deletes the unused register and its link with it.
func TestWithdrawRemovesRegisterLinkToo(t *testing.T) {
	_, dp := newSyncAPITestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	id, _, err := repo.CreateRegisterForEnrolment(t.Context(), "Temp")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkRegisterDevice(t.Context(), nil, id, devA, "t1", data.RegisterLinkEnrol); err != nil {
		t.Fatal(err)
	}
	removed, err := repo.DeleteUnusedEnrolmentRegister(t.Context(), id)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v, want true nil", removed, err)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM register_devices`); n != 0 {
		t.Fatalf("link rows left = %d", n)
	}
}

// AC5: a rename follows the link row and never creates a register.
func TestRenameDoesNotCreateRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	a := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "Phone", devA))
	// drop the audit row so only the link can find the register
	if _, err := dp.Db.Exec(`DELETE FROM audit_log WHERE action = 'till_enrolled'`); err != nil {
		t.Fatal(err)
	}
	before := rowCount(t, dp, `SELECT COUNT(*) FROM registers`)
	var unusable sync.Map
	applyReportedTillName(data.NewTillsRepo(dp.Db), data.NewPOSRepo(dp.Db), &unusable, a.TillID, "Garden")
	if got := registerName(t, dp, a.RegisterID); got != "Garden" {
		t.Fatalf("register name = %q, want Garden", got)
	}
	if after := rowCount(t, dp, `SELECT COUNT(*) FROM registers`); after != before {
		t.Fatalf("registers %d -> %d, a rename must not create one", before, after)
	}
}
