package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4717/#4720: every operator-initiated registration collects the
// cloud-terms acceptance first and sends it with the register request.

type termsCloud struct {
	calls atomic.Int32
	mu    sync.Mutex
	body  map[string]any
}

func (c *termsCloud) sent() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

// newTermsCloud fakes a cloud that registers successfully and records the
// request body. It also resets the enroll package state around the test.
func newTermsCloud(t *testing.T, d *common.Deps) *termsCloud {
	t.Helper()
	c := &termsCloud{}
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.mu.Lock()
		c.body = b
		c.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-t","merchant_id":"m-t","token":"tok-t"}}`))
	})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	d.Cfg.Marketplace.PublicKey = strings.Repeat("ab", 32) // no signing-key fetch
	nameShop(t, d)
	return c
}

func settingVal(t *testing.T, d *common.Deps, key string) string {
	t.Helper()
	v, _, _ := d.Settings.Get(t.Context(), key)
	return v
}

func TestEnrolNow_WithoutTermsTickDoesNotRegisterAndSaysWhy(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newTermsCloud(t, d)

	rec := postForm(mux, "/api/enrol/now", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) {
		t.Fatalf("untick register = %d %s, want 200 with the error span", rec.Code, body)
	}
	if !strings.Contains(body, "Accept the Universal Till cloud service terms") {
		t.Fatalf("missing the terms-required message: %s", body)
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("cloud called %d times without an acceptance", n)
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
		t.Fatalf("terms_version = %q recorded without a tick", v)
	}
}

// Validation runs before the elevation check: a cashier who forgot the tick
// gets the message, not a PIN prompt.
func TestEnrolNow_TermsCheckedBeforeElevation(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = newTermsCloud(t, d)
	rec := postForm(mux, "/api/enrol/now", url.Values{}, &cashUser)
	if strings.Contains(rec.Body.String(), "elevation-dialog") || !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("cashier without tick = %s, want the terms message, not a PIN prompt", rec.Body.String())
	}
}

func TestEnrolNow_ElevationPromptCarriesTheTick(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newTermsCloud(t, d)
	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &cashUser)
	body := rec.Body.String()
	if !strings.Contains(body, "elevation-dialog") || !strings.Contains(body, `name="accept_terms"`) {
		t.Fatalf("elevation prompt lacks the accept_terms hidden field: %s", body)
	}
	if cloud.calls.Load() != 0 || settingVal(t, d, "marketplace.terms_version") != "" {
		t.Fatal("an unapproved request registered or recorded the acceptance")
	}
}

func TestEnrolNow_WithTermsTickRecordsAcceptanceAndSendsIt(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newTermsCloud(t, d)

	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "store-t") {
		t.Fatalf("ticked register = %d %s, want registered", rec.Code, rec.Body.String())
	}
	if got := cloud.sent()["terms_version"]; got != enroll.TermsVersion {
		t.Fatalf("register request terms_version = %v, want %q", got, enroll.TermsVersion)
	}
	if got := cloud.sent()["terms_locale"]; got != "en" {
		t.Fatalf("register request terms_locale = %v, want \"en\"", got)
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
		t.Fatalf("stored terms_version = %q", v)
	}
	if v := settingVal(t, d, "marketplace.terms_accepted_at"); v == "" {
		t.Fatal("terms_accepted_at not stored")
	}
	var payload string
	if err := d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'enrol_now_registered'`).Scan(&payload); err != nil || !strings.Contains(payload, enroll.TermsVersion) {
		t.Fatalf("audit payload %q (err %v) lacks terms_version", payload, err)
	}
}

// Once the current version is accepted the card shows the accepted line,
// not the checkbox, and Register now needs no tick.
func TestEnrolNow_AlreadyAcceptedNeedsNoTick(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newTermsCloud(t, d)
	if err := enroll.RecordTermsAcceptance(t.Context(), d.Settings, "en"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/enrol/now", url.Values{}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "store-t") || cloud.calls.Load() != 1 {
		t.Fatalf("accepted register = %s (calls %d)", rec.Body.String(), cloud.calls.Load())
	}
	if got := cloud.sent()["terms_version"]; got != enroll.TermsVersion {
		t.Fatalf("terms_version = %v", got)
	}
}

func TestSettingsPage_RegistrationCardShowsTermsStep(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	body := get()
	for _, want := range []string{`name="accept_terms"`, enroll.TermsURL("en"), `rel="noopener"`, "version " + enroll.TermsVersion} {
		if !strings.Contains(body, want) {
			t.Fatalf("unaccepted settings page lacks %q", want)
		}
	}
	if err := enroll.RecordTermsAcceptance(t.Context(), d.Settings, "en"); err != nil {
		t.Fatal(err)
	}
	body = get()
	if strings.Contains(body, `name="accept_terms"`) {
		t.Fatal("accepted settings page still shows the checkbox")
	}
	if !strings.Contains(body, "Terms accepted: version "+enroll.TermsVersion) {
		t.Fatalf("accepted settings page lacks the accepted line")
	}
}

func TestSettingsAutoRegisterOn_NeedsTermsTick(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newTermsCloud(t, d)

	rec := postForm(mux, "/api/settings/auto-register", url.Values{"optIn": {"on"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("X-UT-Response") != "refused" ||
		!strings.Contains(rec.Body.String(), "Accept the Universal Till cloud service terms") {
		t.Fatalf("untick toggle = %d %q %q, want a marked, translated refusal", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); ok {
		t.Fatal("refused toggle saved the opt-in")
	}
	if cloud.calls.Load() != 0 {
		t.Fatal("refused toggle registered")
	}

	rec = postForm(mux, "/api/settings/auto-register", url.Values{"optIn": {"on"}, "accept_terms": {"on"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ticked toggle = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if v := settingVal(t, d, common.KeyAutoRegisterOptIn); v != "true" {
		t.Fatalf("opt-in = %q, want true", v)
	}
	if cloud.calls.Load() != 1 || cloud.sent()["terms_version"] != enroll.TermsVersion {
		t.Fatalf("registration carried %v (calls %d), want terms_version", cloud.sent(), cloud.calls.Load())
	}
}

func TestSettingsAutoRegisterOff_NeverNeedsTerms(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = newTermsCloud(t, d)
	rec := postForm(mux, "/api/settings/auto-register", url.Values{}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("toggle off = %d %s, want 204", rec.Code, rec.Body.String())
	}
}

func TestSetupWizardOptInWithoutTermsDoesNotRegister(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"GB"}, "currency": {"GBP"},
		"store_name": {"Corner Shop"}, "till_name": {"Till 1"}, "auto_register": {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard = %d %s", rec.Code, rec.Body.String())
	}
	if v := settingVal(t, d, common.KeyAutoRegisterOptIn); v != "false" {
		t.Fatalf("opt-in = %q, want \"false\" without acceptance", v)
	}
	if hits := mkt.storeRegisterHits(); hits != 0 {
		t.Fatalf("register attempts = %d, want 0 without acceptance", hits)
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
		t.Fatalf("terms_version = %q recorded without a tick", v)
	}
}

func TestSetupWizardOptInWithTermsRecordsAndRegisters(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"GB"}, "currency": {"GBP"},
		"store_name": {"Corner Shop"}, "till_name": {"Till 1"}, "auto_register": {"on"}, "accept_terms": {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard = %d %s", rec.Code, rec.Body.String())
	}
	if v := settingVal(t, d, common.KeyAutoRegisterOptIn); v != "true" {
		t.Fatalf("opt-in = %q, want true", v)
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
		t.Fatalf("terms_version = %q", v)
	}
	if hits := mkt.storeRegisterHits(); hits != 1 {
		t.Fatalf("register attempts = %d, want 1", hits)
	}
}

func TestSetupWizardRendersTermsCheckboxTiedToOptIn(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := getSetup(mux, "?lang=en", "").Body.String()
	i := strings.Index(body, `name="accept_terms"`)
	if i < 0 {
		t.Fatal("setup wizard has no accept_terms checkbox")
	}
	tag := body[i : i+strings.Index(body[i:], ">")]
	if strings.Contains(tag, "checked") {
		t.Fatal("accept_terms is pre-ticked")
	}
	if !strings.Contains(body, enroll.TermsURL("en")) {
		t.Fatal("setup wizard does not link the terms")
	}
	// The step is tied to the opt-in: shown and required only while
	// auto_register is ticked (the driven run checks the behaviour; this pins
	// the wiring so dropping it fails here too).
	for _, want := range []string{
		`name="auto_register" x-model="autoReg"`,
		`x-show="autoReg"`,
		`name="accept_terms" :required="autoReg"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup wizard lacks %s", want)
		}
	}
}

// Review finding (ut-docs#4720): the wizard records the language the
// operator read the terms in, not the shop country's default language.
func TestSetupWizardRecordsTheTermsLanguageShownNotTheCountryDefault(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup?lang=en", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"DE"}, "currency": {"EUR"},
		"store_name": {"Eckladen"}, "till_name": {"Kasse 1"}, "auto_register": {"on"}, "accept_terms": {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard = %d %s", rec.Code, rec.Body.String())
	}
	if v := settingVal(t, d, "marketplace.terms_locale"); v != "en" {
		t.Fatalf("terms_locale = %q, want en (the language the terms were shown in)", v)
	}
}
