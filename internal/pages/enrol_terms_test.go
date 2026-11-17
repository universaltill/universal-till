package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// ADR-0200 (ut-docs#6559): Register now and the auto-register toggle are gone
// (the till registers itself, and sends no terms version on that path). The
// cloud terms are still accepted from the REGISTERED card (POST
// /api/enrol/terms, tested in enrol_terms_registered_test.go), at a plugin
// download (enrol_terms_store_test.go) and at claim.

func TestSettingsPage_RegisteredCardShowsTermsStep(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = newRegisteredTill(t, d)
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

// The not-registered card has no terms step and no Register button any more.
func TestSettingsPage_UnregisteredCardHasNoRegisterButtonOrTermsStep(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = newTermsCloud(t, d)
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, bad := range []string{`name="accept_terms"`, `/api/enrol/now`, `id="enrol-msg"`} {
		if strings.Contains(body, bad) {
			t.Errorf("unregistered settings page still carries %q", bad)
		}
	}
	if !strings.Contains(body, "This till is not registered yet.") {
		t.Error("unregistered settings page lost its status line")
	}
}
