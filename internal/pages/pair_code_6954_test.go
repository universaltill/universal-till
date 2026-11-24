package pages

import (
	"github.com/universaltill/universal-till/internal/enroll"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ut-docs#6954: the code my.universaltill.com shows ("226Y R9H8") typed into
// the Additional till / Satellite join form gets a message that says which
// code goes where, not the generic "type the code shown on the main till".
func TestJoinPrimary_CloudShopCodeSaysWhichCodeGoesWhere(t *testing.T) {
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	r := httptest.NewRequest(http.MethodPost, "/api/setup/join", nil)
	for _, code := range []string{"226Y R9H8", "226y r9h8", " 226Y-R9H8 ", "226YR9H8"} {
		_, err := joinPrimary(r, replica, code, "", "Till 2", "additional", false)
		if k := joinKind(t, err); k != joinErrCloudCode {
			t.Errorf("code %q: kind = %v, want joinErrCloudCode", code, k)
		}
	}
	msg := friendlyJoinError("en", &joinError{kind: joinErrCloudCode})
	for _, want := range []string{"my.universaltill.com", "Main till", "Settings"} {
		if !strings.Contains(msg, want) {
			t.Errorf("cloud-code message %q lacks %q", msg, want)
		}
	}
}

func TestIsCloudShopCode(t *testing.T) {
	for typed, want := range map[string]bool{
		"226Y R9H8": true, "226yr9h8": true, "K7P-4XQ": false, "K7P4XQ": false,
		"": false, "226Y R9H8 1": false, "not-a-code": false, "22 6YR9H8": false, "226Y R9H!": false,
	} {
		if got := isCloudShopCode(typed); got != want {
			t.Errorf("isCloudShopCode(%q) = %v, want %v", typed, got, want)
		}
	}
}

// The reverse slip: a main till's code typed under "Main till" is told to use
// Additional till, and no cloud call is made.
func TestCloudPair_MainTillCodeIsRedirected(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	for _, code := range []string{"K7P-4XQ", "k7p 4xq"} {
		rec := doCloudPairCode(mux, nonce, code)
		body := html.UnescapeString(rec.Body.String())
		if !strings.Contains(body, "Additional till") || rec.Header().Get("HX-Redirect") != "" {
			t.Errorf("code %q: %s", code, body)
		}
	}
	if stub.calls.Load() != 0 {
		t.Fatalf("a main till code reached the cloud (%d calls)", stub.calls.Load())
	}
}

func doCloudPairCode(mux *http.ServeMux, nonce, code string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-pair", nonce, url.Values{"code": {code}, "name": {"Till 1"}}, nil))
	return rec
}

// The cloud's 409s for a device that is already paired / was removed get
// their own message, not "Pairing didn't work".
func TestCloudPairErrorKey_DeviceConflicts(t *testing.T) {
	for code, want := range map[string]string{
		"device_exists":  "setup.cloud.error.device_exists",
		"device_revoked": "setup.cloud.error.device_removed",
	} {
		err := &enroll.RegisterHTTPError{Op: "pair", Status: http.StatusConflict, Code: code}
		if got := cloudPairErrorKey(err); got != want {
			t.Errorf("%s: key = %q, want %q", code, got, want)
		}
	}
}
