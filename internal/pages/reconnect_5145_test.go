package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
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
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5145 (H2): a Registered till whose cloud sign-in was refused on an
// anonymous store. A manager can Reconnect it (same shop, the till proves its
// key) or register it as a new shop; both are operator actions.

type lockoutCloud struct {
	mu       sync.Mutex
	register func(n int, body map[string]any, w http.ResponseWriter) // calls after the first-time register
	calls    atomic.Int32
	bodies   []map[string]any
}

func (c *lockoutCloud) body(i int) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[i]
}

func pageCode(w http.ResponseWriter, status int, code string, data map[string]any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": map[string]any{"code": code, "message": "RAWBODY"}})
}

// newLockedOutTill registers a till against a scripted cloud (the first
// register, with no expect_store_id, always succeeds), then pins the 401
// lock-out so the chip variant is "register". Returns the mux, deps and cloud.
func newLockedOutTill(t *testing.T, script func(n int, body map[string]any, w http.ResponseWriter)) (*http.ServeMux, *common.Deps, *lockoutCloud) {
	t.Helper()
	useTempDataDir(t)
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeUnauthorized})
	c := &lockoutCloud{register: script}
	h := http.NewServeMux()
	h.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		n := int(c.calls.Add(1))
		if n == 1 {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"store_id":"store-t","merchant_id":"m-t","token":"tok-old"}}`))
			return
		}
		c.register(n, body, w)
	})
	h.HandleFunc("GET /ui/api/signing-key", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // tests that need no key set PublicKey below
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	d.Cfg.Marketplace.PublicKey = strings.Repeat("ab", 32)
	nameShop(t, d)
	if _, err := enroll.RegisterNow(t.Context(), d.Cfg, d.Settings); err != nil || !enroll.CurrentStatus().Registered {
		t.Fatalf("seed registration: %v", err)
	}
	return mux, d, c
}

func okRegister(token string) func(int, map[string]any, http.ResponseWriter) {
	return func(_ int, _ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-t","merchant_id":"m-t","token":"` + token + `"}}`))
	}
}

// Chip: the register variant says what happened, not what to do.
func TestChip_LockedOutVariantWords(t *testing.T) {
	mux, _ := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeUnauthorized})
	body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
	for _, want := range []string{`data-cloud-auth="register"`, "Cloud sign-in lost", "Open Settings to reconnect it", `href="/settings#registration"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("manager chip lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Register as a new store") {
		t.Fatalf("old wording survived: %s", body)
	}
	cash := getAs(mux, "/ui/cloud-auth-chip", &cashUser).Body.String()
	if !strings.Contains(cash, "Cloud sign-in lost") || strings.Contains(cash, "href=") || !strings.Contains(cash, "A manager can see details in Settings.") || strings.Contains(cash, "Open Settings") {
		t.Fatalf("cashier chip: %s", cash)
	}
}

// Test 23: panel + both buttons only in the anonymous lock-out; ID and
// fingerprint always for a manager; replica / claimed / env-pinned: no panel.
// The negative checks assert on the panel's own markers, never its prose: the
// v0.36.1 release notes quote "Reconnect this till" and Settings renders them,
// so a phrase match is a false positive.
func TestSettings_LockoutPanelAndFacts(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, okRegister("x"))
	deviceID := enroll.CurrentStatus().DeviceID
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	fp, _ := tillKeyFingerprint()
	for _, want := range []string{
		`data-testid="reconnect-panel"`, "The cloud no longer accepts this till&#39;s sign-in. Selling is not affected.",
		"Reconnect this till", "Register as a new shop", `data-testid="register-facts"`, deviceID, fp[:4] + " " + fp[4:8],
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings lacks %q", want)
		}
	}
	if strings.Index(body, `hx-post="/api/enrol/reconnect"`) > strings.Index(body, `hx-post="/api/enrol/new-shop"`) {
		t.Fatal("Register as a new shop must come after Reconnect in DOM order")
	}
	// Claimed store: pairing is the path, no panel.
	claimStore(t, d)
	if b := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(b, `data-testid="reconnect-panel"`) || strings.Contains(b, `hx-post="/api/enrol/reconnect"`) {
		t.Fatal("panel shown for a claimed store")
	}
}

func TestSettings_NoPanelWithoutLockoutAndFactsStillShown(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, okRegister("x"))
	cloudAuthChipStatus = func() cloudsync.AuthChip { return cloudsync.AuthChip{} }
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if strings.Contains(body, `data-testid="reconnect-panel"`) || strings.Contains(body, `hx-post="/api/enrol/reconnect"`) {
		t.Fatal("panel shown without a lock-out")
	}
	if !strings.Contains(body, `data-testid="register-facts"`) || !strings.Contains(body, enroll.CurrentStatus().DeviceID) {
		t.Fatal("a registered till's manager must always see the device ID and fingerprint")
	}
	_ = d
}

func TestSettings_NoPanelOnAReplicaOrWithAnEnvPinnedToken(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, okRegister("x"))
	cfg := &config.Config{}
	cfg.Marketplace.MerchantToken = "pinned-by-env"
	enroll.Init(t.Context(), cfg, d.Settings, &sync.WaitGroup{})
	if b := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(b, `hx-post="/api/enrol/reconnect"`) {
		t.Fatal("env-pinned token: Reconnect offered")
	}
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	_ = d.Settings.Set(t.Context(), "sync.primary_url", "http://main.local")
	if b := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(b, `hx-post="/api/enrol/reconnect"`) {
		t.Fatal("replica: Reconnect offered")
	}
}

// Facts are a manager's: the view builder withholds them from a cashier too
// (mutant T-J stays dead).
func TestRegisterFactsViewWithholdsFromACashier(t *testing.T) {
	_, d, _ := newLockedOutTill(t, okRegister("x"))
	creq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), cashUser)
	if v := registerFactsView(creq, d, "en"); v != nil {
		t.Fatalf("cashier facts = %v", v)
	}
	mreq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	if v := registerFactsView(mreq, d, "en"); v == nil || v["deviceID"] != enroll.CurrentStatus().DeviceID {
		t.Fatalf("manager facts = %v", v)
	}
}

// Facts render for a not-registered, blocked till too (the state bug of H2).
func TestSettings_FactsShownWhenBlocked(t *testing.T) {
	useTempDataDir(t)
	mux, _, d := newFullAuthDeps(t)
	blockUntil(t, d, time.Now().Add(30*time.Hour))
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, `data-testid="register-facts"`) || !strings.Contains(body, "Key fingerprint") {
		t.Fatal("blocked till: facts missing")
	}
}

func reconnectForm() url.Values { return url.Values{} }

// Test 21 (page half): 200 replaces the token and ends the lock-out.
func TestReconnect_SuccessClearsTheLockout(t *testing.T) {
	mux, d, c := newLockedOutTill(t, func(n int, body map[string]any, w http.ResponseWriter) {
		okRegister("tok-new")(n, body, w)
	})
	var cleared atomic.Int32
	orig := clearAuthLockout
	clearAuthLockout = func() { cleared.Add(1) }
	t.Cleanup(func() { clearAuthLockout = orig })

	rec := postForm(mux, "/api/enrol/reconnect", reconnectForm(), &mgrUser)
	if !strings.Contains(rec.Body.String(), "Reconnected. This till is signed in to the cloud again.") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if cleared.Load() != 1 {
		t.Fatalf("lock-out cleared %d times", cleared.Load())
	}
	if v, _, _ := d.Settings.Get(t.Context(), "marketplace.token"); v != "tok-new" {
		t.Fatalf("token = %q", v)
	}
	if got := c.body(1)["expect_store_id"]; got != "store-t" {
		t.Fatalf("expect_store_id = %v", got)
	}
}

func TestReconnect_OutcomeTexts(t *testing.T) {
	usable := time.Now().UTC().Add(40 * time.Hour).Truncate(time.Minute)
	for name, tc := range map[string]struct {
		answer func(w http.ResponseWriter)
		want   []string
	}{
		"key refused": {func(w http.ResponseWriter) { pageCode(w, 409, "key_required", map[string]any{"challenge": "c"}) },
			[]string{"The cloud knows a different key for this till", "device ID and key fingerprint below"}},
		"device live": {func(w http.ResponseWriter) {
			pageCode(w, 409, "device_live", map[string]any{"usable_from": usable.Format(time.RFC3339)})
		}, []string{"This till was in use recently", "<bdi>"}},
		"not reusable": {func(w http.ResponseWriter) { pageCode(w, 409, "store_not_reusable", nil) },
			[]string{"claimed or removed", "Pair it with a code from the shop owner"}},
		"rate limited": {func(w http.ResponseWriter) { pageCode(w, 429, "rate_limited", nil) }, []string{"Too many tries. Wait a few minutes and try again."}},
		"update needed": {func(w http.ResponseWriter) { pageCode(w, 409, "key_required", nil) },
			[]string{"software is too old to prove its identity", "Update this till"}},
		"server error": {func(w http.ResponseWriter) { http.Error(w, "boom", 500) }, []string{"The cloud could not do this right now", "Selling is not affected"}},
		"503":          {func(w http.ResponseWriter) { http.Error(w, "boom", 503) }, []string{"The cloud could not do this right now"}},
		"unmapped 4xx": {func(w http.ResponseWriter) { pageCode(w, 400, "bad_request", nil) }, []string{"The cloud could not do this right now"}},
		"other shop": {func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"store_id":"store-OTHER","merchant_id":"m-o","token":"tok-other"}}`))
		}, []string{"answer was for a different shop", "device ID and key fingerprint above"}},
	} {
		t.Run(name, func(t *testing.T) {
			mux, d, _ := newLockedOutTill(t, func(_ int, _ map[string]any, w http.ResponseWriter) { tc.answer(w) })
			rec := postForm(mux, "/api/enrol/reconnect", reconnectForm(), &mgrUser)
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Fatalf("body lacks %q: %s", w, body)
				}
			}
			for _, no := range []string{"Could not reach the cloud"} {
				if name != "other shop" && strings.Contains(body, no) && (name == "server error" || name == "503" || name == "unmapped 4xx") {
					t.Fatalf("body says %q for an answered request: %s", no, body)
				}
			}
			if name == "other shop" {
				for _, no := range []string{"claimed or removed", "Reconnected."} {
					if strings.Contains(body, no) {
						t.Fatalf("other-shop answer shows %q: %s", no, body)
					}
				}
			}
			for _, raw := range []string{"RAWBODY", "key_required", "device_live", "store_not_reusable", "boom", "store-OTHER", "bad_request"} {
				if strings.Contains(body, raw) {
					t.Fatalf("raw cloud text %q rendered: %s", raw, body)
				}
			}
			if v, _, _ := d.Settings.Get(t.Context(), "marketplace.token"); v != "tok-old" || !enroll.CurrentStatus().Registered {
				t.Fatalf("a refused reconnect changed the till: token %q", v)
			}
		})
	}
}

func TestReconnect_RefusedForACashierAndNeverCallsTheCloud(t *testing.T) {
	mux, _, c := newLockedOutTill(t, okRegister("tok-new"))
	postForm(mux, "/api/enrol/reconnect", reconnectForm(), &cashUser)
	postForm(mux, "/api/enrol/new-shop", url.Values{"confirm_new_shop": {"1"}}, &cashUser)
	if c.calls.Load() != 1 { // only the seed registration
		t.Fatalf("a cashier reached the cloud: %d calls", c.calls.Load())
	}
	if !enroll.CurrentStatus().Registered {
		t.Fatal("a cashier changed the identity")
	}
}

// Test 22 (D6): nothing renders or ticks into a register call on its own.
func TestLockedOutTillNeverRegistersWithoutTheOperator(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-new"))
	for i := 0; i < 4; i++ {
		getAs(mux, "/ui/cloud-auth-chip", &mgrUser)
		getAs(mux, "/settings", &mgrUser)
		enroll.EnsureRegistered(t.Context(), d.Cfg, d.Settings)
	}
	if c.calls.Load() != 1 {
		t.Fatalf("register called %d times without an operator action", c.calls.Load()-1)
	}
}

// Test 24 (page half): a confirm first, nothing changes until it is given.
func TestNewShop_NeedsTheConfirm(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	old := enroll.CurrentStatus().DeviceID
	rec := postForm(mux, "/api/enrol/new-shop", url.Values{}, &mgrUser)
	body := rec.Body.String()
	for _, want := range []string{"confirm-modal", "Register as a new shop?", "You cannot undo this", "Yes, register as a new shop", `value="1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("confirm lacks %q: %s", want, body)
		}
	}
	if c.calls.Load() != 1 || enroll.CurrentStatus().DeviceID != old {
		t.Fatal("the till changed before the confirm")
	}
	_ = d
}

func TestNewShop_ConfirmedWipesAndRegistersFirstTime(t *testing.T) {
	mux, d, c := newLockedOutTill(t, func(n int, _ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-new","merchant_id":"store-new","token":"tok-fresh"}}`))
	})
	old := enroll.CurrentStatus().DeviceID
	var cleared atomic.Int32
	orig := clearAuthLockout
	clearAuthLockout = func() { cleared.Add(1) }
	t.Cleanup(func() { clearAuthLockout = orig })
	pin := seedNewShopManagerPIN(t, d)
	rec := postForm(mux, "/api/enrol/new-shop", newShopForm(pin), &mgrUser)
	if !strings.Contains(rec.Body.String(), "Registered as a new shop. This till has a new device ID.") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	var approver int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'new_shop_registered' AND actor_id = ?`, mgrUser.ID).Scan(&approver); err != nil || approver == 0 {
		t.Fatalf("no audit row naming the approver: %d (%v)", approver, err)
	}
	nb := c.body(1)
	if nb["device_id"] == old || nb["expect_store_id"] != nil {
		t.Fatalf("second register body: %v", nb)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "marketplace.token"); v != "tok-fresh" || cleared.Load() != 1 {
		t.Fatalf("token %q cleared %d", v, cleared.Load())
	}
}

// ut-docs#5145 (tester v2 D2): the register succeeded but the follow-up
// signing-key fetch failed. The wipe ran and the till IS registered: the page
// must say so (not "Nothing changed"), clear the lock-out, audit, and check in.
func TestNewShop_RegisteredButSigningKeyFetchFailedTellsTheTruth(t *testing.T) {
	mux, d, c := newLockedOutTill(t, func(n int, _ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-new","merchant_id":"store-new","token":"tok-fresh"}}`))
	})
	d.Cfg.Marketplace.PublicKey = "" // the key is fetched after registering; the stub answers 401
	old := enroll.CurrentStatus().DeviceID
	var cleared atomic.Int32
	orig := clearAuthLockout
	clearAuthLockout = func() { cleared.Add(1) }
	t.Cleanup(func() { clearAuthLockout = orig })
	rec := postForm(mux, "/api/enrol/new-shop", newShopForm(seedNewShopManagerPIN(t, d)), &mgrUser)
	body := rec.Body.String()
	if c.calls.Load() != 2 || enroll.CurrentStatus().DeviceID == old || !enroll.CurrentStatus().Registered {
		t.Fatalf("precondition: calls %d, registered %v", c.calls.Load(), enroll.CurrentStatus().Registered)
	}
	if strings.Contains(body, "Nothing changed") || strings.Contains(body, "error") || !strings.Contains(body, "Registered as a new shop. This till has a new device ID.") || !strings.Contains(body, "signing key for plugins") {
		t.Fatalf("body does not tell the truth: %s", body)
	}
	if cleared.Load() != 1 {
		t.Fatalf("lock-out cleared %d times, want 1", cleared.Load())
	}
}

func TestNewShop_FailureIsTheOrdinaryNotRegisteredState(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, func(_ int, _ map[string]any, w http.ResponseWriter) { http.Error(w, "boom", 500) })
	rec := postForm(mux, "/api/enrol/new-shop", newShopForm(seedNewShopManagerPIN(t, d)), &mgrUser)
	if !strings.Contains(rec.Body.String(), "This till is no longer in the old shop and is not registered yet. It will try again by itself.") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if enroll.CurrentStatus().Registered {
		t.Fatal("still registered after a failed new-shop register")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "marketplace.token"); v != "" {
		t.Fatalf("token = %q", v)
	}
}

// A key refusal quiets the not-registered till: chip says so, the lazy path is silent.
func TestKeyRefusedShowsTheBlockedChipForANotRegisteredTill(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	if err := d.Settings.Set(t.Context(), "marketplace.device_register_key_refused", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
	if !strings.Contains(body, `data-cloud-auth="blocked"`) || !strings.Contains(body, "different key") {
		t.Fatalf("chip = %s", body)
	}
	// ut-docs#6424 (L1): the Settings notice offers no action that does not exist here.
	settings := strings.ToLower(getAs(mux, "/settings", &mgrUser).Body.String())
	i := strings.Index(settings, `data-testid="register-blocked"`)
	if i < 0 {
		t.Fatalf("no register-blocked notice: %s", settings)
	}
	notice := settings[i:]
	if j := strings.Index(notice, "</div>"); j >= 0 {
		notice = notice[:j]
	}
	if !strings.Contains(notice, "it will not register it") || strings.Contains(notice, "register as a new shop") || strings.Contains(notice, "reconnect sooner") {
		t.Fatalf("settings notice for a not-registered key-refused till: %s", notice)
	}
}

// ut-docs#5145 (v2 L1): "Register as a new shop" is refused by the server
// unless the till is in the anonymous lock-out; a refusal wipes and audits
// nothing.
func newShopRefusedAndNothingChanged(t *testing.T, mux *http.ServeMux, d *common.Deps, c *lockoutCloud, wantCalls int32, wantRefusalText bool) {
	t.Helper()
	before := enroll.CurrentStatus()
	tok, _, _ := d.Settings.Get(t.Context(), "marketplace.token")
	rec := postForm(mux, "/api/enrol/new-shop", newShopForm(seedNewShopManagerPIN(t, d)), &mgrUser)
	body := rec.Body.String()
	after := enroll.CurrentStatus()
	tok2, _, _ := d.Settings.Get(t.Context(), "marketplace.token")
	if after.DeviceID != before.DeviceID || after.StoreID != before.StoreID || after.Registered != before.Registered || tok2 != tok {
		t.Fatalf("identity changed: %+v -> %+v token %q -> %q; body %s", before, after, tok, tok2, body)
	}
	if c != nil && c.calls.Load() != wantCalls {
		t.Fatalf("cloud called %d times, want %d", c.calls.Load(), wantCalls)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('new_shop_identity_reset','new_shop_registered')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit rows for a reset that did not happen: %d (%v)", n, err)
	}
	if strings.Contains(body, "no longer in the old shop") || strings.Contains(body, "Registered as a new shop") {
		t.Fatalf("report claims a reset: %s", body)
	}
	if wantRefusalText && (!strings.Contains(body, "only works for a till that lost its cloud sign-in") || strings.Contains(body, "Replicas and tills")) {
		t.Fatalf("refusal text: %s", body)
	}
}

func TestNewShop_RefusedOnAHealthyTill(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	cloudAuthChipStatus = func() cloudsync.AuthChip { return cloudsync.AuthChip{} }
	newShopRefusedAndNothingChanged(t, mux, d, c, 1, true)
}

func TestNewShop_RefusedOnAClaimedShop(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	claimStore(t, d)
	newShopRefusedAndNothingChanged(t, mux, d, c, 1, true)
}

func TestNewShop_RefusedOnANotRegisteredTill(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeUnauthorized})
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = "http://127.0.0.1:1/api"
	if enroll.CurrentStatus().Registered {
		t.Fatal("precondition: registered")
	}
	newShopRefusedAndNothingChanged(t, mux, d, nil, 0, true)
}

// Refused before the wipe on a legitimately locked-out till (not configured):
// no reset report, no audit row.
func TestNewShop_RefusalBeforeTheWipeIsNotReportedOrAudited(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	d.Cfg.Marketplace.EndpointURL = ""
	newShopRefusedAndNothingChanged(t, mux, d, c, 1, false)
}

// Fable review M2 (2026-11-16): Reconnect is hidden AND server-refused outside
// the anonymous lock-out, like new-shop: a manager's POST on a healthy till or
// on a till of a claimed shop never reaches the cloud.
func reconnectRefusedAndNothingChanged(t *testing.T, mux *http.ServeMux, d *common.Deps, c *lockoutCloud) {
	t.Helper()
	before := enroll.CurrentStatus()
	tok, _, _ := d.Settings.Get(t.Context(), "marketplace.token")
	rec := postForm(mux, "/api/enrol/reconnect", reconnectForm(), &mgrUser)
	tok2, _, _ := d.Settings.Get(t.Context(), "marketplace.token")
	if after := enroll.CurrentStatus(); after.StoreID != before.StoreID || after.DeviceID != before.DeviceID || tok2 != tok {
		t.Fatalf("identity changed: %+v -> %+v", before, after)
	}
	if c.calls.Load() != 1 { // only the seed registration
		t.Fatalf("cloud called %d times, want 1; body %s", c.calls.Load(), rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Reconnected.") {
		t.Fatalf("claims a reconnect: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "only works for a till that lost its cloud sign-in") || strings.Contains(rec.Body.String(), "Replicas and tills") {
		t.Fatalf("refusal text: %s", rec.Body.String())
	}
}

func TestReconnect_RefusedOnAHealthyTill(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-new"))
	cloudAuthChipStatus = func() cloudsync.AuthChip { return cloudsync.AuthChip{} }
	reconnectRefusedAndNothingChanged(t, mux, d, c)
}

func TestReconnect_RefusedOnAClaimedShop(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-new"))
	claimStore(t, d)
	reconnectRefusedAndNothingChanged(t, mux, d, c)
}

// ut-docs#6424 (L5): an endpoint that cannot be reached is the one case that
// says "Could not reach the cloud".
func TestReconnect_UnreachableSaysCouldNotReach(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, okRegister("tok-new"))
	dead := httptest.NewServer(http.NewServeMux())
	deadURL := dead.URL
	dead.Close()
	d.Cfg.Marketplace.EndpointURL = deadURL + "/api"
	body := postForm(mux, "/api/enrol/reconnect", reconnectForm(), &mgrUser).Body.String()
	if !strings.Contains(body, "Could not reach the cloud") || strings.Contains(body, "could not do this right now") {
		t.Fatalf("body = %s", body)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "marketplace.token"); v != "tok-old" {
		t.Fatalf("token = %q", v)
	}
}

// ut-docs#6424 (L5): busy and not-configured keep their own texts.
func TestReconnectResultHTML_BusyAndNotConfigured(t *testing.T) {
	if got := reconnectResultHTML("en", fmt.Errorf("%w: slot held", enroll.ErrAttemptBusy)); got != html.EscapeString(httpx.T("en", "settings.enrol.busy")) {
		t.Fatalf("busy = %q", got)
	}
	if got := reconnectResultHTML("en", enroll.ErrNotConfigured); got != html.EscapeString(httpx.T("en", "settings.enrol.not_configured")) {
		t.Fatalf("not configured = %q", got)
	}
}

// ut-docs#6424 (T1): the other-shop answer persists nothing and audits nothing.
func TestReconnect_OtherShopWritesNoAudit(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-OTHER","merchant_id":"m-o","token":"tok-other"}}`))
	})
	postForm(mux, "/api/enrol/reconnect", reconnectForm(), &mgrUser)
	if enroll.CurrentStatus().StoreID != "store-t" {
		t.Fatalf("store = %q", enroll.CurrentStatus().StoreID)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'reconnected'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("reconnected audit rows = %d (%v)", n, err)
	}
}

// ut-docs#6424 (D6): a replica or an env-pinned identity keeps "unavailable"
// and the cloud is not called.
func TestReconnect_RefusedOnAReplicaOrEnvPinnedSaysUnavailable(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("x"))
	cfg := &config.Config{}
	cfg.Marketplace.MerchantToken = "pinned-by-env"
	enroll.Init(t.Context(), cfg, d.Settings, &sync.WaitGroup{})
	check := func(label string) {
		t.Helper()
		for _, p := range []string{"/api/enrol/reconnect", "/api/enrol/new-shop"} {
			body := postForm(mux, p, url.Values{"confirm_new_shop": {"1"}}, &mgrUser).Body.String()
			if !strings.Contains(body, "Replicas and tills") || strings.Contains(body, "only works for a till") {
				t.Fatalf("%s %s: body = %s", label, p, body)
			}
		}
		if c.calls.Load() != 1 {
			t.Fatalf("%s: cloud called %d times", label, c.calls.Load())
		}
	}
	check("env-pinned")
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	_ = d.Settings.Set(t.Context(), "sync.primary_url", "http://main.local")
	check("replica")
}

// ut-docs#6424 (L3): the new-shop wipe is a step-up: a manager session needs a PIN too.
func seedNewShopManagerPIN(t *testing.T, d *common.Deps) string {
	t.Helper()
	hash, err := auth.HashPIN(mvPIN)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.NewAuthRepo(d.Db).SetUserPIN(t.Context(), mgrUser.ID, hash); err != nil {
		t.Fatal(err)
	}
	return mvPIN
}

func newShopForm(pin string) url.Values {
	return url.Values{"confirm_new_shop": {"1"}, "override_pin": {pin}}
}

func TestNewShop_ManagerWithoutPINGetsThePrompt(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	seedNewShopManagerPIN(t, d)
	old := enroll.CurrentStatus().DeviceID
	body := postForm(mux, "/api/enrol/new-shop", url.Values{"confirm_new_shop": {"1"}}, &mgrUser).Body.String()
	if !isElevationBody(body) || !strings.Contains(body, `name="confirm_new_shop"`) {
		t.Fatalf("no elevation prompt carrying the confirm: %s", body)
	}
	if enroll.CurrentStatus().DeviceID != old || c.calls.Load() != 1 {
		t.Fatalf("changed without a PIN: calls %d", c.calls.Load())
	}
}

func TestNewShop_WrongPINChangesNothing(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	seedNewShopManagerPIN(t, d)
	old := enroll.CurrentStatus().DeviceID
	body := postForm(mux, "/api/enrol/new-shop", newShopForm("000000"), &mgrUser).Body.String()
	if !isElevationBody(body) || !strings.Contains(body, httpx.T("en", "elevation.error_invalid_pin")) {
		t.Fatalf("no invalid-PIN prompt: %s", body)
	}
	if enroll.CurrentStatus().DeviceID != old || c.calls.Load() != 1 {
		t.Fatal("changed on a wrong PIN")
	}
}

func TestNewShop_CrossOriginRefused(t *testing.T) {
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	pin := seedNewShopManagerPIN(t, d)
	old := enroll.CurrentStatus().DeviceID
	req := httptest.NewRequest(http.MethodPost, "/api/enrol/new-shop", strings.NewReader(newShopForm(pin).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(req, mgrUser))
	if strings.Contains(rec.Body.String(), "Registered as a new shop") || enroll.CurrentStatus().DeviceID != old || c.calls.Load() != 1 {
		t.Fatalf("cross-origin request wiped: %s", rec.Body.String())
	}
}

func TestNewShop_CashierWithManagerPINRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, d, c := newLockedOutTill(t, okRegister("tok-fresh"))
	pin := seedNewShopManagerPIN(t, d)
	old := enroll.CurrentStatus().DeviceID
	body := postForm(mux, "/api/enrol/new-shop", newShopForm(pin), &cashUser).Body.String()
	if strings.Contains(body, "Registered as a new shop") || enroll.CurrentStatus().DeviceID != old || c.calls.Load() != 1 {
		t.Fatalf("cashier wiped the till: %s", body)
	}
}

func isElevationBody(body string) bool {
	return strings.Contains(body, "override_pin")
}

// ut-docs#6424 (UX): after a wipe, a server error adds no "Nothing changed".
func TestNewShop_WipedThenServerErrorDoesNotSayNothingChanged(t *testing.T) {
	mux, d, _ := newLockedOutTill(t, func(_ int, _ map[string]any, w http.ResponseWriter) { http.Error(w, "boom", 500) })
	body := postForm(mux, "/api/enrol/new-shop", newShopForm(seedNewShopManagerPIN(t, d)), &mgrUser).Body.String()
	if !strings.Contains(body, "no longer in the old shop") || strings.Contains(body, "Nothing changed") || strings.Contains(body, "Try again later") {
		t.Fatalf("body = %s", body)
	}
}
