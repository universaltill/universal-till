package pages

import (
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5898: the joining till applies the same name-length rule as the
// main till (#5875): the three join inputs cap at maxTillNameRunes, and every
// server-side entry refuses a longer name with sync.error.name_too_long
// before any request to the main till.

func tcLongName() string { return strings.Repeat("é", maxTillNameRunes+1) }

func tcWantNotTooLong(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(html.UnescapeString(body), httpx.T("en", keyTillNameTooLong)) {
		t.Fatalf("body shows the too-long message for an acceptable name: %s", body)
	}
}

func TestPairStart_OverLongNameRefusedLocally(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	srv := tcPrimaryNeverHit(t)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)
	for label, name := range map[string]string{"bare": tcLongName(), "padded": "  " + tcLongName() + "  "} {
		t.Run(label, func(t *testing.T) {
			rec := postPairStart(t, rmux, srv.URL, "some-till-id", name)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			tcWantTooLong(t, rec.Body.String())
			if strings.Contains(rec.Body.String(), "hx-trigger") {
				t.Fatal("a refused name must not start polling")
			}
		})
	}
	// Characters win over length.
	rec := postPairStart(t, rmux, srv.URL, "some-till-id", tcLongName()+"‮")
	tcWantMsg(t, rec.Body.String())
}

func TestPairStart_SixtyRuneNameNotRefusedForLength(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(main.Close)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)
	name := "  " + strings.Repeat("é", maxTillNameRunes) + "  "
	rec := postPairStart(t, rmux, main.URL, "some-till-id", name)
	tcWantNotTooLong(t, rec.Body.String())
}

func TestSyncJoin_OverLongNameRefusedLocally(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	for _, path := range []string{"/api/sync/join", "/api/setup/join"} {
		for label, name := range map[string]string{"bare": tcLongName(), "padded": "  " + tcLongName() + "  "} {
			t.Run(path+" "+label, func(t *testing.T) {
				srv := tcPrimaryNeverHit(t)
				replica, _ := newSyncDepsWithPath(t, "replica.db")
				rmux := http.NewServeMux()
				replica.AuthSvc = auth.NewService(replica.Db)
				registerSyncAPI(rmux, replica)
				code := `{"url":"` + srv.URL + `","token":"whatever"}`
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path,
					strings.NewReader(url.Values{"code": {code}, "name": {name}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rmux.ServeHTTP(rec, req)
				tcWantTooLong(t, rec.Body.String())
				if strings.Contains(html.UnescapeString(rec.Body.String()), httpx.T("en", "tills.join_error.refused")) {
					t.Fatalf("shows the generic refused text: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestSetupWizard_OverLongTillNameShowsLengthMessage(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/setup", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"},
		"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"},
		"store_name": {"Corner Shop"}, "till_name": {tcLongName()},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want a 200 wizard re-render", rec.Code)
	}
	body := rec.Body.String()
	if want := template.HTMLEscapeString(httpx.T("en", keyTillNameTooLong)); !strings.Contains(body, `<p class="login-error" role="alert">`+want+`</p>`) {
		t.Errorf("re-render lacks the %q error: %s", want, body)
	}
	if strings.Contains(body, `<p class="login-error" role="alert">`+html.EscapeString(httpx.T("en", "tills.pairing.error.name_required_field"))) {
		t.Error("an over-long name must not show the name-required text")
	}
	if !strings.Contains(body, "step: 4,") {
		t.Error("re-render must open on the shop/till-name step (4)")
	}
	if strings.Contains(body, tcLongName()) {
		t.Error("a refused till name must not be echoed back")
	}
	if fb, _ := svc.NeedsFirstBoot(t.Context()); !fb {
		t.Fatal("a refused wizard submit must not create an operator")
	}
	for _, k := range []string{"till.name", "store.name"} {
		if v, ok, _ := d.Settings.Get(t.Context(), k); ok && v != "" {
			t.Fatalf("%s = %q written by a refused submit", k, v)
		}
	}
}

func TestCloudPair_OverLongNameShowsLengthMessage(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-pair", nonce,
		url.Values{"code": {pairPageCode}, "name": {tcLongName()}}, nil))
	tcWantTooLong(t, rec.Body.String())
	if strings.Contains(html.UnescapeString(rec.Body.String()), httpx.T("en", "tills.pairing.error.name_required_field")) {
		t.Fatalf("shows name-required text: %s", rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Fatal("paired anyway")
	}
	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("the cloud was contacted %d times", n)
	}
}

// The three join inputs carry maxlength = maxTillNameRunes (the literal in
// the template is pinned to the Go const so they cannot drift).
func TestJoinInputsCapNameAtLimit(t *testing.T) {
	limit := `maxlength="` + strconv.Itoa(maxTillNameRunes) + `"`
	inputOf := func(t *testing.T, body, id string) string {
		t.Helper()
		i := strings.Index(body, `id="`+id+`"`)
		if i < 0 {
			t.Fatalf("no input %s", id)
		}
		start := strings.LastIndex(body[:i], "<input")
		end := i + strings.Index(body[i:], ">")
		return body[start : end+1]
	}
	t.Run("tills page", func(t *testing.T) {
		t.Setenv("UT_AUTH", "off")
		mux, _ := newSyncAPITestDeps(t)
		if in := inputOf(t, getTillsPage(t, mux), "pairing-till-name"); !strings.Contains(in, limit) {
			t.Errorf("%s lacks %s", in, limit)
		}
	})
	t.Run("setup wizard", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		registerSyncAPI(mux, d)
		body := getSetup(mux, "?lang=en", "").Body.String()
		if in := inputOf(t, body, "setup-pairing-till-name"); !strings.Contains(in, limit) {
			t.Errorf("%s lacks %s", in, limit)
		}
	})
	t.Run("join notice", func(t *testing.T) {
		t.Setenv("UT_AUTH", "off")
		mux, dp, _ := newPairingAPITestDeps(t)
		registerJoinNoticeUI(mux, dp)
		withJoinCandidate(t, dp, joinNoticeMain)
		if in := inputOf(t, getJoinNotice(t, mux).Body.String(), "join-notice-name"); !strings.Contains(in, limit) {
			t.Errorf("%s lacks %s", in, limit)
		}
	})
}
