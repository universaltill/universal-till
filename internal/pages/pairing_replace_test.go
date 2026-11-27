package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const devC = "till-cccccccc-cccc-4ccc-8ccc-cccccccccccc"

// pairRequestAndApprove files a pair request from device dev, approves it as the
// manager (optionally choosing replaceOf) and returns the enrolment token the
// approval carries. UT_AUTH=off, as the other pairing tests.
func pairRequestAndApprove(t *testing.T, mux *http.ServeMux, dp *common.Deps, name, dev, replaceOf, role string) (tok string, approve *httptest.ResponseRecorder) {
	t.Helper()
	rec := postPairRequestFromDevice(t, mux, name, commitOf("s-"+name), dev, "10.0.0.5:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair request: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	form := url.Values{}
	if replaceOf != "" {
		form.Set("replace_of", replaceOf)
	}
	if role != "" {
		form.Set("role", role)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	approve = httptest.NewRecorder()
	mux.ServeHTTP(approve, req)
	if approve.Code != http.StatusOK {
		return "", approve
	}
	if err := dp.Db.QueryRow(`SELECT token FROM pending_pairings WHERE id = ?`, created.Data.ID).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	return tok, approve
}

func quietTill(t *testing.T, dp *common.Deps, tillID string) {
	t.Helper()
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339), tillID); err != nil {
		t.Fatal(err)
	}
}

// The owner's scenario (2026-11-27): one phone joined as "phone till", then
// "iphone-till", then "phone-till2". Each time the manager said "this is the
// same device: replace". Result: ONE tills row, ONE register, sales untouched.
func TestRejoinThreeNamesOneRowOneRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	tok, rec := pairRequestAndApprove(t, mux, dp, "phone till", devA, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve 1: %d %s", rec.Code, rec.Body.String())
	}
	first := parseEnrol(t, enrolForTest(t, mux, tok, "phone till", devA))
	if _, err := dp.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES ('s1','R1',0,0,?,'synced')`, first.RegisterID); err != nil {
		t.Fatal(err)
	}

	for i, step := range []struct{ name, dev string }{{"iphone-till", devB}, {"phone-till2", devC}} {
		quietTill(t, dp, first.TillID)
		tok, rec := pairRequestAndApprove(t, mux, dp, step.name, step.dev, first.TillID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("approve %d: %d %s", i+2, rec.Code, rec.Body.String())
		}
		got := parseEnrol(t, enrolForTest(t, mux, tok, step.name, step.dev))
		if got.TillID != first.TillID || got.RegisterID != first.RegisterID {
			t.Fatalf("%s: till %s register %s, want %s / %s", step.name, got.TillID, got.RegisterID, first.TillID, first.RegisterID)
		}
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 1 {
		t.Fatalf("tills rows = %d, want 1 for the one phone", n)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills WHERE name = 'phone-till2'`); n != 1 {
		t.Fatalf("the row must carry the newest name")
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM registers`); n != 2 {
		t.Fatalf("registers = %d, want 2 (main + the phone's one)", n)
	}
	var regName string
	_ = dp.Db.QueryRow(`SELECT name FROM registers WHERE id = ?`, first.RegisterID).Scan(&regName)
	if regName != "phone-till2" {
		t.Fatalf("register name = %q, want phone-till2 (follows the till)", regName)
	}
	l := linkOf(t, dp, devC)
	if l.RegisterID != first.RegisterID || l.SupersededBy != devB {
		t.Fatalf("link = %+v, want register %s superseded_by %s", l, first.RegisterID, devB)
	}
	if n := rowCount(t, dp, `SELECT is_active FROM registers WHERE id = ?`, first.RegisterID); n != 1 {
		t.Fatal("the phone's register must be active")
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM sales WHERE register_id = ?`, first.RegisterID); n != 1 {
		t.Fatalf("sales on the register = %d, want 1 (history stays)", n)
	}
	if row, ok, _ := data.NewTillsRepo(dp.Db).TillByDeviceID(t.Context(), devC); !ok || row.ID != first.TillID {
		t.Fatalf("device %s must now map to the one till row", devC)
	}
}

// A live row can never be offered or replaced, even by a forced post.
func TestApproveReplaceOfRefusesLive(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	tok, _ := pairRequestAndApprove(t, mux, dp, "Phone", devA, "", "")
	first := parseEnrol(t, enrolForTest(t, mux, tok, "Phone", devA))
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339), first.TillID); err != nil {
		t.Fatal(err)
	}
	_, rec := pairRequestAndApprove(t, mux, dp, "Other", devB, first.TillID, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "in use right now") {
		t.Fatalf("approve replace of a live till: %d %s, want 409 in use right now", rec.Code, rec.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM pending_pairings WHERE status = 'approved' AND device_name = 'Other'`); n != 0 {
		t.Fatal("a refused replace must store no approval")
	}
}

// A satellite cannot be replaced by a request that wants another role.
func TestApproveReplaceOfKeepsRole(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	id, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Kitchen", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE tills SET role = 'satellite', last_seen_at = ? WHERE id = ?`, time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	_, rec := pairRequestAndApprove(t, mux, dp, "Phone", devA, id, "additional")
	if rec.Code != http.StatusConflict {
		t.Fatalf("replace of a satellite by an additional request: %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// A choice made on request A is carried by A's token only: B's token enrols a new row.
func TestApproveTokenBindsChoice(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	tok, _ := pairRequestAndApprove(t, mux, dp, "Phone", devA, "", "")
	first := parseEnrol(t, enrolForTest(t, mux, tok, "Phone", devA))
	quietTill(t, dp, first.TillID)
	_, recA := pairRequestAndApprove(t, mux, dp, "A-new", devB, first.TillID, "")
	if recA.Code != http.StatusOK {
		t.Fatalf("approve A: %d", recA.Code)
	}
	tokB, _ := pairRequestAndApprove(t, mux, dp, "B-new", devC, "", "")
	b := parseEnrol(t, enrolForTest(t, mux, tokB, "B-new", devC))
	if b.TillID == first.TillID {
		t.Fatal("B's token must not use A's replace choice")
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 2 {
		t.Fatalf("tills = %d, want 2 (phone + B)", n)
	}
}

// Between approve and enrol the old till came back: the replace is refused.
func TestEnrolReplaceRefusedWhenTheTillCameBack(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	tok, _ := pairRequestAndApprove(t, mux, dp, "Phone", devA, "", "")
	first := parseEnrol(t, enrolForTest(t, mux, tok, "Phone", devA))
	quietTill(t, dp, first.TillID)
	tok2, rec := pairRequestAndApprove(t, mux, dp, "Phone2", devB, first.TillID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d", rec.Code)
	}
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), first.TillID); err != nil {
		t.Fatal(err)
	}
	out := enrolForTest(t, mux, tok2, "Phone2", devB)
	if out.Code < 400 {
		t.Fatalf("enrol replaced a till that came back: %d %s", out.Code, out.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 1 {
		t.Fatalf("tills = %d, want 1 (nothing added, nothing replaced)", n)
	}
}

func pendingCardBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pending card: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// Same device id as an old entry: the card asks "same device?" with a Yes
// (replace) that carries the till id, and a No (new device).
func TestPendingCardOffersSameDeviceByDeviceID(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)
	tok, _ := pairRequestAndApprove(t, mux, dp, "phone till", devA, "", "")
	first := parseEnrol(t, enrolForTest(t, mux, tok, "phone till", devA))
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339), first.TillID); err != nil {
		t.Fatal(err)
	}
	if rec := postPairRequestFromDevice(t, mux, "phone-till2", commitOf("c2"), devA, "10.0.0.6:1234"); rec.Code != http.StatusOK {
		t.Fatalf("request: %d", rec.Code)
	}
	body := pendingCardBody(t, mux)
	for _, want := range []string{
		`Is this the same device as &#34;phone till&#34;? Replace it.`,
		`name="replace_of" value="` + first.TillID + `"`,
		`Yes, same device: replace &#34;phone till&#34;`,
		`No, this is a new device`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("card lacks %q:\n%s", want, body)
		}
	}
}

// No device match: a select lists only non-live, same-role old entries.
func TestPendingCardOffersCandidatesWithoutDeviceID(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)
	repo := data.NewTillsRepo(dp.Db)
	ins := func(name, role string, ago time.Duration) string {
		id, err := repo.InsertTill(t.Context(), name, "h-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dp.Db.Exec(`UPDATE tills SET role = ?, last_seen_at = ? WHERE id = ?`, role, time.Now().Add(-ago).UTC().Format(time.RFC3339), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldID := ins("phone till", "additional", 3*24*time.Hour)
	ins("iphone-till", "additional", 40*24*time.Hour)
	ins("busy-till", "additional", 2*time.Minute)
	ins("kitchen-sat", "satellite", 5*24*time.Hour)
	if rec := postPairRequestFromDevice(t, mux, "phone-till2", commitOf("c3"), devC, "10.0.0.7:1234"); rec.Code != http.StatusOK {
		t.Fatalf("request: %d", rec.Code)
	}
	body := pendingCardBody(t, mux)
	if !strings.Contains(body, "Replace an old entry?") || !strings.Contains(body, `<option value="`+oldID+`"`) {
		t.Fatalf("no candidate select:\n%s", body)
	}
	for _, name := range []string{"phone till", "iphone-till"} {
		if !strings.Contains(body, name+", last seen") {
			t.Fatalf("candidate %q missing:\n%s", name, body)
		}
	}
	for _, bad := range []string{"busy-till", "kitchen-sat", "Is this the same device"} {
		if strings.Contains(body, bad) {
			t.Fatalf("%q must not be offered:\n%s", bad, body)
		}
	}
}

// No candidates: no select, plain Approve.
func TestPendingCardNoSelectWithoutCandidates(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)
	postPairRequestFromDevice(t, mux, "phone-till2", commitOf("c4"), devC, "10.0.0.8:1234")
	body := pendingCardBody(t, mux)
	if strings.Contains(body, "Replace an old entry?") {
		t.Fatalf("a select with nothing to pick:\n%s", body)
	}
}
