package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#7140: voiding through POST /api/pos/sale/status needs a bound,
// single-use approval (token from the refusal body), not a reusable PIN.

type voidRefusal struct {
	Data  any `json:"data"`
	Error struct {
		Code     string `json:"code"`
		Approval string `json:"approval"`
	} `json:"error"`
}

func decodeVoidRefusal(t *testing.T, rec *httptest.ResponseRecorder) voidRefusal {
	t.Helper()
	var v voidRefusal
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("refusal is not the JSON envelope: %v (%q)", err, rec.Body.String())
	}
	return v
}

func voidBody(t *testing.T, id, reason, pin, approval string) string {
	t.Helper()
	m := map[string]string{"saleId": id, "status": "voided"}
	if reason != "" {
		m["reason"] = reason
	}
	if pin != "" {
		m["override_pin"] = pin
	}
	if approval != "" {
		m["approval"] = approval
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func voidSetup(t *testing.T, id string) (*common.Deps, func() (string, int), func(pin, reason, approval string) *httptest.ResponseRecorder, func() string) {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	dp := newElevationTestDeps(t)
	seedCompletedSaleForStatus(t, dp.Db, id)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	cashier := sessionUser(t, dp.Db, "cash1", "cashier")
	state := func() (string, int) { return readSaleStatusAndAudit(t, dp.Db, id) }
	post := func(pin, reason, approval string) *httptest.ResponseRecorder {
		return postSaleStatus(t, mux, cashier, voidBody(t, id, reason, pin, approval))
	}
	token := func() string {
		rec := post("", "", "")
		v := decodeVoidRefusal(t, rec)
		if rec.Code != http.StatusForbidden || v.Error.Code != "manager_pin_required" || v.Error.Approval == "" {
			t.Fatalf("prompt = %d %s", rec.Code, rec.Body.String())
		}
		return v.Error.Approval
	}
	return dp, state, post, token
}

// AC7
func TestSaleStatusVoid7140_RefusalIsJSONWithToken(t *testing.T) {
	_, state, post, _ := voidSetup(t, "v1")
	rec := post("", "", "")
	v := decodeVoidRefusal(t, rec)
	if rec.Code != http.StatusForbidden || v.Data != nil || v.Error.Code != "manager_pin_required" || v.Error.Approval == "" {
		t.Fatalf("got %d %s, want 403 envelope manager_pin_required with error.approval", rec.Code, rec.Body.String())
	}
	if s, a := state(); s != "completed" || a != 0 {
		t.Fatalf("status=%q audits=%d, want completed/0", s, a)
	}
}

// AC8, AC9
func TestSaleStatusVoid7140_ApprovedRetrySpendsTokenOnce(t *testing.T) {
	_, state, post, token := voidSetup(t, "v2")
	tok := token()
	if rec := post("135790", "", tok); rec.Code != http.StatusNoContent {
		t.Fatalf("approved retry = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if s, a := state(); s != "voided" || a != 1 {
		t.Fatalf("status=%q audits=%d, want voided/1", s, a)
	}
	// The sale is voided now: the replay is an impossible move, answered 409
	// before the gate (single-use itself is pinned by the unit and no-sale tests).
	if rec := post("135790", "", tok); rec.Code != http.StatusConflict {
		t.Fatalf("replay = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if _, a := state(); a != 1 {
		t.Fatalf("audits=%d after replay, want 1", a)
	}
}

// AC10
func TestSaleStatusVoid7140_EditedValuesRefusedBeforePIN(t *testing.T) {
	_, state, post, token := voidSetup(t, "v3")
	tok := token()
	rec := post("135790", "different reason", tok)
	if v := decodeVoidRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("edited reason = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if s, a := state(); s != "completed" || a != 0 {
		t.Fatalf("status=%q audits=%d, want completed/0", s, a)
	}
}

// AC10 (other saleId): a token minted for one sale cannot void another.
func TestSaleStatusVoid7140_TokenForOtherSaleRefused(t *testing.T) {
	dp, state, post, token := voidSetup(t, "v3a")
	seedCompletedSaleForStatus(t, dp.Db, "v3b")
	tok := token()
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	cashier := sessionUser(t, dp.Db, "cash1", "cashier")
	_ = post
	rec := postSaleStatus(t, mux, cashier, voidBody(t, "v3b", "", "135790", tok))
	if v := decodeVoidRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("other saleId = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if s, a := readSaleStatusAndAudit(t, dp.Db, "v3b"); s != "completed" || a != 0 {
		t.Fatalf("v3b status=%q audits=%d, want completed/0", s, a)
	}
	if s, a := state(); s != "completed" || a != 0 {
		t.Fatalf("v3a status=%q audits=%d, want completed/0", s, a)
	}
}

// AC11 (gate arm): the sale changed after the token was minted (still a
// legal void source, so the gate, not the 409 pre-check, must refuse it).
func TestSaleStatusVoid7140_StatusChangedSincePromptRefused(t *testing.T) {
	dp, _, post, token := voidSetup(t, "v4")
	if _, err := dp.Db.Exec(`UPDATE sales SET status='parked' WHERE id='v4'`); err != nil {
		t.Fatal(err)
	}
	tok := token()
	if _, err := dp.Db.Exec(`UPDATE sales SET status='open' WHERE id='v4'`); err != nil {
		t.Fatal(err)
	}
	rec := post("135790", "", tok)
	if v := decodeVoidRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("changed status = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if s, a := readSaleStatusAndAudit(t, dp.Db, "v4"); s != "open" || a != 0 {
		t.Fatalf("status=%q audits=%d, want open/0", s, a)
	}
}

// Review Low 2: an impossible move answers 409 before the gate: no prompt,
// no PIN check (no lockout tick), no token minted or spent.
func TestSaleStatusVoid7140_ImpossibleTransition409BeforeGate(t *testing.T) {
	dp, _, post, token := voidSetup(t, "v4b")
	tok := token()
	if _, err := dp.Db.Exec(`UPDATE sales SET status='voided' WHERE id='v4b'`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ pin, tok string }{{"", ""}, {"000000", tok}, {"135790", tok}} {
		rec := post(c.pin, "", c.tok)
		if rec.Code != http.StatusConflict {
			t.Fatalf("pin=%q tok=%q = %d %s, want 409", c.pin, c.tok, rec.Code, rec.Body.String())
		}
	}
	if s, a := readSaleStatusAndAudit(t, dp.Db, "v4b"); s != "voided" || a != 0 {
		t.Fatalf("status=%q audits=%d, want voided/0", s, a)
	}
	// Token untouched and the wrong PIN above ticked no lockout: once the sale
	// is completed again the same token still approves.
	if _, err := dp.Db.Exec(`UPDATE sales SET status='completed' WHERE id='v4b'`); err != nil {
		t.Fatal(err)
	}
	if rec := post("135790", "", tok); rec.Code != http.StatusNoContent {
		t.Fatalf("token after the 409s = %d %s, want 204 (not consumed, no lockout)", rec.Code, rec.Body.String())
	}
}

// AC12
func TestSaleStatusVoid7140_OneStepPINNoLongerApproves(t *testing.T) {
	_, state, post, _ := voidSetup(t, "v5")
	rec := post("135790", "", "")
	if v := decodeVoidRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("one-step PIN = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if s, a := state(); s != "completed" || a != 0 {
		t.Fatalf("status=%q audits=%d, want completed/0", s, a)
	}
}

// AC13
func TestSaleStatusVoid7140_CashierPINRefusedTokenSurvives(t *testing.T) {
	_, _, post, token := voidSetup(t, "v6")
	tok := token()
	rec := post("246800", "", tok)
	v := decodeVoidRefusal(t, rec)
	if rec.Code != http.StatusForbidden || (v.Error.Code != "insufficient_role" && v.Error.Code != "invalid_pin") || v.Error.Approval != tok {
		t.Fatalf("cashier PIN = %d %s, want 403 insufficient_role/invalid_pin with the same token", rec.Code, rec.Body.String())
	}
	if rec := post("135790", "", tok); rec.Code != http.StatusNoContent {
		t.Fatalf("manager PIN with the surviving token = %d %s, want 204", rec.Code, rec.Body.String())
	}
}
