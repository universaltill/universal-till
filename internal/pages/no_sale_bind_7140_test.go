package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#7140: POST /api/pos/no-sale needs a bound, single-use approval
// (token from the refusal body) for a session without cash_adjustment.

func noSaleBody(t *testing.T, reason, pin, approval string) string {
	t.Helper()
	m := map[string]string{}
	if reason != "" {
		m["reason"] = reason
	}
	if pin != "" {
		m["manager_pin"] = pin
	}
	if approval != "" {
		m["approval"] = approval
	}
	b, _ := json.Marshal(m)
	return string(b)
}

type noSaleRefusal struct {
	Error struct {
		Code     string `json:"code"`
		Approval string `json:"approval"`
	} `json:"error"`
}

func decodeNoSaleRefusal(t *testing.T, rec *httptest.ResponseRecorder) noSaleRefusal {
	t.Helper()
	var v noSaleRefusal
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not the JSON envelope: %v (%q)", err, rec.Body.String())
	}
	return v
}

func noSaleToken(t *testing.T, mux *http.ServeMux, reason string) string {
	t.Helper()
	rec := postNoSale(t, mux, cashier3134, noSaleBody(t, reason, "", ""))
	v := decodeNoSaleRefusal(t, rec)
	if rec.Code != http.StatusForbidden || v.Error.Code != "manager_pin_required" || v.Error.Approval == "" {
		t.Fatalf("prompt = %d %s, want 403 manager_pin_required with error.approval", rec.Code, rec.Body.String())
	}
	return v.Error.Approval
}

func noSaleBindEnv(t *testing.T) (*http.ServeMux, string, func() []byte) {
	t.Helper()
	mux, _, kicked := noSaleBindEnvDeps(t)
	return mux, "", kicked
}

func noSaleBindEnvDeps(t *testing.T) (*http.ServeMux, *common.Deps, func() []byte) {
	t.Helper()
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	mux, dp, dev := newNoSaleTestDeps(t, "device")
	return mux, dp, func() []byte { b, _ := os.ReadFile(dev); return b }
}

// AC15
func TestNoSaleBind7140_RefusalCarriesTokenNoKick(t *testing.T) {
	mux, _, kicked := noSaleBindEnv(t)
	for _, body := range []string{`{}`, `{"reason":"float"}`} {
		rec := postNoSale(t, mux, cashier3134, body)
		v := decodeNoSaleRefusal(t, rec)
		if rec.Code != http.StatusForbidden || v.Error.Code != "manager_pin_required" || v.Error.Approval == "" {
			t.Fatalf("%s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(kicked()) != 0 {
		t.Fatalf("drawer kicked on a refusal")
	}
}

// AC16, AC17
func TestNoSaleBind7140_ApprovedRetryKicksOnceReplayRefused(t *testing.T) {
	mux, dp, kicked := noSaleBindEnvDeps(t)
	tok := noSaleToken(t, mux, "float")
	rec := postNoSale(t, mux, cashier3134, noSaleBody(t, "float", perm3134MgrPIN, tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("approved retry = %d %s, want 200", rec.Code, rec.Body.String())
	}
	n := len(kicked())
	if n == 0 {
		t.Fatalf("drawer not kicked")
	}
	rec = postNoSale(t, mux, cashier3134, noSaleBody(t, "float", perm3134MgrPIN, tok))
	if v := decodeNoSaleRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("replay = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if len(kicked()) != n {
		t.Fatalf("replay kicked the drawer again")
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 1 {
		t.Fatalf("replay wrote an event row: %+v", rows)
	}
	if rows := noSaleAudit(t, dp.Db); len(rows) != 1 {
		t.Fatalf("replay wrote an audit row: %+v", rows)
	}
}

// AC18
func TestNoSaleBind7140_EditedReasonRefusedWhitespaceAccepted(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	tok := noSaleToken(t, mux, "float")
	rec := postNoSale(t, mux, cashier3134, noSaleBody(t, "other", perm3134MgrPIN, tok))
	if v := decodeNoSaleRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("edited reason = %d %s, want approval_changed", rec.Code, rec.Body.String())
	}
	assertNothingRecorded(t, dp)
	tok = noSaleToken(t, mux, "float")
	rec = postNoSale(t, mux, cashier3134, noSaleBody(t, "  float ", perm3134MgrPIN, tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("whitespace-only difference = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// AC18 (other user)
func TestNoSaleBind7140_OtherSessionUserRefused(t *testing.T) {
	mux, _, kicked := noSaleBindEnv(t)
	tok := noSaleToken(t, mux, "")
	other := cashier3134
	other.ID = "cashier-other"
	rec := postNoSale(t, mux, other, noSaleBody(t, "", perm3134MgrPIN, tok))
	if v := decodeNoSaleRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("other user = %d %s, want approval_changed", rec.Code, rec.Body.String())
	}
	if len(kicked()) != 0 {
		t.Fatalf("kicked")
	}
}

// AC19
func TestNoSaleBind7140_WrongPINLockoutIs429(t *testing.T) {
	mux, _, kicked := noSaleBindEnv(t)
	tok := noSaleToken(t, mux, "")
	var last *httptest.ResponseRecorder
	for i := 0; i < 20; i++ {
		last = postNoSale(t, mux, cashier3134, noSaleBody(t, "", "000000", tok))
		if last.Code == http.StatusTooManyRequests {
			break
		}
		if v := decodeNoSaleRefusal(t, last); last.Code != http.StatusForbidden || v.Error.Code != "invalid_pin" || v.Error.Approval != tok {
			t.Fatalf("try %d = %d %s, want 403 invalid_pin with the same token", i, last.Code, last.Body.String())
		}
	}
	if v := decodeNoSaleRefusal(t, last); last.Code != http.StatusTooManyRequests || v.Error.Code != "locked_out" {
		t.Fatalf("lockout = %d %s, want 429 locked_out", last.Code, last.Body.String())
	}
	if len(kicked()) != 0 {
		t.Fatalf("kicked")
	}
}

// AC20
func TestNoSaleBind7140_OneStepPINNoLongerApproves(t *testing.T) {
	mux, dp, kicked := noSaleBindEnv(t)
	_ = dp
	rec := postNoSale(t, mux, cashier3134, noSaleBody(t, "", perm3134MgrPIN, ""))
	if v := decodeNoSaleRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("one-step = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if len(kicked()) != 0 {
		t.Fatalf("kicked")
	}
}

// AC22: the token is spent at the gate; a failed kick needs a fresh prompt.
func TestNoSaleBind7140_KickFailureConsumesToken(t *testing.T) {
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	mux, dp, _ := newNoSaleTestDeps(t, "broken")
	tok := noSaleToken(t, mux, "")
	rec := postNoSale(t, mux, cashier3134, noSaleBody(t, "", perm3134MgrPIN, tok))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("kick failure = %d %s, want 503", rec.Code, rec.Body.String())
	}
	rec = postNoSale(t, mux, cashier3134, noSaleBody(t, "", perm3134MgrPIN, tok))
	if v := decodeNoSaleRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("retry after failed kick = %d %s, want approval_changed", rec.Code, rec.Body.String())
	}
	assertNothingRecorded(t, dp)
}

// Review Low 1: the approval is bound to the register the event will be
// recorded under; a till whose register identity changed between prompt and
// retry must get a fresh prompt, not an open drawer under the new register.
func TestNoSaleBind7140_RegisterChangeRefused(t *testing.T) {
	mux, dp, kicked := noSaleBindEnvDeps(t)
	tok := noSaleToken(t, mux, "float")
	if _, err := dp.Db.Exec(`INSERT INTO registers (id, name) VALUES ('reg-other', 'Other till')`); err != nil {
		t.Fatal(err)
	}
	setSettings(t, dp, map[string]string{pos.SettingsKeyTillRegisterID: "reg-other"})
	rec := postNoSale(t, mux, cashier3134, noSaleBody(t, "float", perm3134MgrPIN, tok))
	if v := decodeNoSaleRefusal(t, rec); rec.Code != http.StatusForbidden || v.Error.Code != "approval_changed" {
		t.Fatalf("register changed = %d %s, want 403 approval_changed", rec.Code, rec.Body.String())
	}
	if len(kicked()) != 0 {
		t.Fatalf("drawer kicked after the register changed")
	}
	assertNothingRecorded(t, dp)
}
