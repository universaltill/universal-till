package pages

import (
	"bytes"
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

// ut-docs#5145: the cloud refused to re-register this till (409 device_live).
// It keeps selling; a status chip and Settings say when it can reconnect and
// give a manager the two facts support needs (device ID, key fingerprint).

// blockUntil stores a register block the way enroll does.
func blockUntil(t *testing.T, d *common.Deps, at time.Time) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), enroll.RegisterBlockedUntilKey, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func TestCloudAuthChip_BlockedVariant(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	until := time.Now().Add(30 * time.Hour)
	blockUntil(t, d, until)

	body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
	for _, want := range []string{`data-cloud-auth="blocked"`, "Cloud reconnect paused", `href="/settings#registration"`, "Selling is not affected"} {
		if !strings.Contains(body, want) {
			t.Fatalf("manager chip lacks %q: %s", want, body)
		}
	}
	for _, other := range []string{"needs pairing again", "Register as a new store", "Removed from the shop"} {
		if strings.Contains(body, other) {
			t.Fatalf("blocked chip also says %q: %s", other, body)
		}
	}
	for _, modal := range []string{"<dialog", "showModal", "alertdialog"} {
		if strings.Contains(body, modal) {
			t.Fatalf("chip must never be a modal: %s", body)
		}
	}
}

// Blocked wins over the 401 variants: a till refused a re-register must not be
// told to "Register as a new store".
func TestCloudAuthChip_BlockedWinsOverTheLockoutVariants(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeUnauthorized})
	blockUntil(t, d, time.Now().Add(time.Hour))
	body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
	if !strings.Contains(body, `data-cloud-auth="blocked"`) || strings.Contains(body, "Register as a new store") {
		t.Fatalf("body = %s", body)
	}
}

func TestCloudAuthChip_NoBlockAfterItPassedOrWithNone(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	if rec := getAs(mux, "/ui/cloud-auth-chip", &mgrUser); strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("no block: %q", rec.Body.String())
	}
	blockUntil(t, d, time.Now().Add(-time.Minute))
	if rec := getAs(mux, "/ui/cloud-auth-chip", &mgrUser); strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("a past block still shows a chip: %q", rec.Body.String())
	}
}

// Test 19: a cashier sees the chip (it is the till's state) but no link, no
// date, no ID and no fingerprint; the title is the neutral short hint.
func TestCloudAuthChip_BlockedCashierSeesNeutralChip(t *testing.T) {
	useTempDataDir(t)
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	until := time.Now().Add(30 * time.Hour)
	blockUntil(t, d, until)
	body := getAs(mux, "/ui/cloud-auth-chip", &cashUser).Body.String()
	if !strings.Contains(body, `data-cloud-auth="blocked"`) || !strings.Contains(body, "Cloud reconnect paused") {
		t.Fatalf("cashier must see the chip: %s", body)
	}
	if strings.Contains(body, "href=") {
		t.Fatalf("cashier chip must not link to Settings: %s", body)
	}
	if !strings.Contains(body, "A manager can see details in Settings.") {
		t.Fatalf("cashier title is not the neutral hint: %s", body)
	}
	for _, leak := range []string{until.Format("2006"), "support", "fingerprint"} {
		if leak != until.Format("2006") && strings.Contains(strings.ToLower(body), leak) {
			t.Fatalf("cashier chip leaks %q: %s", leak, body)
		}
	}
}

// Test 15/16 (till side of the stub cloud): Register now shows the translated
// message with the date and never the raw body.
func newDeviceLiveCloud(t *testing.T, d *common.Deps, usableFrom string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprintf(w, `{"data":{"usable_from":%q},"error":{"code":"device_live","message":"RAWBODY this device was in use recently"}}`, usableFrom)
	})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	d.Cfg.Marketplace.PublicKey = strings.Repeat("ab", 32)
	nameShop(t, d)
	return &calls
}

func TestEnrolNow_DeviceLiveShowsTheDateAndNeverTheRawBody(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	usable := time.Now().UTC().Add(40 * time.Hour).Truncate(time.Minute)
	calls := newDeviceLiveCloud(t, d, usable.Format(time.RFC3339))

	rec := postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "This till was in use recently") || !strings.Contains(body, "Selling is not affected") {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(body, "<bdi>") {
		t.Fatalf("the date must sit in a <bdi>: %s", body)
	}
	// D5: a standing state, not a failure: neutral styling, no cross.
	if strings.Contains(body, `class="error"`) || strings.Contains(body, "❌") || !strings.Contains(body, `class="muted"`) {
		t.Fatalf("device_live must render neutral (muted), not as an error: %s", body)
	}
	for _, raw := range []string{"RAWBODY", "device_live", "usable_from"} {
		if strings.Contains(body, raw) {
			t.Fatalf("raw cloud text %q rendered: %s", raw, body)
		}
	}
	if got := enroll.RegisterBlockedUntil(t.Context(), d.Settings); !got.Equal(usable) {
		t.Fatalf("blocked until %v, want %v", got, usable)
	}
	// Register now never honours the block: a second press calls the cloud again.
	postForm(mux, "/api/enrol/now", url.Values{"accept_terms": {"on"}}, &mgrUser)
	if calls.Load() != 2 {
		t.Fatalf("cloud calls = %d, want 2 (a deliberate Register now always tries)", calls.Load())
	}
}

// The plugin install path surfaces the block instead of failing silently, and
// the lazy registration inside it stays quiet while blocked.
func TestInstallFromMarketplace_BlockedTillSaysSoAndDoesNotCallTheCloud(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})
	registerPluginAPI(mux, d)
	blockUntil(t, d, time.Now().Add(5*time.Hour))

	body, _ := json.Marshal(map[string]any{"listing_id": "lst-blocked", "accept_terms": true})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"plugins.install.error.device_live"`) {
		t.Fatalf("install = %d %s, want the device_live key", rec.Code, rec.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("cloud hit %d times while blocked", hits.Load())
	}
}

// Settings > Till registration (manager): the notice with the date, the
// device ID and the key fingerprint, grouped, as plain selectable text.
func TestSettings_BlockedRegistrationFactsForAManager(t *testing.T) {
	useTempDataDir(t)
	mux, _, d := newFullAuthDeps(t)
	until := time.Now().Add(30 * time.Hour)
	blockUntil(t, d, until)

	body := getAs(mux, "/settings", &mgrUser).Body.String()
	for _, want := range []string{
		`data-testid="register-blocked"`,
		"can reconnect to the cloud from",
		"Selling is not affected",
		"ask support to allow it",
		`data-testid="register-facts-fingerprint"`,
		"Key fingerprint",
		"<bdi",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings lacks %q", want)
		}
	}
	fp, err := tillKeyFingerprint()
	if err != nil || len(fp) != 32 {
		t.Fatalf("fingerprint = %q err=%v", fp, err)
	}
	if !strings.Contains(body, fp[0:4]+" "+fp[4:8]+" "+fp[8:12]) {
		t.Fatalf("settings does not show the grouped fingerprint %q", fp)
	}
}

func TestSettings_BlockedRegistrationKeyUnavailable(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	blockUntil(t, d, time.Now().Add(time.Hour))
	orig := tillKeyFingerprint
	tillKeyFingerprint = func() (string, error) { return "", errors.New("unreadable") }
	t.Cleanup(func() { tillKeyFingerprint = orig })
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, "Key fingerprint unavailable") || strings.Contains(body, "unreadable") {
		t.Fatalf("settings must say the fingerprint is unavailable, without the raw error")
	}
}

// Test 19 (T4): a cashier is refused /settings flat (sale-only), and everywhere
// a cashier CAN see (the chip, the sell screen) neither the device ID nor the
// key fingerprint is present; the view builder itself withholds both too, so
// the gate does not depend on the page-level 403 alone.
func TestSettings_BlockedRegistrationHidesFactsFromACashier(t *testing.T) {
	useTempDataDir(t)
	mux, _, d := newFullAuthDeps(t)
	const deviceID = "till-device-5145-secret"
	if err := d.Settings.Set(t.Context(), "marketplace.device_id", deviceID); err != nil {
		t.Fatal(err)
	}
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	if enroll.CurrentStatus().DeviceID != deviceID {
		t.Fatalf("fixture: device id = %q", enroll.CurrentStatus().DeviceID)
	}
	blockUntil(t, d, time.Now().Add(30*time.Hour))
	fp, err := tillKeyFingerprint()
	if err != nil || fp == "" {
		t.Fatalf("fingerprint = %q err=%v", fp, err)
	}

	if rec := getAs(mux, "/settings", &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("/settings as a cashier = %d, want 403", rec.Code)
	}
	assertNoFacts := func(where, body string) {
		t.Helper()
		flat := strings.ReplaceAll(body, " ", "")
		if strings.Contains(body, deviceID) || strings.Contains(flat, fp) || strings.Contains(body, `data-testid="register-facts-fingerprint"`) {
			t.Fatalf("%s shows the device ID or key fingerprint to a cashier: %s", where, body)
		}
	}
	// The view builder, asked as each role.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), cashUser)
	v := registerBlockedView(req, d, "en")
	if v == nil || v["notice"] == nil {
		t.Fatalf("cashier view = %v, want the notice", v)
	}
	for _, k := range []string{"manage", "deviceID", "fingerprint"} {
		if _, has := v[k]; has {
			t.Fatalf("cashier view carries %q", k)
		}
	}
	if fv := registerFactsView(req, d, "en"); fv != nil {
		t.Fatalf("cashier facts view = %v", fv)
	}
	mreq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	if mv := registerFactsView(mreq, d, "en"); mv["deviceID"] != deviceID {
		t.Fatalf("manager view = %v, want the device ID", mv)
	}
	// The sell screen and the chip, as the cashier.
	assertNoFacts("sell screen", getAs(mux, "/", &cashUser).Body.String())
}

// The chip as a cashier, with the device ID and key set (T4, the chip half).
func TestCloudAuthChip_BlockedCashierChipHasNoDeviceFacts(t *testing.T) {
	useTempDataDir(t)
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	const deviceID = "till-device-5145-secret"
	if err := d.Settings.Set(t.Context(), "marketplace.device_id", deviceID); err != nil {
		t.Fatal(err)
	}
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	blockUntil(t, d, time.Now().Add(30*time.Hour))
	body := getAs(mux, "/ui/cloud-auth-chip", &cashUser).Body.String()
	fp, _ := tillKeyFingerprint()
	if !strings.Contains(body, `data-cloud-auth="blocked"`) || strings.Contains(body, deviceID) || strings.Contains(strings.ReplaceAll(body, " ", ""), fp) {
		t.Fatalf("cashier chip = %s", body)
	}
}

// T9: a till that is already registered shows no blocked chip and no notice,
// even if a stale block is still stored.
func TestBlockedChipAndNoticeAreNotShownOnARegisteredTill(t *testing.T) {
	useTempDataDir(t)
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	newRegisteredTill(t, d)
	blockUntil(t, d, time.Now().Add(30*time.Hour))
	if rec := getAs(mux, "/ui/cloud-auth-chip", &mgrUser); strings.Contains(rec.Body.String(), "blocked") || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("registered till shows the blocked chip: %q", rec.Body.String())
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	if v := registerBlockedView(req, d, "en"); v != nil {
		t.Fatalf("registered till has a blocked notice: %v", v)
	}
}

func TestSettings_NoBlockNoRegistrationNotice(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, `data-testid="register-blocked"`) {
		t.Fatal("notice shown with no block")
	}
}
