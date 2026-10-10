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
	"time"

	"github.com/universaltill/universal-till/internal/auth"
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
	for _, want := range []string{`hx-sync="this:drop"`, `hx-disabled-elt="find button[type=submit]"`} {
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

	rec := postForm(mux, "/api/enrol/now", url.Values{}, &mgrUser)
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

func TestEnrolNow_OtherFailureShowsGenericMessageNotRawBody(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	srv := refusingRegisterCloud(t, http.StatusInternalServerError, `{"error":{"code":"internal","message":"db exploded <b>"}}`)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"

	rec := postForm(mux, "/api/enrol/now", url.Values{}, &mgrUser)
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
	rec := postForm(mux, "/api/enrol/now", url.Values{}, &mgrUser)
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

	// A first attempt holds the slot inside the (blocked) cloud call.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = enroll.RegisterNow(context.Background(), d.Cfg, d.Settings)
	}()
	<-entered
	t.Cleanup(func() { close(release); <-done })

	form := url.Values{}
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
