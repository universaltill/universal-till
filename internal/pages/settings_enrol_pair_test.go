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
