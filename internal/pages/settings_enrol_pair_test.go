package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3523, ADR-0116 D5/D6: Settings → "Pair with a shop" posts the
// owner's pairing code to POST /api/enrol/pair — elevation-gated like
// POST /api/enrol/now, always 200 with an HTMX fragment, the code never in
// the audit row.

const (
	pairPageCode  = "K7QX-M2RP"
	pairPageStore = "store-paired"
	pairPageToken = "9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b"
)

// newPairCloud fakes the cloud's POST /api/v1/stores/pair, answering for
// pairPageStore.
func newPairCloud(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != pairPageCode {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"code_invalid"}}`))
			return
		}
		// ut-cloud's handler shape: the credential is data.device_token (ADR-0116).
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"store_id": pairPageStore, "merchant_id": pairPageStore, "device_id": body["device_id"], "device_token": pairPageToken,
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, calls
}

// pairDeps boots enrolment over deps' own settings, pointed at a fake
// cloud, and resets the package-wide enrolment state afterwards.
func pairDeps(t *testing.T, d *common.Deps) *atomic.Int32 {
	t.Helper()
	srv, calls := newPairCloud(t)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	return calls
}

// setenvPinnedStore pins this till's store id through the environment for
// one test. Its enroll reset is registered before t.Setenv, so it runs after
// the variable is restored: pairDeps' own reset runs first (cleanups are
// LIFO) while the variable is still set, which re-pinned enroll's
// process-wide store id for whichever test ran next (pages-shuffle seed
// 1791386291628608919, ut-docs#5305).
func setenvPinnedStore(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	t.Setenv("UT_MARKETPLACE_STORE_ID", "store-pinned-by-env")
}

func TestEnrolPair_DeniedSessionGetsPromptCarryingCode(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	calls := pairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &cashUser)
	assertElevationPrompt(t, "/api/enrol/pair", rec.Code, rec.Body.String())
	body := rec.Body.String()
	if !strings.Contains(body, `#pair-msg`) || !strings.Contains(body, `/api/enrol/pair`) {
		t.Fatalf("prompt does not retry into #pair-msg via /api/enrol/pair: %s", body)
	}
	// The dialog's form is the retry: it must carry the code.
	if !strings.Contains(body, `name="code"`) || !strings.Contains(body, pairPageCode) {
		t.Fatalf("prompt drops the pairing code: %s", body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a denied session reached the cloud (%d calls)", n)
	}
}

func TestEnrolPair_ElevatedPairsAndAuditsStoreOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mgrID, cashierID := seedElevationUsers(t, d)
	calls := pairDeps(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}, "override_pin": {"555222"}}, &cashier)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "✅") || !strings.Contains(body, "This till is now paired with the shop.") ||
		!strings.Contains(body, "<code>"+pairPageStore+"</code>") {
		t.Fatalf("pair = %d %s, want the success fragment naming %s", rec.Code, body, pairPageStore)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("pair calls = %d, want 1", n)
	}
	if got, _, _ := d.Settings.Get(t.Context(), "marketplace.store_id"); got != pairPageStore {
		t.Fatalf("marketplace.store_id = %q, want %q", got, pairPageStore)
	}
	assertElevatedAudit(t, d, "paired", mgrID, cashierID)
	var payload string
	if err := d.Db.QueryRow(`SELECT COALESCE(data_json,'') FROM audit_log WHERE action='paired'`).Scan(&payload); err != nil {
		t.Fatalf("read audit payload: %v", err)
	}
	if !strings.Contains(payload, pairPageStore) || strings.Contains(payload, pairPageCode) || strings.Contains(payload, pairPageToken) {
		t.Fatalf("audit payload = %q, want the store id only", payload)
	}
}

func TestEnrolPair_RefusedCodeShowsFailure(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	pairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {"WRONG-123"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) || !strings.Contains(body, "Pairing failed") {
		t.Fatalf("refused pair = %d %s, want 200 with the failure span", rec.Code, body)
	}
	if strings.Contains(body, "WRONG-123") {
		t.Fatalf("the failure echoes the code: %s", body)
	}
}

func TestEnrolPair_ReplicaStoreMismatchHasOwnMessage(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	for k, v := range map[string]string{
		"sync.primary_url":     "http://127.0.0.1:1",
		"marketplace.store_id": "store-of-main-till",
	} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	pairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "This code belongs to a different shop") {
		t.Fatalf("mismatch = %d %s, want the store-mismatch message", rec.Code, body)
	}
	if strings.Contains(body, "Pairing failed") {
		t.Fatalf("mismatch shows the generic failure: %s", body)
	}
	if got, _, _ := d.Settings.Get(ctx, "marketplace.store_id"); got != "store-of-main-till" {
		t.Fatalf("marketplace.store_id = %q, want it unchanged", got)
	}
}

// A replica that has not synced its store yet gets its own translated
// message (the code was not spent), not the generic failure.
func TestEnrolPair_ReplicaWithoutStoreHasOwnMessage(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if err := d.Settings.Set(ctx, "sync.primary_url", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	calls := pairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) || !strings.Contains(body, "has not received its shop from the main till yet") {
		t.Fatalf("replica without store = %d %s, want the no-store message", rec.Code, body)
	}
	if strings.Contains(body, "Pairing failed") {
		t.Fatalf("replica without store shows the generic failure: %s", body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("pair calls = %d, want 0 (the single-use code must not be burnt)", n)
	}
}

// An env-pinned cloud identity gets its own translated message (the code was
// not spent and a new one will never work), not the generic failure
// (ut-docs#4062).
func TestEnrolPair_PinnedIdentityHasOwnMessage(t *testing.T) {
	setenvPinnedStore(t)
	mux, _, d := newFullAuthDeps(t)
	calls := pairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) || !strings.Contains(body, "set by whoever installed it") {
		t.Fatalf("pinned identity = %d %s, want the pinned-identity message", rec.Code, body)
	}
	if strings.Contains(body, "Pairing failed") {
		t.Fatalf("pinned identity shows the generic failure: %s", body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("pair calls = %d, want 0 (the single-use code must not be burnt)", n)
	}
}

func TestSettingsPage_ShowsPairWithAShop(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`hx-post="/api/enrol/pair"`, `hx-target="#pair-msg"`, `id="pair-msg"`, `name="code"`, "Pair with a shop",
		// Pair is not idempotent (it clears the identity before the code is
		// spent): a double-tap must not queue a second submit, and the call
		// has a visible busy state (ut-docs#3523 review).
		`hx-sync="this:drop"`, `hx-disabled-elt="find button[type=submit]"`, `hx-indicator="#pair-busy"`, `id="pair-busy"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page lacks %q", want)
		}
	}
}

// The elevation dialog is the retry for a denied session's Pair, so it
// carries the same double-submit guard as the form it stands in for.
func TestEnrolPair_PromptFormDropsDuplicateSubmits(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	pairDeps(t, d)
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &cashUser)
	assertElevationPrompt(t, "/api/enrol/pair", rec.Code, rec.Body.String())
	// ut-docs#5288: the guard now greys Cancel as well as the submit.
	for _, want := range []string{`hx-sync="this:drop"`, `hx-disabled-elt="#elevation-modal button:not([data-osk-toggle])"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("elevation prompt lacks %q: %s", want, rec.Body.String())
		}
	}
}

// ut-docs#3861: a refused "Register now" must not print the cloud's raw
// JSON (or the endpoint) into the card.
func refusingRegisterCloud(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEnrolNow_ServiceRefusedShowsTranslatedMessageOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	srv := refusingRegisterCloud(t, http.StatusForbidden,
		`{"data":null,"error":{"code":"service_unavailable","message":"Shops in XX are not served"}}`)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	nameShop(t, d) // an unnamed shop never reaches the cloud (ut-docs#4272)

	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) {
		t.Fatalf("refused register = %d %s, want 200 with the failure span", rec.Code, body)
	}
	if !strings.Contains(body, "The Universal Till cloud is not available for this shop. The till keeps working offline.") {
		t.Fatalf("missing the service-unavailable message: %s", body)
	}
	for _, bad := range []string{"register returned", "{", "service_unavailable", "not served", srv.URL, "Registration failed"} {
		if strings.Contains(body, bad) {
			t.Fatalf("body leaks %q: %s", bad, body)
		}
	}
}

// nameShop gives the shop a real name, so "Register now" reaches the
// (fake) cloud: an unnamed shop never does (ut-docs#4272).
func nameShop(t *testing.T, d *common.Deps) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Corner Shop"); err != nil {
		t.Fatalf("seed store.name: %v", err)
	}
}

// ut-docs#4272: "Register now" on a till whose shop is still unnamed (the
// migration-seeded "My Store") never calls the cloud; it tells the owner to
// name the shop first and links to the Shop name card.
func TestEnrolNow_UnnamedShopAsksForNameAndLinksToCard(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	var calls atomic.Int32
	cloud := http.NewServeMux()
	cloud.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"store_id":"s","token":"t"}}`))
	})
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "My Store"); err != nil {
		t.Fatalf("seed store.name: %v", err)
	}
	d.Cfg.StoreName = "My Store"
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	d.Cfg.Marketplace.PublicKey = strings.Repeat("ab", 32) // no signing-key fetch

	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) {
		t.Fatalf("unnamed register = %d %s, want 200 with the failure span", rec.Code, body)
	}
	for _, want := range []string{
		"Name your shop first: the till registers with the cloud only once the shop has a real name.",
		`<a href="/settings#settings-store-name">Shop name</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Registration failed") || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("unnamed shop shows the generic failure: %s", body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("/stores/register called %d times for an unnamed shop", n)
	}
}

func TestEnrolNow_OtherFailureShowsGenericMessageNotRawBody(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	srv := refusingRegisterCloud(t, http.StatusInternalServerError, `{"error":{"code":"internal","message":"db exploded <b>"}}`)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	nameShop(t, d) // an unnamed shop never reaches the cloud (ut-docs#4272)

	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "Registration failed") {
		t.Fatalf("missing the generic failure text: %s", body)
	}
	for _, bad := range []string{"register returned", "{", "db exploded", "<b>", "not available for this shop"} {
		if strings.Contains(body, bad) {
			t.Fatalf("body leaks %q: %s", bad, body)
		}
	}
	if !strings.Contains(body, "(") || !strings.Contains(body, "127.0.0.1") {
		t.Fatalf("generic failure should still name the endpoint tried: %s", body)
	}
}

// ut-docs#3990: "Show claim code" on a registered till whose shop the cloud
// refuses (or any other cloud failure) prints a translated message, never
// the cloud's body.
func claimCodeDeps(t *testing.T, status int, body string) (*http.ServeMux, *common.Deps, string) {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	cloud := http.NewServeMux()
	cloud.HandleFunc("POST /api/v1/stores/claim-code", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: srv.URL + "/api", StoreID: "store-claim-3990", MerchantToken: "claim-store-token",
		DeviceID: "till-main", PublicKey: strings.Repeat("ab", 32),
	}}
	enroll.Init(t.Context(), cfg, newMemKV(), &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace = cfg.Marketplace
	return mux, d, srv.URL
}

func TestClaimCode_ServiceRefusedShowsTranslatedMessageOnly(t *testing.T) {
	mux, _, cloudURL := claimCodeDeps(t, http.StatusForbidden,
		`{"data":null,"error":{"code":"service_unavailable","message":"Shops in XX are not served"}}`)
	rec := postForm(mux, "/api/enrol/claim-code", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) {
		t.Fatalf("refused claim code = %d %s, want 200 with the failure span", rec.Code, body)
	}
	if !strings.Contains(body, "The Universal Till cloud is not available for this shop. The till keeps working offline.") {
		t.Fatalf("missing the service-unavailable message: %s", body)
	}
	for _, bad := range []string{"claim-code returned", "{", "&#34;", "service_unavailable", "not served", cloudURL} {
		if strings.Contains(body, bad) {
			t.Fatalf("body leaks %q: %s", bad, body)
		}
	}
}

func TestClaimCode_OtherFailureShowsGenericMessageNotRawBody(t *testing.T) {
	mux, _, _ := claimCodeDeps(t, http.StatusInternalServerError, `{"error":{"code":"internal","message":"db exploded <b>"}}`)
	rec := postForm(mux, "/api/enrol/claim-code", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "Could not get a claim code") {
		t.Fatalf("missing the generic claim failure text: %s", body)
	}
	for _, bad := range []string{"claim-code returned", "{", "&#34;", "db exploded", "&lt;b&gt;", "not available for this shop"} {
		if strings.Contains(body, bad) {
			t.Fatalf("body leaks %q: %s", bad, body)
		}
	}
}

func TestClaimCode_UnregisteredTillSaysNotRegistered(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	enroll.Init(t.Context(), &config.Config{}, newMemKV(), &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace = config.MarketplaceConfig{}
	rec := postForm(mux, "/api/enrol/claim-code", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "This till is not registered yet.") || strings.Contains(body, "marketplace yet") {
		t.Fatalf("unregistered claim = %s, want the translated not-registered text only", body)
	}
}

// ut-docs#3990: "Register now" with no cloud address configured, or while
// another attempt holds the slot, says what is actually wrong instead of
// "check the internet connection".
func TestEnrolNow_NotConfiguredHasOwnMessage(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = ""
	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "No cloud address is set up on this till") {
		t.Fatalf("missing the not-configured text: %s", body)
	}
	for _, bad := range []string{"check the internet connection", "not configured", "marketplace endpoint"} {
		if strings.Contains(body, bad) {
			t.Fatalf("body has %q: %s", bad, body)
		}
	}
}

func TestEnrolNow_BusySlotHasOwnMessage(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	cloud := http.NewServeMux()
	cloud.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	nameShop(t, d) // an unnamed shop never reaches the cloud (ut-docs#4272)

	// A first attempt holds the slot inside the (blocked) cloud call.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = enroll.RegisterNow(context.Background(), d.Cfg, d.Settings)
	}()
	<-entered
	t.Cleanup(func() { close(release); <-done })

	form := url.Values{"accept_terms": {"on"}}
	req := httptest.NewRequest(http.MethodPost, "/api/enrol/now", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, mgrUser)
	ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	body := rec.Body.String()
	if !strings.Contains(body, "Another registration attempt is still running") {
		t.Fatalf("missing the busy text: %s", body)
	}
	if strings.Contains(body, "check the internet connection") || strings.Contains(body, "slot held") {
		t.Fatalf("busy answer = %s, want only the busy text", body)
	}
}

// ut-docs#4058: a failed "Pair with a shop" shows a translated message,
// never err.Error() (cloud JSON, endpoint, dial errors).
func pairFailureCloud(t *testing.T, d *common.Deps, status int, body string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
}

func assertNoLeak(t *testing.T, body string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(body, b) {
			t.Fatalf("body leaks %q: %s", b, body)
		}
	}
}

func TestEnrolPair_ServiceRefusedShowsTranslatedMessageOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	pairFailureCloud(t, d, http.StatusForbidden,
		`{"data":null,"error":{"code":"service_unavailable","message":"Shop X refused because of region"}}`)
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) ||
		!strings.Contains(body, "The Universal Till cloud is not available for this shop. The till keeps working offline.") {
		t.Fatalf("refused pair = %d %s, want the service-unavailable message", rec.Code, body)
	}
	assertNoLeak(t, body, "Pairing failed", "service_unavailable", "refused because", "403", pairPageCode)
}

func TestEnrolPair_OtherFailuresShowGenericMessageOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"403 code_invalid": {http.StatusForbidden, `{"data":null,"error":{"code":"code_invalid","message":"nope"}}`},
		"502 non-JSON":     {http.StatusBadGateway, `<html>bad gateway</html>`},
	} {
		t.Run(name, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			pairFailureCloud(t, d, tc.status, tc.body)
			rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
			body := rec.Body.String()
			if !strings.Contains(body, `class="error"`) || !strings.Contains(body, "Pairing failed — check the code and the internet connection, then try again") {
				t.Fatalf("pair = %s, want the generic pair_failed text", body)
			}
			assertNoLeak(t, body, "code_invalid", "cloud answered", "returned", "502", "403", "bad gateway", "not available for this shop", ": ")
		})
	}
}

func TestEnrolPair_NotConfiguredShowsTranslatedMessage(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	pairDeps(t, d)
	d.Cfg.Marketplace.EndpointURL = ""
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "No cloud address is set up on this till") {
		t.Fatalf("pair = %s, want the not-configured message", body)
	}
	assertNoLeak(t, body, "Pairing failed", "marketplace endpoint")
}

func TestEnrolPair_NetworkFailureShowsGenericMessageOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	pairDeps(t, d)
	d.Cfg.Marketplace.EndpointURL = "http://127.0.0.1:1/api"
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, "Pairing failed — check the code and the internet connection, then try again") {
		t.Fatalf("pair = %s, want the generic pair_failed text", body)
	}
	assertNoLeak(t, body, "dial", "127.0.0.1", "pair request", "refused", ": ")
}

// The handler-level busy path needs a held attempt slot, which only the
// enroll package can take; enroll's TestPairBusySlotIsSentinel proves the
// sentinel and this proves its message.
func TestEnrolFailureKey_CoversPairErrors(t *testing.T) {
	busy := fmt.Errorf("%w: %w", enroll.ErrAttemptBusy, context.DeadlineExceeded)
	for err, want := range map[error]string{
		busy:                     "settings.enrol.busy",
		enroll.ErrNotConfigured:  "settings.enrol.not_configured",
		enroll.ErrStoreUnnamed:   "settings.enrol.name_required",
		errors.New("pair: boom"): "",
	} {
		if got := enrolFailureKey(err); got != want {
			t.Fatalf("enrolFailureKey(%v) = %q, want %q", err, got, want)
		}
	}
}

// ut-docs#3557: a till with a working cloud connection of its own asks
// before "Pair with a shop" replaces it — Pair clears the identity before
// the cloud judges the code, so a typo would leave the till disconnected.

const (
	pairOldStore = "store-old"
	pairOldToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// registeredPairDeps makes d a healthy registered till (own token, a named
// shop, no lock-out chip) pointed at the fake pairing cloud.
func registeredPairDeps(t *testing.T, d *common.Deps) *atomic.Int32 {
	t.Helper()
	for k, v := range map[string]string{
		"marketplace.store_id": pairOldStore, "marketplace.token": pairOldToken,
		"marketplace.device_id": "till-old-device", "store.name": "Corner Shop",
	} {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	calls := pairDeps(t, d)
	if !enroll.CurrentStatus().Registered {
		t.Fatal("precondition: the till must be registered")
	}
	pinAuthChip(t, cloudsync.AuthChip{})
	return calls
}

func pinAuthChip(t *testing.T, st cloudsync.AuthChip) {
	t.Helper()
	old := cloudAuthChipStatus
	cloudAuthChipStatus = func() cloudsync.AuthChip { return st }
	t.Cleanup(func() { cloudAuthChipStatus = old })
}

func assertIdentityKept(t *testing.T, d *common.Deps) {
	t.Helper()
	for k, want := range map[string]string{"marketplace.store_id": pairOldStore, "marketplace.token": pairOldToken, "marketplace.device_id": "till-old-device"} {
		if got, _, _ := d.Settings.Get(t.Context(), k); got != want {
			t.Fatalf("%s = %q, want %q (identity must be untouched)", k, got, want)
		}
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='paired'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("paired audit rows = %d (%v), want 0", n, err)
	}
}

func assertConfirmPrompt(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "confirm-prompt" || !strings.Contains(body, "confirm-modal") {
		t.Fatalf("pair = %d %q %s, want 200 with the confirm prompt", rec.Code, rec.Header().Get("X-UT-Response"), body)
	}
	return body
}

func TestEnrolPair_RegisteredTillAsksToConfirmFirst(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	calls := registeredPairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	body := assertConfirmPrompt(t, rec)
	for _, want := range []string{"Corner Shop", pairPageCode, `name="confirm_replace" value="1"`, `name="code"`, `hx-target="#pair-msg"`, "data-ut-escape-close", "data-ut-prompt", `<span class="elevation-hint`, "Replace and pair"} {
		if !strings.Contains(body, want) {
			t.Fatalf("confirm prompt lacks %q: %s", want, body)
		}
	}
	// Bidi isolates around both placeholders (typo check in RTL sentences).
	if !strings.Contains(body, "\u2068Corner Shop\u2069") || !strings.Contains(body, "\u2068"+unbreakableCode(pairPageCode)+"\u2069") {
		t.Fatalf("shop and code are not bidi-isolated: %s", body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("cloud calls = %d, want 0 before confirming", n)
	}
	assertIdentityKept(t, d)
}

func TestEnrolPair_ConfirmIsDangerWithCancelFocused(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registeredPairDeps(t, d)
	body := assertConfirmPrompt(t, postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser))
	if !strings.Contains(body, `class="btn danger"`) || strings.Contains(body, `class="btn primary"`) {
		t.Fatalf("pair confirm is not the danger button: %s", body)
	}
	if !strings.Contains(body, `class="btn secondary" data-action="close-closest" autofocus`) {
		t.Fatalf("autofocus is not on Cancel: %s", body)
	}
}

func TestConfirmPrompt_DefaultStaysPrimaryAutofocusConfirm(t *testing.T) {
	rec := httptest.NewRecorder()
	renderConfirmPrompt(rec, httptest.NewRequest(http.MethodPost, "/x", nil), confirmPrompt{Action: "/x", HxTarget: "#m", Title: "T", Body: "B", Confirm: "Go"})
	body := rec.Body.String()
	if !strings.Contains(body, `class="btn primary" autofocus>Go`) || strings.Contains(body, "danger") {
		t.Fatalf("default confirm prompt changed: %s", body)
	}
}

func TestEnrolPair_RegisteredTillConfirmedPairs(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	calls := registeredPairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}, "confirm_replace": {"1"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "✅") || strings.Contains(rec.Header().Get("X-UT-Response"), "confirm") {
		t.Fatalf("confirmed pair = %d %s, want the success fragment", rec.Code, body)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("cloud calls = %d, want 1", n)
	}
	if got, _, _ := d.Settings.Get(t.Context(), "marketplace.store_id"); got != pairPageStore {
		t.Fatalf("store_id = %q, want %q", got, pairPageStore)
	}
	var payload string
	if err := d.Db.QueryRow(`SELECT COALESCE(data_json,'') FROM audit_log WHERE action='paired'`).Scan(&payload); err != nil {
		t.Fatalf("paired audit row: %v", err)
	}
	if !strings.Contains(payload, pairPageStore) || strings.Contains(payload, pairPageCode) || strings.Contains(payload, pairPageToken) {
		t.Fatalf("audit payload = %q, want the store id only", payload)
	}
}

func TestEnrolPair_RegisteredCashierConfirmsBeforeElevation(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	calls := registeredPairDeps(t, d)

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &cashUser)
	assertConfirmPrompt(t, rec)
	if strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Fatalf("a PIN was asked before the confirmation: %s", rec.Body.String())
	}

	rec = postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}, "confirm_replace": {"1"}}, &cashUser)
	assertElevationPrompt(t, "cashier confirmed", rec.Code, rec.Body.String())
	for _, want := range []string{`name="code"`, `name="confirm_replace"`, pairPageCode} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("elevation prompt lacks %q: %s", want, rec.Body.String())
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("cloud calls = %d, want 0", n)
	}
	assertIdentityKept(t, d)
}

func TestEnrolPair_RegisteredCashierConfirmedWithPinPairsOnce(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mgrID, cashierID := seedElevationUsers(t, d)
	calls := registeredPairDeps(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}, "confirm_replace": {"1"}, "override_pin": {"000000"}}, &cashier)
	assertElevationPrompt(t, "wrong pin", rec.Code, rec.Body.String())
	if !strings.Contains(rec.Body.String(), `name="confirm_replace"`) || !strings.Contains(rec.Body.String(), `name="code"`) {
		t.Fatalf("wrong-PIN prompt drops a field: %s", rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("a wrong PIN reached the cloud")
	}
	assertIdentityKept(t, d)

	rec = postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}, "confirm_replace": {"1"}, "override_pin": {"555222"}}, &cashier)
	if !strings.Contains(rec.Body.String(), "✅") {
		t.Fatalf("pair = %s, want success", rec.Body.String())
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("cloud calls = %d, want 1", n)
	}
	assertElevatedAudit(t, d, "paired", mgrID, cashierID)
}

func TestEnrolPair_ShopNameFallsBackToStoreID(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registeredPairDeps(t, d)
	if err := d.Settings.Set(t.Context(), "store.name", "My Store"); err != nil {
		t.Fatal(err)
	}
	d.Cfg.StoreName = ""
	body := assertConfirmPrompt(t, postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser))
	if !strings.Contains(body, pairOldStore) || strings.Contains(body, "My Store") {
		t.Fatalf("placeholder shop name not replaced by the store id: %s", body)
	}
}

func TestEnrolPair_LockedOutTillPairsWithoutConfirm(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	calls := registeredPairDeps(t, d)
	pinAuthChip(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeDeviceRevoked})

	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✅") || calls.Load() != 1 {
		t.Fatalf("locked-out till = %s (calls %d), want a one-step pair", rec.Body.String(), calls.Load())
	}
}

func TestEnrolPair_ReplacedTillPairsWithoutConfirm(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	calls := registeredPairDeps(t, d)
	if err := d.Settings.Set(t.Context(), "till_identity.replaced", "1"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✅") || calls.Load() != 1 {
		t.Fatalf("replaced till = %s (calls %d), want a one-step pair", rec.Body.String(), calls.Load())
	}
}

func TestEnrolPair_ViaMainTillReplicaPairsWithoutConfirm(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	for k, v := range map[string]string{
		"sync.primary_url": "http://127.0.0.1:1", "marketplace.device_id": "till-replica-own",
		"marketplace.device_registered": "till-replica-own", "marketplace.store_id": pairPageStore,
	} {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	calls := pairDeps(t, d)
	pinAuthChip(t, cloudsync.AuthChip{})
	if st := enroll.CurrentStatus(); !st.ViaMainTill || st.Registered {
		t.Fatalf("precondition: want ViaMainTill && !Registered, got %+v", st)
	}
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✅") || calls.Load() != 1 {
		t.Fatalf("via-main replica = %s (calls %d), want a one-step pair", rec.Body.String(), calls.Load())
	}
}

func TestEnrolPair_RefusalsComeBeforeConfirm(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mgrID, cashierID := seedElevationUsers(t, d)
	_, _ = mgrID, cashierID
	calls := registeredPairDeps(t, d)

	for _, u := range []*auth.User{&mgrUser, &cashUser} {
		rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {"  "}}, u)
		body := rec.Body.String()
		if !strings.Contains(body, "Pairing failed") || strings.Contains(body, "confirm-modal") || strings.Contains(body, "elevation-dialog") {
			t.Fatalf("empty code (%s) = %s, want the plain refusal", u.Role, body)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("cloud calls = %d, want 0", calls.Load())
	}
	assertIdentityKept(t, d)
}

func TestEnrolPair_PinnedRefusalComesBeforeConfirm(t *testing.T) {
	setenvPinnedStore(t)
	mux, _, d := newFullAuthDeps(t)
	calls := pairDeps(t, d)
	if err := d.Settings.Set(t.Context(), "marketplace.token", pairOldToken); err != nil {
		t.Fatal(err)
	}
	pinAuthChip(t, cloudsync.AuthChip{})
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {pairPageCode}}, &cashUser)
	body := rec.Body.String()
	if !strings.Contains(body, "set by whoever installed it") || strings.Contains(body, "confirm-modal") || strings.Contains(body, "elevation-dialog") {
		t.Fatalf("pinned = %s, want the pinned refusal only", body)
	}
	if calls.Load() != 0 {
		t.Fatalf("cloud calls = %d, want 0", calls.Load())
	}
}

// The pinned-refusal tests set UT_MARKETPLACE_STORE_ID; once each is done,
// enroll must not still treat the store id as pinned, or the next test's
// pairing is refused before its gate (pages-shuffle seed
// 1791386291628608919 hit TestSettingsEndpoints_RoleMatrix, ut-docs#5305).
func TestEnrolPair_PinnedRefusalLeavesNoPin(t *testing.T) {
	for _, run := range []func(*testing.T){TestEnrolPair_PinnedRefusalComesBeforeConfirm, TestEnrolPair_PinnedIdentityHasOwnMessage} {
		t.Run("", run)
		assertNoEnrolPin(t)
	}
}

func assertNoEnrolPin(t *testing.T) {
	t.Helper()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1/api"}}
	if err := enroll.PairPrecheck(t.Context(), cfg, newMemKV(), pairPageCode); errors.Is(err, enroll.ErrPairIdentityPinned) {
		t.Fatalf("store id still pinned after the pinned-refusal test: %v", err)
	}
}

func TestEnrolPair_ConfirmEscapesCode(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registeredPairDeps(t, d)
	body := assertConfirmPrompt(t, postForm(mux, "/api/enrol/pair", url.Values{"code": {"<b>x"}}, &mgrUser))
	if !strings.Contains(body, "&lt;b&gt;x") || strings.Contains(body, "<b>x") {
		t.Fatalf("code not escaped in the confirm body: %s", body)
	}
}

// The dialog's shop name and code are display-only: a stray isolate/override
// control can't break out of the FSI..PDI wrap, and the code can't wrap at a
// hyphen (ut-docs#3557 review + test F1).
func TestPairConfirmDisplayHelpers(t *testing.T) {
	if got := stripBidiControls("Shop\u2069\u202Eevil\u2066"); got != "Shopevil" {
		t.Fatalf("stripBidiControls = %q, want %q", got, "Shopevil")
	}
	if got := unbreakableCode("AB12-CD34"); got != "AB12-\u2060CD34" {
		t.Fatalf("unbreakableCode = %q", got)
	}
	if got := unbreakableCode("K7QXM2RP"); got != "K7QXM2RP" {
		t.Fatalf("unbreakableCode changed a code without hyphens: %q", got)
	}
}
