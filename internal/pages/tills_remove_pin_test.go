package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// removalDeps is the main till with auth ON, a cashier and a manager (PIN
// perm3134MgrPIN), one enrolled phone with its register.
func removalDeps(t *testing.T) (*http.ServeMux, *common.Deps, enrolOut) {
	t.Helper()
	t.Setenv("UT_AUTH", "off") // minting the enrolment code needs a session; flipped on below
	mux, dp := newShiftsAPITestDeps(t)
	registerSyncAPI(mux, dp)
	registerTillsRevokeStale(mux, dp)
	registerTillsCloudRemove(mux, dp)
	seed3134Users(t, dp.Db)
	out := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "phone till", devA))
	t.Setenv("UT_AUTH", "")
	return mux, dp, out
}

func postRemoval(mux *http.ServeMux, u auth.User, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A cashier session removes a ghost only with a manager PIN: none and a wrong
// one change nothing, the right one removes the till and retires its register.
func TestRemoveOldEntryNeedsManagerPINForACashier(t *testing.T) {
	mux, dp, ph := removalDeps(t)
	path := "/api/sync/tills/" + ph.TillID + "/revoke"

	rec := postRemoval(mux, cashier3134, path, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Enter a manager PIN to remove an entry. Nothing was removed.") {
		t.Fatalf("no PIN: %d %q", rec.Code, rec.Body.String())
	}
	rec = postRemoval(mux, cashier3134, path, url.Values{"manager_pin": {"000000"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "That PIN is not right. Nothing was removed.") {
		t.Fatalf("wrong PIN: %d %q", rec.Code, rec.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills WHERE id = ?`, ph.TillID); n != 1 {
		t.Fatal("a refused removal removed the till")
	}
	rec = postRemoval(mux, cashier3134, path, url.Values{"manager_pin": {perm3134MgrPIN}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("right PIN: %d %q", rec.Code, rec.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills WHERE id = ?`, ph.TillID); n != 0 {
		t.Fatal("till still enrolled after a PIN-approved removal")
	}
	if n := rowCount(t, dp, `SELECT is_active FROM registers WHERE id = ?`, ph.RegisterID); n != 0 {
		t.Fatal("removing the old entry must retire its register too")
	}
	var actor string
	if err := dp.Db.QueryRow(`SELECT actor_id FROM audit_log WHERE action = 'till_revoked' AND entity_id = ?`, ph.TillID).Scan(&actor); err != nil || actor != "mgr1" {
		t.Fatalf("audit actor = %q err=%v, want the PIN's manager mgr1", actor, err)
	}
}

// One PIN rule for every removal (ut-docs#7098 + #7173, owner 2026-11-27): a
// signed-in manager types a manager PIN too; a session alone removes nothing.
func TestRemoveOldEntryManagerSessionStillNeedsAPIN(t *testing.T) {
	mux, dp, ph := removalDeps(t)
	path := "/api/sync/tills/" + ph.TillID + "/revoke"
	rec := postRemoval(mux, manager3134, path, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Enter a manager PIN") {
		t.Fatalf("manager session, no PIN: %d %q", rec.Code, rec.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 1 {
		t.Fatal("a removal without a PIN removed the till")
	}
	// override_pin (the inline panel) and manager_pin (older callers) are the same PIN.
	rec = postRemoval(mux, manager3134, path, url.Values{"override_pin": {perm3134MgrPIN}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager session + PIN: %d %q", rec.Code, rec.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 0 {
		t.Fatal("till not removed")
	}
}

// The bulk button uses the same gate.
func TestRemoveStaleBulkNeedsManagerPINForACashier(t *testing.T) {
	mux, _, _ := removalDeps(t)
	rec := postRemoval(mux, cashier3134, "/api/sync/tills/revoke-stale", url.Values{"id": {"x"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Enter a manager PIN") {
		t.Fatalf("bulk without PIN: %d %q", rec.Code, rec.Body.String())
	}
}
