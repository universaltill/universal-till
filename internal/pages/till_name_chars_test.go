package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5862: a till/device name is a single line with no hidden formatting
// characters (data.CheckSingleLineName, the #5858 rule). Operator-typed entry
// points refuse with common.error.name_invalid_characters before anything is
// persisted or any network call is made; machine-facing ones refuse silently.

const tcLoneIsolate = "Bar⁦x" // unbalanced isolate

func tcBadNames() map[string]string {
	return map[string]string{
		"rlo":           "Bar‮abc",
		"newline":       "Bar\nabc",
		"line sep":      "Bar abc",
		"zero width":    "Bar​abc",
		"lone isolate":  tcLoneIsolate,
		"tab":           "Bar\tabc",
		"escape":        "Bar\x1babc",
		"ends with rlo": "Till‮",
	}
}

// tcGoodNames are names the rule must keep accepting (RTL tills).
func tcGoodNames() map[string]string {
	return map[string]string{
		"balanced isolate": "Kasse ⁧קופה⁩",
		"rlm":              "Kasse‏",
		"zwj":              "Ka‍sse",
	}
}

func tcWantMsg(t *testing.T, body string) {
	t.Helper()
	want := httpx.T("en", "common.error.name_invalid_characters")
	if want == "common.error.name_invalid_characters" {
		t.Fatal("i18n not initialised: key did not resolve")
	}
	if !strings.Contains(html.UnescapeString(body), want) {
		t.Fatalf("body = %q, want the translated name text %q", body, want)
	}
}

func TestValidateTillName_SingleLineRule(t *testing.T) {
	for label, bad := range tcBadNames() {
		t.Run(label, func(t *testing.T) {
			if _, err := validateTillName(bad); !errors.Is(err, data.ErrNameInvalidCharacters) {
				t.Fatalf("validateTillName(%q) err = %v, want ErrNameInvalidCharacters", bad, err)
			}
		})
	}
	for label, good := range tcGoodNames() {
		t.Run(label, func(t *testing.T) {
			if got, err := validateTillName("  " + good + " "); err != nil || got != strings.TrimSpace(good) {
				t.Fatalf("validateTillName(%q) = %q, %v", good, got, err)
			}
		})
	}
	// Blank and over-long stay refused, but are not the characters error.
	for label, v := range map[string]string{"blank": "  ", "61 runes": strings.Repeat("é", maxTillNameRunes+1)} {
		_, err := validateTillName(v)
		if err == nil || errors.Is(err, data.ErrNameInvalidCharacters) {
			t.Fatalf("%s: err = %v, want a non-characters refusal", label, err)
		}
	}
}

func TestTillNameEndpoint_InvalidCharactersRefused(t *testing.T) {
	for label, bad := range tcBadNames() {
		t.Run(label, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			ctx := t.Context()
			if _, err := d.Db.ExecContext(ctx, `DELETE FROM registers`); err != nil {
				t.Fatal(err)
			}
			insertRegister(t, d, "reg-main", "Front")
			if err := d.Settings.Set(ctx, "till.name", "Front"); err != nil {
				t.Fatal(err)
			}
			if err := d.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "reg-main"); err != nil {
				t.Fatal(err)
			}
			// A cashier: the elevation gate would prompt for a PIN, but a bad
			// name is refused before it (ut-docs#557).
			for _, u := range []*struct{ name string }{{"manager"}, {"cashier"}} {
				user := mgrUser
				if u.name == "cashier" {
					user = cashUser
				}
				rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {bad}}, &user)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status = %d, want 422: %s", u.name, rec.Code, rec.Body.String())
				}
				tcWantMsg(t, rec.Body.String())
				if rec.Header().Get("X-UT-Response") != "refused" {
					t.Fatalf("%s: X-UT-Response = %q, want refused", u.name, rec.Header().Get("X-UT-Response"))
				}
				if strings.Contains(strings.ToLower(rec.Body.String()), "pin") {
					t.Fatalf("%s: an elevation prompt was rendered: %s", u.name, rec.Body.String())
				}
			}
			if got := settingValue(t, d, "till.name"); got != "Front" {
				t.Fatalf("till.name = %q, want it unchanged", got)
			}
			if a := renameTillAudits(t, d); len(a) != 0 {
				t.Fatalf("a refused rename was audited: %+v", a)
			}
			if got := registerName(t, d, "reg-main"); got != "Front" {
				t.Fatalf("own register = %q, want Front", got)
			}
		})
	}
}

func TestTillNameEndpoint_InvalidCharactersRefusedOnJoinedTill(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	joinedTillWithRoster(t, d)
	rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"Bar‮abc"}}, &mgrUser)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	tcWantMsg(t, rec.Body.String())
	if got := settingValue(t, d, "sync.till_name"); got != "Back Office" {
		t.Fatalf("sync.till_name = %q, want it unchanged", got)
	}
}

func TestTillNameEndpoint_BidiSafeNamesAccepted(t *testing.T) {
	for label, good := range tcGoodNames() {
		t.Run(label, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"  " + good + "  "}}, &mgrUser)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
			}
			if got := settingValue(t, d, "till.name"); got != good {
				t.Fatalf("till.name = %q, want %q", got, good)
			}
		})
	}
}

func TestCloudRenameTill_RefusesInvisibleCharacters(t *testing.T) {
	for label, bad := range tcBadNames() {
		t.Run(label, func(t *testing.T) {
			dp := newCloudSyncTestDeps(t)
			ctx := t.Context()
			if _, err := dp.Db.ExecContext(ctx, `DELETE FROM registers`); err != nil {
				t.Fatal(err)
			}
			insertRegister(t, dp, "reg-main", "Front Counter")
			if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
				t.Fatal(err)
			}
			if _, err := cloudRenameTill(ctx, dp, bad); err == nil {
				t.Fatalf("%q accepted", bad)
			}
			if got := settingValue(t, dp, "till.name"); got != "Front Counter" {
				t.Fatalf("till.name = %q", got)
			}
			if got := registerName(t, dp, "reg-main"); got != "Front Counter" {
				t.Fatalf("register = %q", got)
			}
		})
	}
}

func TestSetupWizard_TillNameInvisibleCharsRefused(t *testing.T) {
	for label, bad := range tcBadNames() {
		t.Run(label, func(t *testing.T) {
			mux, svc, d := newFullAuthDeps(t)
			rec := postForm(mux, "/api/setup", url.Values{
				"pin": {"2468"}, "pin_confirm": {"2468"},
				"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"},
				"store_name": {"Corner Shop"}, "till_name": {bad},
			}, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d loc=%q, want a 200 wizard re-render", rec.Code, rec.Header().Get("Location"))
			}
			body := rec.Body.String()
			if want := template.HTMLEscapeString(httpx.T("en", "common.error.name_invalid_characters")); !strings.Contains(body, `<p class="login-error" role="alert">`+want+`</p>`) {
				t.Errorf("re-render lacks the %q error: %s", want, body)
			}
			if strings.Contains(body, `<p class="login-error" role="alert">`+html.EscapeString(httpx.T("en", "tills.pairing.error.name_required_field"))) {
				t.Error("an invalid-characters name must not show the name-required text")
			}
			if !strings.Contains(body, "step: 4,") {
				t.Error("re-render must open on the shop/till-name step (4)")
			}
			if fb, _ := svc.NeedsFirstBoot(t.Context()); !fb {
				t.Fatal("a refused wizard submit must not create an operator")
			}
			if v, ok, _ := d.Settings.Get(t.Context(), "till.name"); ok && v != "" {
				t.Fatalf("till.name = %q written by a refused submit", v)
			}
			if v, ok, _ := d.Settings.Get(t.Context(), "store.name"); ok && v != "" {
				t.Fatalf("store.name = %q written by a refused submit", v)
			}
		})
	}
}

func TestCloudPair_TillNameInvisibleCharsRefused(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	for label, bad := range tcBadNames() {
		t.Run(label, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-pair", nonce,
				url.Values{"code": {pairPageCode}, "name": {bad}}, nil))
			tcWantMsg(t, rec.Body.String())
			if rec.Header().Get("HX-Redirect") != "" {
				t.Fatalf("paired anyway: %s", rec.Body.String())
			}
		})
	}
	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("the cloud was contacted %d times for refused names", n)
	}
}

// tcPrimaryNeverHit is a stand-in main till that fails the test if contacted.
func tcPrimaryNeverHit(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the main till was contacted (%s %s) for a refused name", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSyncJoin_InvisibleCharsNameRefusedLocally(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, path := range []string{"/api/sync/join", "/api/setup/join"} {
		for label, bad := range tcBadNames() {
			t.Run(path+" "+label, func(t *testing.T) {
				srv := tcPrimaryNeverHit(t)
				replica, _ := newSyncDepsWithPath(t, "replica.db")
				rmux := http.NewServeMux()
				replica.AuthSvc = auth.NewService(replica.Db) // /api/setup/join checks first boot
				registerSyncAPI(rmux, replica)
				code := `{"url":"` + srv.URL + `","token":"whatever"}`
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path,
					strings.NewReader(url.Values{"code": {code}, "name": {bad}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rmux.ServeHTTP(rec, req)
				tcWantMsg(t, rec.Body.String())
			})
		}
	}
}

func TestPairStart_InvisibleCharsNameRefusedLocally(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for label, bad := range tcBadNames() {
		t.Run(label, func(t *testing.T) {
			srv := tcPrimaryNeverHit(t)
			replica, _ := newSyncDepsWithPath(t, "replica.db")
			rmux := http.NewServeMux()
			registerPairingJoinAPI(rmux, replica)
			rec := postPairStart(t, rmux, srv.URL, "some-till-id", bad)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (htmx does not swap non-2xx): %s", rec.Code, rec.Body.String())
			}
			tcWantMsg(t, rec.Body.String())
			if strings.Contains(rec.Body.String(), "hx-trigger") {
				t.Fatal("a refused name must not start polling")
			}
		})
	}
}

func tcEnrol(t *testing.T, mux http.Handler, token, name string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "name": name})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncEnroll_InvisibleCharsNameRefusedBeforeTokenSpent(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	mux, dp := newSyncAPITestDeps(t)
	code := issueEnrolCode(t, mux, "http://192.168.1.10:8080")
	_, token, err := decodeEnrollCode(code)
	if err != nil || token == "" {
		t.Fatalf("decode code: %v", err)
	}
	for label, bad := range tcBadNames() {
		rec := tcEnrol(t, mux, token, bad)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (never 422, which means name_taken): %s", label, rec.Code, rec.Body.String())
		}
		tcWantMsg(t, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tills`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tills rows = %d, %v; want none", n, err)
	}
	// The same token still works with a clean name: it was not spent.
	rec := tcEnrol(t, mux, token, "Bar")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry with a valid name = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			RegisterID string `json:"register_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.RegisterID == "" {
		t.Fatalf("enrol response = %s (%v), want a register_id", rec.Body.String(), err)
	}
}

func TestPairRequest_InvisibleCharsDeviceNameRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	mux, dp, _ := newPairingAPITestDeps(t)
	i := 0
	for label, bad := range tcBadNames() {
		i++ // a distinct source each: the endpoint rate-limits per address
		rec := postPairRequest(t, mux, bad, commitOf("secret-1"), fmt.Sprintf("10.0.0.%d:1234", i))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", label, rec.Code, rec.Body.String())
		}
		tcWantMsg(t, rec.Body.String())
	}
	pending, err := data.NewPairingRepo(dp.Db).ListPending(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v; want none", pending, err)
	}
}

func TestApplyReportedTillName_InvisibleCharsKeepRowAndRegister(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	tills := data.NewTillsRepo(dp.Db)
	posRepo := data.NewPOSRepo(dp.Db)
	id, err := tills.InsertTill(ctx, "Bar", "hash-bar")
	if err != nil {
		t.Fatal(err)
	}
	insertRegister(t, dp, "reg-till", "Bar")
	if err := posRepo.InsertAudit(ctx, nil, "system", "till", id, "till_enrolled",
		map[string]any{"name": "Bar", "register_id": "reg-till", "register_name": "Bar"}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		t.Fatal(err)
	}
	var unusable sync.Map
	for range 2 {
		applyReportedTillName(tills, posRepo, &unusable, id, "Bar‮")
	}
	if got := registerName(t, dp, "reg-till"); got != "Bar" {
		t.Fatalf("register = %q, want Bar", got)
	}
	var name string
	if err := dp.Db.QueryRowContext(ctx, `SELECT name FROM tills WHERE id = ?`, id).Scan(&name); err != nil || name != "Bar" {
		t.Fatalf("tills.name = %q, %v; want Bar", name, err)
	}
}
