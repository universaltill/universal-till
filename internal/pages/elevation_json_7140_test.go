package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#7140: checkOrElevateApproval is the one bound gate; the form
// wrapper and the JSON handlers differ only in where pin/approval come from.

const (
	jsonMgrPIN  = "135790" // newElevationTestDeps manager
	jsonCashPIN = "246800" // newElevationTestDeps cashier
)

func jsonGateReq(user string) *http.Request {
	return elevationRequest(auth.User{ID: user, Role: "cashier"})
}

func jsonBinding(user, reason string) string {
	return approvalBinding("/api/test", user, []string{"refund"}, []elevationHiddenField{{"reason", reason}})
}

// AC1: mint, then the same binding + token + manager PIN elevates once.
func TestCheckOrElevateApproval_MintThenConsumeOnce(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	dp := newElevationTestDeps(t)
	b := jsonBinding("c1", "r")

	got, tok := checkOrElevateApproval(dp, jsonGateReq("c1"), b, "", "", "refund")
	if got.Outcome != needsElevation || got.Err != nil || tok == "" {
		t.Fatalf("first call = %+v tok %q, want needsElevation, nil Err, a token", got, tok)
	}
	got, left := checkOrElevateApproval(dp, jsonGateReq("c1"), b, jsonMgrPIN, tok, "refund")
	if got.Outcome != elevated || left != "" {
		t.Fatalf("approved call = %+v tok %q, want elevated and no token", got, left)
	}
	got, _ = checkOrElevateApproval(dp, jsonGateReq("c1"), b, jsonMgrPIN, tok, "refund")
	if got.Outcome != needsElevation || !errors.Is(got.Err, errApprovalStale) {
		t.Fatalf("replay = %+v, want errApprovalStale", got)
	}
}

// AC2/AC3/AC5: a stale, missing, forged or other-user token is refused before
// the PIN lookup (5 wrong-PIN tries must not lock the manager out).
func TestCheckOrElevateApproval_StaleNeverReachesPINLookup(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	dp := newElevationTestDeps(t)
	b := jsonBinding("c1", "r")
	_, tok := checkOrElevateApproval(dp, jsonGateReq("c1"), b, "", "", "refund")

	cases := map[string]struct{ binding, tok, user string }{
		"changed value": {jsonBinding("c1", "other"), tok, "c1"},
		"missing token": {b, "", "c1"},
		"forged token":  {b, "AAAAAAAAAAAAAAAAAAAAAA", "c1"},
		"other user":    {jsonBinding("c2", "r"), tok, "c2"},
	}
	for name, c := range cases {
		for i := 0; i < 5; i++ {
			got, _ := checkOrElevateApproval(dp, jsonGateReq(c.user), c.binding, "000000", c.tok, "refund")
			if !errors.Is(got.Err, errApprovalStale) {
				t.Fatalf("%s try %d: Err = %v, want errApprovalStale", name, i, got.Err)
			}
		}
	}
	// The manager is not locked out and the original token still works.
	got, _ := checkOrElevateApproval(dp, jsonGateReq("c1"), b, jsonMgrPIN, tok, "refund")
	if got.Outcome != elevated {
		t.Fatalf("after 20 stale tries: %+v, want elevated (no lockout tick)", got)
	}
}

// AC4: a wrong PIN returns the same, still-valid token.
func TestCheckOrElevateApproval_WrongPINKeepsToken(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	dp := newElevationTestDeps(t)
	b := jsonBinding("c1", "r")
	_, tok := checkOrElevateApproval(dp, jsonGateReq("c1"), b, "", "", "refund")

	got, again := checkOrElevateApproval(dp, jsonGateReq("c1"), b, "000000", tok, "refund")
	if got.Outcome != needsElevation || !errors.Is(got.Err, auth.ErrInvalidPIN) || again != tok {
		t.Fatalf("wrong PIN = %+v tok %q, want ErrInvalidPIN and the same token %q", got, again, tok)
	}
	got, _ = checkOrElevateApproval(dp, jsonGateReq("c1"), b, jsonCashPIN, tok, "refund")
	if got.Outcome != needsElevation || got.Err == nil {
		t.Fatalf("cashier PIN = %+v, want refused", got)
	}
	got, _ = checkOrElevateApproval(dp, jsonGateReq("c1"), b, jsonMgrPIN, tok, "refund")
	if got.Outcome != elevated {
		t.Fatalf("retry with the right PIN = %+v, want elevated", got)
	}
}

func TestElevationJSONCode(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, "manager_pin_required"},
		{errApprovalStale, "approval_changed"},
		{auth.ErrInvalidPIN, "invalid_pin"},
		{auth.ErrLockedOut, "locked_out"},
		{errAuthServiceUnavailable, "elevation_unavailable"},
		{errStepUpCrossOrigin, "elevation_unavailable"},
		{errApproverInsufficientRole, "insufficient_role"},
		{errors.New("boom"), "insufficient_role"},
	} {
		if got := elevationJSONCode(c.err); got != c.want {
			t.Errorf("elevationJSONCode(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestWriteElevationRefusalJSON_Shape(t *testing.T) {
	for _, c := range []struct {
		name       string
		err        error
		tok        string
		wantStatus int
		wantCode   string
	}{
		{"prompt", nil, "tok1", 403, "manager_pin_required"},
		{"stale", errApprovalStale, "tok2", 403, "approval_changed"},
		{"locked 429", auth.ErrLockedOut, "tok3", 429, "locked_out"},
		{"no token", auth.ErrInvalidPIN, "", 403, "invalid_pin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			writeElevationRefusalJSON(rec, r, elevationCheck{Outcome: needsElevation, Err: c.err}, c.tok, http.StatusTooManyRequests)
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			var env map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("not JSON: %v", err)
			}
			if v, ok := env["data"]; !ok || v != nil {
				t.Fatalf("data = %v, want present null", v)
			}
			e, _ := env["error"].(map[string]any)
			if e["code"] != c.wantCode || e["message"] == "" || e["message"] == nil {
				t.Fatalf("error = %v, want code %q and a message", e, c.wantCode)
			}
			if tok, has := e["approval"]; c.tok == "" && has || c.tok != "" && tok != c.tok {
				t.Fatalf("approval = %v (present %v), want %q", tok, has, c.tok)
			}
		})
	}
}
