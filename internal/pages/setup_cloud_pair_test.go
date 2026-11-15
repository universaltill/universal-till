package pages

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0166 / ut-docs#4610: the wizard's "Main till" join choice pairs with the
// shop on my. through POST /api/setup/cloud-pair, holds the pairing in
// memory, and the final POST /api/setup (cloud_paired=1) persists it. The
// review record's CSRF/nonce/ordering findings are requirements here.

const cloudStoreAnswer = `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"merchant-1","device_id":"x","device_token":"` + pairPageToken + `",` +
	`"store":{"name":"Cloud Cafe","country":"DE","currency":"EUR","currency_decimals":2,"catalog_master":"cloud"}},"error":null}`

type cloudPairStub struct {
	calls  atomic.Int32
	mu     sync.Mutex
	status int
	reply  string
	last   map[string]any // the latest /pair request body
}

func (c *cloudPairStub) lastBody() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *cloudPairStub) set(status int, reply string) {
	c.mu.Lock()
	c.status, c.reply = status, reply
	c.mu.Unlock()
}

// cloudWizard boots the wizard against a stub cloud, enrolment live over the
// deps' own settings.
func cloudWizard(t *testing.T) (*http.ServeMux, *common.Deps, *cloudPairStub, string) {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	prevMax := setupCloudPairLimiter.max
	setupCloudPairLimiter.max = 1000
	t.Cleanup(func() { setupCloudPairLimiter.max = prevMax })
	stub := &cloudPairStub{status: 200, reply: cloudStoreAnswer}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only pair calls count: the wizard's own catalogue fetches hit the
		// same endpoint.
		if strings.HasSuffix(r.URL.Path, "/v1/stores/pair") {
			stub.calls.Add(1)
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			stub.mu.Lock()
			stub.last = b
			stub.mu.Unlock()
		}
		stub.mu.Lock()
		st, rep := stub.status, stub.reply
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write([]byte(rep))
	}))
	t.Cleanup(srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = srv.URL + "/api"
	d.CloudSyncNow = make(chan struct{}, 1)
	// Mints the wizard nonce, as the browser's first load does.
	rec := getSetup(mux, "?lang=en", "en")
	m := regexp.MustCompile(`name="setup_nonce" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("GET /setup renders no setup_nonce field:\n%s", rec.Body.String())
	}
	return mux, d, stub, m[1]
}

func cloudPairReq(path, nonce string, form url.Values, mut func(*http.Request)) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "ut_lang=en")
	req.Header.Set("HX-Request", "true")
	if nonce != "" {
		req.Header.Set("X-UT-Setup-Nonce", nonce)
	}
	if mut != nil {
		mut(req)
	}
	return req
}

func doCloudPair(mux *http.ServeMux, nonce string, mut func(*http.Request)) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-pair", nonce, url.Values{"code": {pairPageCode}, "name": {"Till 1"}}, mut))
	return rec
}

func TestCloudPairRefusesCrossSiteAndUnknownHosts(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	for name, tc := range map[string]struct {
		nonce string
		mut   func(*http.Request)
	}{
		"no nonce":          {"", nil},
		"wrong nonce":       {"nope", nil},
		"rebound host":      {nonce, func(r *http.Request) { r.Host = "evil.example:8080" }},
		"cross-site fetch":  {nonce, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		"same-site fetch":   {nonce, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		"foreign origin":    {nonce, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }},
		"null origin":       {nonce, func(r *http.Request) { r.Header.Set("Origin", "null") }},
		"origin other port": {nonce, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:9") }},
	} {
		rec := doCloudPair(mux, tc.nonce, tc.mut)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", name, rec.Code)
		}
	}
	if n := stub.calls.Load(); n != 0 {
		t.Fatalf("a refused request reached the cloud (%d calls): it would pair the till into a stranger's shop", n)
	}
	// The same request, same-origin with the nonce, goes through (also with
	// the nonce in the form field).
	ok := doCloudPair(mux, nonce, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Origin", "http://127.0.0.1:8080")
	})
	if ok.Header().Get("HX-Redirect") != "/setup" {
		t.Fatalf("same-origin pair: code=%d headers=%v body=%s", ok.Code, ok.Header(), ok.Body.String())
	}
}

func TestCloudPairIsFirstBootOnly(t *testing.T) {
	mux, d, stub, nonce := cloudWizard(t)
	seedOperatorWithPIN(t, d.AuthSvc)
	rec := doCloudPair(mux, nonce, nil)
	if rec.Code != http.StatusSeeOther || stub.calls.Load() != 0 {
		t.Fatalf("after first boot: code=%d calls=%d, want 303 and no cloud call", rec.Code, stub.calls.Load())
	}
}

func TestCloudPairPersistsNothingAndReopensOnYourShop(t *testing.T) {
	mux, d, stub, nonce := cloudWizard(t)
	rec := doCloudPair(mux, nonce, nil)
	if rec.Header().Get("HX-Redirect") != "/setup" {
		t.Fatalf("pair: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.calls.Load() != 1 {
		t.Fatalf("cloud calls = %d", stub.calls.Load())
	}
	// Nothing written: no credential, no setup state, no store.
	for _, key := range []string{"marketplace.store_id", "marketplace.merchant_id", "marketplace.token", "setup.completed", "store.name"} {
		if v, ok, _ := d.Settings.Get(t.Context(), key); ok && v != "" && key != "store.name" {
			t.Errorf("%s = %q was persisted before the final post", key, v)
		}
	}
	if enroll.HasCredentials(d.Cfg) {
		t.Fatal("the held pairing is already a usable credential: sync could run before the seed")
	}
	// A reload (or a redirect back to /setup) opens the Your shop step with
	// the cloud's facts, and flags the form as cloud-paired.
	html := getSetup(mux, "?lang=en", "en").Body.String()
	for _, want := range []string{"step: 8", "Cloud Cafe", `name="cloud_paired" value="1"`} {
		if !strings.Contains(html, want) {
			t.Errorf("GET /setup with a held pairing lacks %q", want)
		}
	}
	// The token never reaches the page.
	if strings.Contains(html, pairPageToken) {
		t.Fatal("the device token was rendered into the page")
	}
}

func TestCloudPairNonceIsStableAcrossLoads(t *testing.T) {
	mux, _, _, nonce := cloudWizard(t)
	again := regexp.MustCompile(`name="setup_nonce" value="([^"]+)"`).FindStringSubmatch(getSetup(mux, "?lang=en", "en").Body.String())
	if again == nil || again[1] != nonce {
		t.Fatal("the nonce changed on a later GET /setup: the wizard's own redirects would lose a held pairing")
	}
}

func TestCloudPairErrorsShownInPlace(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	for name, tc := range map[string]struct {
		status int
		reply  string
		want   string
	}{
		"invalid code":   {403, `{"data":null,"error":{"code":"invalid_code","message":"x"}}`, "isn't right"},
		"rate limited":   {429, `{"data":null,"error":{"code":"rate_limited","message":"x"}}`, "Too many attempts"},
		"server error":   {500, `{"data":null,"error":{"code":"pair_failed","message":"x"}}`, "didn't work"},
		"store has till": {409, `{"data":{"existing_till":{"name":"Front counter","last_used_at":"2026-10-20T09:30:00Z"}},"error":{"code":"store_has_till","message":"x"}}`, "Front counter"},
	} {
		stub.set(tc.status, tc.reply)
		rec := doCloudPair(mux, nonce, nil)
		if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "" || !strings.Contains(html.UnescapeString(rec.Body.String()), tc.want) {
			t.Errorf("%s: code=%d redirect=%q body=%s, want an in-place message containing %q", name, rec.Code, rec.Header().Get("HX-Redirect"), html.UnescapeString(rec.Body.String()), tc.want)
		}
	}
	// store_has_till names the date too.
	stub.set(409, `{"data":{"existing_till":{"name":"Front counter","last_used_at":"2026-10-20T09:30:00Z"}},"error":{"code":"store_has_till"}}`)
	if b := doCloudPair(mux, nonce, nil).Body.String(); !strings.Contains(b, "2026") {
		t.Errorf("store_has_till message lacks the last-seen date: %s", b)
	}
	// Nothing was held by any of those: GET /setup stays on step 1.
	if html := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(html, "step: 8") {
		t.Fatal("a refused pairing was held")
	}
}

func TestCloudPairNoNetworkAndNoEndpoint(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	d.Cfg.Marketplace.EndpointURL = "http://127.0.0.1:1/api" // refused connection
	if b := doCloudPair(mux, nonce, nil).Body.String(); !strings.Contains(html.UnescapeString(b), "Can't reach the cloud") {
		t.Errorf("offline: %s", b)
	}
	d.Cfg.Marketplace.EndpointURL = ""
	if b := doCloudPair(mux, nonce, nil).Body.String(); !strings.Contains(html.UnescapeString(b), "can't pair with a shop") {
		t.Errorf("no endpoint: %s", b)
	}
}

func TestCloudPairRequiresATillName(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-pair", nonce, url.Values{"code": {pairPageCode}, "name": {"  "}}, nil))
	if rec.Header().Get("HX-Redirect") != "" || stub.calls.Load() != 0 {
		t.Fatalf("a blank till name paired anyway: %s", rec.Body.String())
	}
}

func TestCloudUnpairDropsHeldPairing(t *testing.T) {
	mux, _, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-unpair", nonce, url.Values{}, nil))
	if rec.Header().Get("HX-Redirect") != "/setup" {
		t.Fatalf("unpair: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if html := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(html, "step: 8") || strings.Contains(html, "Cloud Cafe") {
		t.Fatal("This is not my shop did not drop the pairing")
	}
	// Same guards as the pair call.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-unpair", "", url.Values{}, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unpair without nonce = %d, want 403", rec.Code)
	}
}

func TestCloudPairExpiresAfterThirtyMinutes(t *testing.T) {
	mux, _, _, nonce := cloudWizard(t)
	now := time.Now()
	prev := cloudPairNow
	cloudPairNow = func() time.Time { return now }
	t.Cleanup(func() { cloudPairNow = prev })
	doCloudPair(mux, nonce, nil)
	now = now.Add(31 * time.Minute)
	if html := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(html, "step: 8") {
		t.Fatal("a pairing older than 30 minutes still reopens step 2")
	}
	rec := finalCloudPost(mux, nonce, nil)
	if !strings.Contains(rec.Body.String(), "Pairing expired") {
		t.Fatalf("final post with an expired pairing: code=%d body=%.300s", rec.Code, rec.Body.String())
	}
}

func finalCloudPost(mux *http.ServeMux, nonce string, mut func(url.Values)) *httptest.ResponseRecorder {
	form := url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "cloud_paired": {"1"}, "setup_nonce": {nonce},
		// tampered values the server must ignore on the cloud path:
		"country": {"GB"}, "currency": {"GBP"}, "tax_rate_pct": {"20"}, "store_name": {"Attacker Shop"}, "till_name": {"Till 1"},
		"currency_touched": {"1"},
	}
	if mut != nil {
		mut(form)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup", "", form, nil))
	return rec
}

func TestCloudSetupPersistsCredentialAfterSeedAndIgnoresTamperedFields(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	windows := []time.Duration{}
	prev := openOperatorWindow
	openOperatorWindow = func(w time.Duration) { windows = append(windows, w) }
	t.Cleanup(func() { openOperatorWindow = prev })
	doCloudPair(mux, nonce, nil)

	// Ordering (ADR-0166 §4 step 6): when the credential is committed the
	// seed (gift voucher item) and the shop state are already written.
	var atCommit struct {
		called       bool
		vouchers     int
		storeName    string
		hadCredsThen bool
	}
	prevCommit := commitCloudPairing
	commitCloudPairing = func(ctx context.Context, d *common.Deps, p *enroll.FirstTillPairing) error {
		atCommit.called = true
		_ = d.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE item_type = 'gift_voucher'`).Scan(&atCommit.vouchers)
		atCommit.storeName, _, _ = d.Settings.Get(ctx, "store.name")
		atCommit.hadCredsThen = enroll.HasCredentials(d.Cfg)
		return prevCommit(ctx, d, p)
	}
	t.Cleanup(func() { commitCloudPairing = prevCommit })

	rec := finalCloudPost(mux, nonce, nil)
	if !atCommit.called || atCommit.vouchers != 1 || atCommit.storeName != "Cloud Cafe" || atCommit.hadCredsThen {
		t.Errorf("at Commit: %+v, want called with the seed and store name already written and no credential yet", atCommit)
	}
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/import?welcome=1") {
		t.Fatalf("final post: code=%d loc=%q body=%.300s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	ctx := t.Context()
	get := func(k string) string { v, _, _ := d.Settings.Get(ctx, k); return v }
	if got := get("store.name"); got != "Cloud Cafe" {
		t.Errorf("store.name = %q, want the cloud's", got)
	}
	st := d.CurrentState()
	if st.Country != "DE" || st.Currency != "EUR" {
		t.Errorf("country/currency = %q/%q, want DE/EUR from the cloud, not the posted GB/GBP", st.Country, st.Currency)
	}
	if get(common.KeyCurrencyConfirmed) != "true" {
		t.Error("currency not marked confirmed")
	}
	if get(entitlement.KeyShopCurrencyDecimals) != "2" || get(entitlement.KeyShopCurrency) != "EUR" {
		t.Errorf("shop currency/decimals = %q/%q", get(entitlement.KeyShopCurrency), get(entitlement.KeyShopCurrencyDecimals))
	}
	// The credential is persisted now, after the seed.
	if !enroll.HasCredentials(d.Cfg) {
		t.Fatal("no credential after the final post")
	}
	if get("marketplace.store_id") != pairPageStore {
		t.Errorf("marketplace.store_id = %q", get("marketplace.store_id"))
	}
	// A check-in was requested, and no ADR-0071 question was asked.
	if len(windows) != 1 {
		t.Errorf("operator check-in windows opened = %v, want exactly one", windows)
	}
	select {
	case <-d.CloudSyncNow:
	default:
		t.Error("no check-in kick sent")
	}
	if get(common.KeyAutoRegisterOptIn) != "" {
		t.Error("the cloud path recorded an ADR-0071 answer")
	}
	// The pairing is spent: a second final post cannot reuse it.
	if second := finalCloudPost(mux, nonce, nil); second.Header().Get("Location") != "/login" {
		t.Errorf("a second final post was not refused by the first-boot gate: %d %q", second.Code, second.Header().Get("Location"))
	}
}

func TestCloudSetupRefusals(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	// Missing/wrong nonce and rebound Host: 403, nothing set up.
	for name, mut := range map[string]func(*http.Request){
		"rebound host": func(r *http.Request) { r.Host = "evil.example" },
		"cross-site":   func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		form := url.Values{"pin": {"2468"}, "pin_confirm": {"2468"}, "cloud_paired": {"1"}, "setup_nonce": {nonce}, "store_name": {"X"}, "till_name": {"T"}}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, cloudPairReq("/api/setup", "", form, mut))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: final post = %d, want 403", name, rec.Code)
		}
	}
	if rec := finalCloudPost(mux, "wrong", nil); rec.Code != http.StatusForbidden {
		t.Errorf("wrong nonce: final post = %d, want 403", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "setup.completed"); v == "true" {
		t.Fatal("a refused final post completed setup")
	}
	if enroll.HasCredentials(d.Cfg) {
		t.Fatal("a refused final post persisted the credential")
	}
}

func TestCloudSetupWithoutAHeldPairingAsksForANewCode(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	rec := finalCloudPost(mux, nonce, nil)
	if rec.Code == http.StatusSeeOther || !strings.Contains(rec.Body.String(), "Pairing expired") {
		t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), "setup.completed"); v == "true" {
		t.Fatal("setup completed without a pairing")
	}
}

func TestCloudSetupFallsBackToLocalStepsForMissingFields(t *testing.T) {
	// An older cloud (no store block) and a till-born store (no country,
	// no currency): the posted local answers are used, validated as usual.
	for name, reply := range map[string]string{
		"no store block": `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"m","device_id":"x","device_token":"` + pairPageToken + `"},"error":null}`,
		"empty fields":   `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"m","device_id":"x","device_token":"` + pairPageToken + `","store":{"name":"","country":"","currency":"","catalog_master":"till"}},"error":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			mux, d, stub, nonce := cloudWizard(t)
			stub.set(200, reply)
			doCloudPair(mux, nonce, nil)
			html := getSetup(mux, "?lang=en", "en").Body.String()
			if !strings.Contains(html, "step: 8") {
				t.Fatal("pairing not held")
			}
			rec := finalCloudPost(mux, nonce, func(f url.Values) {
				f.Set("store_name", "Local Name")
				f.Set("country", "FR")
				f.Set("currency", "EUR")
			})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
			}
			if v, _, _ := d.Settings.Get(t.Context(), "store.name"); v != "Local Name" {
				t.Errorf("store.name = %q, want the local answer", v)
			}
			if d.CurrentState().Country != "FR" {
				t.Errorf("country = %q, want FR from the local step", d.CurrentState().Country)
			}
			if !enroll.HasCredentials(d.Cfg) {
				t.Error("credential not persisted")
			}
		})
	}
}

func TestCloudPairHeldStoreFieldsAreRenderedOnlyFromTheCloud(t *testing.T) {
	// A store name carrying markup is escaped in the Your shop step.
	mux, _, stub, nonce := cloudWizard(t)
	stub.set(200, strings.Replace(cloudStoreAnswer, "Cloud Cafe", `<script>alert(1)</script>`, 1))
	doCloudPair(mux, nonce, nil)
	if html := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(html, "<script>alert(1)") {
		t.Fatal("cloud-supplied store name rendered unescaped")
	}
}

func TestJoinScreenOffersMainTillChoice(t *testing.T) {
	mux, _, _, _ := cloudWizard(t)
	html := getSetup(mux, "?lang=en", "en").Body.String()
	for _, want := range []string{`value="main"`, "Main till (first till of the shop)", `/api/setup/cloud-pair`, `hx-headers`} {
		if !strings.Contains(html, want) {
			t.Errorf("join screen lacks %q", want)
		}
	}
}

func TestCloudPairIsRateLimited(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	setupCloudPairLimiter.max = 2
	setupCloudPairLimiter.hits = map[string][]time.Time{}
	var last *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		last = doCloudPair(mux, nonce, func(r *http.Request) { r.RemoteAddr = "192.0.2.77:1234" })
	}
	if last.Code != http.StatusTooManyRequests || stub.calls.Load() != 2 {
		t.Fatalf("third call: code=%d cloud calls=%d, want 429 and 2", last.Code, stub.calls.Load())
	}
}

// Review finding 1: a cloud shop named like the name field's own placeholder
// ("My Shop") is refused by the final post, so the wizard must ask the local
// name step instead of locking the shop out; a local answer wins.
func TestCloudShopNamedLikeThePlaceholderAsksForALocalName(t *testing.T) {
	for _, cloudName := range []string{"My Shop", "my shop"} {
		mux, d, stub, nonce := cloudWizard(t)
		stub.set(200, strings.Replace(cloudStoreAnswer, "Cloud Cafe", cloudName, 1))
		doCloudPair(mux, nonce, nil)
		if html := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(html, ">"+cloudName+"<") {
			t.Errorf("%q: the refused cloud name is shown as the shop's name", cloudName)
		}
		rec := finalCloudPost(mux, nonce, func(f url.Values) { f.Set("store_name", "Local Name") })
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%q: code=%d body=%.300s", cloudName, rec.Code, rec.Body.String())
		}
		if v, _, _ := d.Settings.Get(t.Context(), "store.name"); v != "Local Name" {
			t.Errorf("%q: store.name = %q, want the locally typed name", cloudName, v)
		}
	}
}

// Review finding 2: a failed Commit must not look like success.
func TestCloudSetupCommitFailureIsSurfaced(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	prev := commitCloudPairing
	commitCloudPairing = func(context.Context, *common.Deps, *enroll.FirstTillPairing) error {
		return enroll.ErrAttemptBusy
	}
	t.Cleanup(func() { commitCloudPairing = prev })
	doCloudPair(mux, nonce, nil)
	rec := finalCloudPost(mux, nonce, nil)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/import?welcome=1") || strings.Contains(loc, "syncing=1") || !strings.Contains(loc, "pairfail=1") {
		t.Fatalf("location = %q (code %d), want the welcome page flagged pairfail=1 and not syncing", loc, rec.Code)
	}
	if enroll.HasCredentials(d.Cfg) {
		t.Fatal("credential persisted despite the failed commit")
	}
}

func TestImportWelcomeShowsPairingIncompleteNoteNotSyncing(t *testing.T) {
	mux, _, _ := newSampleDataDeps(t)
	body := getImportAs(mux, "/import?welcome=1&pairfail=1", mgrUser, false).Body.String()
	if !strings.Contains(body, "cloud-pair-incomplete") || strings.Contains(body, "cloud-syncing-chip") {
		t.Fatalf("want the pairing-incomplete note and no syncing chip:\n%.400s", body)
	}
	if plain := getImportAs(mux, "/import?welcome=1", mgrUser, false).Body.String(); strings.Contains(plain, "cloud-pair-incomplete") {
		t.Error("note shown without pairfail=1")
	}
}

// ut-docs#4803: a till that joined a my. shop is already registered, so the
// last step must not ask the ADR-0071 opt-in or the terms; a fresh wizard still does.
func TestSetupDoneStepAsksRegistrationOnlyOffThePairedPath(t *testing.T) {
	mux, _, _, nonce := cloudWizard(t)
	fresh := getSetup(mux, "?lang=en", "en").Body.String()
	for _, want := range []string{`name="auto_register"`, `name="accept_terms"`} {
		if !strings.Contains(fresh, want) {
			t.Errorf("unpaired wizard lacks %q", want)
		}
	}
	doCloudPair(mux, nonce, nil)
	paired := getSetup(mux, "?lang=en", "en").Body.String()
	for _, bad := range []string{`name="auto_register"`, `name="accept_terms"`} {
		if strings.Contains(paired, bad) {
			t.Errorf("paired wizard still renders %q", bad)
		}
	}
	if !strings.Contains(paired, `data-testid="setup-joined-shop"`) || !strings.Contains(paired, "Cloud Cafe") {
		t.Error("paired wizard does not show the joined shop on the last step")
	}
}

// ADR-0171 §4 (ut-docs#4830): a shop type chosen on my. is kept with the held
// pairing, the wizard skips its shop-type step and shows the type read-only,
// and the SERVER saves the cloud's value whatever the form posts.

func cloudAnswerWithShopType(st string) string {
	return `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"merchant-1","device_id":"x","device_token":"` + pairPageToken + `",` +
		`"store":{"name":"Cloud Cafe","country":"DE","currency":"EUR","currency_decimals":2,"catalog_master":"cloud","shop_type":"` + st + `"}},"error":null}`
}

func heldWizardPage(t *testing.T, shopType string) (*http.ServeMux, *common.Deps, string, string) {
	t.Helper()
	mux, d, stub, nonce := cloudWizard(t)
	stub.set(200, cloudAnswerWithShopType(shopType))
	doCloudPair(mux, nonce, nil)
	return mux, d, nonce, getSetup(mux, "?lang=en", "en").Body.String()
}

func TestCloudWizardWithKnownShopTypeSkipsTheShopTypeStep(t *testing.T) {
	_, _, _, page := heldWizardPage(t, "cafe")
	if strings.Contains(page, `data-step="5"`) || strings.Contains(page, `name="shop_type"`) {
		t.Error("the shop-type step is still rendered for a cloud-known type")
	}
	if !strings.Contains(page, `data-cloud-field="shop_type"`) || !strings.Contains(page, "Café / coffee shop") {
		t.Error("the Your shop step lacks the read-only type line")
	}
	if !strings.Contains(page, "cloudShopType: true") {
		t.Error("the step navigator is not told to skip step 5")
	}
	// Without the rule itself, Next from step 4 lands on a step 5 that is not
	// on the page: an empty card the owner cannot leave.
	if !strings.Contains(page, "(s === 5 && this.cloud && this.cloudShopType)") {
		t.Error("the step navigator has no rule that skips step 5")
	}
}

func TestCloudWizardWithoutShopTypeStillAsksIt(t *testing.T) {
	for _, st := range []string{"", "spaceship"} {
		_, _, _, page := heldWizardPage(t, st)
		if !strings.Contains(page, `data-step="5"`) || !strings.Contains(page, `name="shop_type"`) {
			t.Errorf("shop_type=%q: the step is not asked", st)
		}
		if strings.Contains(page, `data-cloud-field="shop_type"`) {
			t.Errorf("shop_type=%q: a read-only type line was shown", st)
		}
		if !strings.Contains(page, "cloudShopType: false") {
			t.Errorf("shop_type=%q: navigator would skip the step", st)
		}
	}
}

func TestCloudSetupSavesTheCloudsShopTypeAndIgnoresAPostedOne(t *testing.T) {
	mux, d, nonce, _ := heldWizardPage(t, "cafe")
	rec := finalCloudPost(mux, nonce, func(f url.Values) { f.Set("shop_type", "retail") })
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyShopType); v != "cafe" {
		t.Fatalf("shop.type = %q, want the cloud's cafe (a crafted post must not override it)", v)
	}
}

func TestCloudSetupWithAnEmptyCloudTypeAcceptsThePostedOne(t *testing.T) {
	mux, d, nonce, _ := heldWizardPage(t, "")
	rec := finalCloudPost(mux, nonce, func(f url.Values) { f.Set("shop_type", "retail") })
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyShopType); v != "retail" {
		t.Fatalf("shop.type = %q, want the posted retail", v)
	}
}

func TestCloudSetupShopTypeSideEffectsRunForTheCloudType(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(orig) })
	mux, d, nonce, _ := heldWizardPage(t, "service")
	// A posted cafe must not win over the cloud's service.
	rec := finalCloudPost(mux, nonce, func(f url.Values) { f.Set("shop_type", "cafe") })
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyShopType); v != "service" {
		t.Fatalf("shop.type = %q, want service", v)
	}
	// applyShopTypeOrderTypeDefault ran: a service shop has no eat-in.
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != data.OrderTypePromptModeOff {
		t.Fatalf("%s = %q, want off for a service shop", data.OrderTypePromptModeKey, v)
	}
}

// ut-docs#5127 path B: during a my.-first (cloud-paired) wizard the paired
// credential is held in memory until the seed is written. A pending base
// plugin (a language pack core does not bundle, a German fiscal plugin)
// must not make the till register a till-born store of its own in that
// window: Commit then adopts the paired identity and the till-born store
// would be left behind as a silent orphan. UT-0 holds registration.
func TestCloudSetupDoesNotRegisterATillBornStoreBeforeCommit(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))

	var registers atomic.Int32
	var registersBeforeCommit int32 = -1
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/stores/pair"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cloudStoreAnswer))
		case strings.HasSuffix(r.URL.Path, "/v1/stores/register"):
			registers.Add(1)
			http.Error(w, "stub does not enrol", http.StatusInternalServerError)
		default:
			mkt.server.Config.Handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(front.Close)
	d.Cfg.Marketplace.EndpointURL = front.URL
	d.Cfg.Marketplace.PublicKey = mkt.config().PublicKey

	doCloudPair(mux, nonce, nil)
	if err := addPendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "language", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}
	prevCommit := commitCloudPairing
	commitCloudPairing = func(ctx context.Context, d *common.Deps, p *enroll.FirstTillPairing) error {
		registersBeforeCommit = registers.Load()
		return prevCommit(ctx, d, p)
	}
	t.Cleanup(func() { commitCloudPairing = prevCommit })

	rec := finalCloudPost(mux, nonce, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("final post: code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if registersBeforeCommit != 0 {
		t.Fatalf("/v1/stores/register was called %d time(s) before the pairing was saved: a till-born store would be orphaned (ut-docs#5127)", registersBeforeCommit)
	}
	if enroll.RegistrationHeld() {
		t.Fatal("registration is still held after the wizard finished")
	}
}

// The hold follows the held pairing: dropped by "This is not my shop",
// expired with it, cleared when setup finishes, and never taken by a pair
// call that lands after setup finished (it would be stranded for good).
func TestCloudPairHoldFollowsTheHeldPairing(t *testing.T) {
	mux, _, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	if !enroll.RegistrationHeld() {
		t.Fatal("a held pairing does not hold registration")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-unpair", nonce, url.Values{}, nil))
	if enroll.RegistrationHeld() {
		t.Fatal("cloud-unpair left registration held")
	}

	s := &setupCloudState{}
	s.ensureNonce()
	prev := cloudPairNow
	t.Cleanup(func() { cloudPairNow = prev; enroll.ReleaseRegistration() })
	now := time.Now()
	cloudPairNow = func() time.Time { return now }
	s.hold(&enroll.FirstTillPairing{}, "Till 1")
	if !enroll.RegistrationHeld() {
		t.Fatal("hold() did not hold registration")
	}
	cloudPairNow = func() time.Time { return now.Add(cloudPairHold + time.Minute) }
	if s.current() != nil || enroll.RegistrationHeld() {
		t.Fatal("an expired pairing still holds registration")
	}
	cloudPairNow = func() time.Time { return now }
	s.hold(&enroll.FirstTillPairing{}, "Till 1")
	s.finish()
	if enroll.RegistrationHeld() {
		t.Fatal("finish() left registration held")
	}
	s.hold(&enroll.FirstTillPairing{}, "Till 1") // a late pair call after setup
	if enroll.RegistrationHeld() || s.current() != nil {
		t.Fatal("a pairing that landed after setup finished was held")
	}
}

// After the pairing is saved the pending plugin is retried at once, not on
// the 5-minute tick (ut-docs#5127, design 4.3).
func TestCloudSetupNudgesBasePluginRetryAfterCommit(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	if err := addPendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "language", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}
	// Drain just before Commit: the install attempt's own nudge (a spec left
	// pending) is already spent, so only the post-commit one can satisfy this.
	prevCommit := commitCloudPairing
	commitCloudPairing = func(ctx context.Context, d *common.Deps, p *enroll.FirstTillPairing) error {
		select {
		case <-basePluginRetryNudge:
		default:
		}
		return prevCommit(ctx, d, p)
	}
	t.Cleanup(func() { commitCloudPairing = prevCommit })
	if rec := finalCloudPost(mux, nonce, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("final post: code=%d", rec.Code)
	}
	select {
	case <-basePluginRetryNudge:
	default:
		t.Fatal("no base-plugin retry was nudged after the pairing was saved")
	}
}
