package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// ADR-0166 §2-§4 (ut-docs#4610): the wizard's pair-first path. PairFirstTill
// posts the owner's code with first_till:true and returns the answer WITHOUT
// persisting or adopting anything; Commit (the wizard's final post) is what
// writes the credential.

type firstTillCloud struct {
	srv    *httptest.Server
	mu     sync.Mutex
	body   map[string]any
	status int
	reply  string
}

func newFirstTillCloud(t *testing.T, status int, reply string) *firstTillCloud {
	t.Helper()
	c := &firstTillCloud{status: status, reply: reply}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.mu.Lock()
		c.body = b
		st, rep := c.status, c.reply
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write([]byte(rep))
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *firstTillCloud) sent() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

const firstTillAnswer = `{"data":{"store_id":"store-paired","merchant_id":"merchant-paired","device_id":"x","device_token":"` + pairToken + `",
"store":{"name":"Corner Shop","country":"DE","currency":"EUR","currency_decimals":2,"catalog_master":"cloud"}},"error":null}`

func bootFirstBoot(t *testing.T, endpoint string) (*orderedKV, *config.Config) {
	t.Helper()
	resetState()
	t.Cleanup(resetState)
	kv := newOrderedKV()
	_ = kv.fakeKV.Set(context.Background(), keyPublicKey, pinnedKey)
	cfg := &config.Config{}
	initForTest(t, cfg, kv)
	cfg.Marketplace.EndpointURL = endpoint + "/api"
	return kv, cfg
}

func TestPairFirstTillSendsFirstTillAndPersistsNothing(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)
	kv, cfg := bootFirstBoot(t, cloud.srv.URL)
	before := len(kv.writes)

	p, err := PairFirstTill(context.Background(), cfg, "  "+pairCode+" ", "Till 1", "")
	if err != nil {
		t.Fatalf("PairFirstTill: %v", err)
	}
	b := cloud.sent()
	if b["first_till"] != true || b["code"] != pairCode || b["device_name"] != "Till 1" {
		t.Fatalf("sent body = %v", b)
	}
	if id, _ := b["device_id"].(string); !strings.HasPrefix(id, "till-") || id != p.DeviceID {
		t.Fatalf("device_id = %q, held %q", id, p.DeviceID)
	}
	if p.StoreID != pairStore || p.MerchantID != pairMerchant || p.Token != pairToken {
		t.Fatalf("pairing = %+v", p)
	}
	if p.Store == nil || p.Store.Name != "Corner Shop" || p.Store.Country != "DE" || p.Store.Currency != "EUR" ||
		p.Store.CurrencyDecimals == nil || *p.Store.CurrencyDecimals != 2 || p.Store.CatalogMaster != "cloud" {
		t.Fatalf("store block = %+v", p.Store)
	}
	// Nothing persisted, nothing adopted: no credential for any sync to use.
	if len(kv.writes) != before {
		t.Fatalf("PairFirstTill wrote settings: %v", kv.writes[before:])
	}
	if m := Effective(cfg).Marketplace; m.MerchantToken != "" || m.StoreID != "" {
		t.Fatalf("effective marketplace adopted the pairing: %+v", m)
	}
}

func TestPairFirstTillOlderCloudHasNoStoreBlock(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, `{"data":{"store_id":"s","merchant_id":"m","device_id":"x","device_token":"`+pairToken+`"},"error":null}`)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Store != nil {
		t.Fatalf("Store = %+v, want nil for an older cloud", p.Store)
	}
}

func TestPairFirstTillStoreHasTill(t *testing.T) {
	cloud := newFirstTillCloud(t, 409, `{"data":{"existing_till":{"name":"Front counter","last_used_at":"2026-10-20T09:30:00Z"}},"error":{"code":"store_has_till","message":"x"}}`)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
	var has *StoreHasTillError
	if !errors.As(err, &has) {
		t.Fatalf("err = %v, want *StoreHasTillError", err)
	}
	if has.Name != "Front counter" || has.LastUsedAt != "2026-10-20T09:30:00Z" {
		t.Fatalf("existing till = %+v", has)
	}
}

func TestPairFirstTillCloudErrorsKeepStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{403, "invalid_code"}, {429, "rate_limited"}} {
		cloud := newFirstTillCloud(t, tc.status, `{"data":null,"error":{"code":"`+tc.code+`","message":"nope"}}`)
		_, cfg := bootFirstBoot(t, cloud.srv.URL)
		_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
		var he *RegisterHTTPError
		if !errors.As(err, &he) || he.Status != tc.status || he.Code != tc.code {
			t.Fatalf("%d: err = %v", tc.status, err)
		}
	}
}

func TestPairFirstTillRefusals(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)

	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	cfg.Marketplace.EndpointURL = ""
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "T", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no endpoint: err = %v, want ErrNotConfigured", err)
	}

	_, cfg = bootFirstBoot(t, cloud.srv.URL)
	mu.Lock()
	tokenExplicit = true
	mu.Unlock()
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "T", ""); !errors.Is(err, ErrPairIdentityPinned) {
		t.Fatalf("pinned identity: err = %v, want ErrPairIdentityPinned", err)
	}
	if cloud.sent() != nil {
		t.Fatal("a refused pairing reached the cloud (it would burn the code)")
	}

	_, cfg = bootFirstBoot(t, cloud.srv.URL)
	if _, err := PairFirstTill(context.Background(), cfg, "  ", "T", ""); err == nil {
		t.Fatal("blank code accepted")
	}
}

func TestPairFirstTillFreshDeviceIDEveryRun(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	a, err1 := PairFirstTill(context.Background(), cfg, pairCode, "T", "")
	b, err2 := PairFirstTill(context.Background(), cfg, pairCode, "T", "")
	if err1 != nil || err2 != nil || a.DeviceID == b.DeviceID {
		t.Fatalf("device ids (%v %v), want two fresh ones", err1, err2)
	}
}

func TestFirstTillPairingCommitPersistsCredential(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)
	kv, cfg := bootFirstBoot(t, cloud.srv.URL)
	p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Commit(context.Background(), cfg, kv)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for key, want := range map[string]string{
		keyStoreID:          pairStore,
		keyMerchantID:       pairMerchant,
		keyToken:            pairToken,
		keyDeviceID:         p.DeviceID,
		keyDeviceRegistered: p.DeviceID,
	} {
		if got := kv.get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if kv.get(keyEnrolledAt) == "" {
		t.Fatal("enrolled_at not set")
	}
	if !st.Registered || st.StoreID != pairStore {
		t.Fatalf("status = %+v", st)
	}
	m := Effective(cfg).Marketplace
	if m.MerchantToken != pairToken || m.DeviceID != p.DeviceID || m.StoreID != pairStore || m.ClientID != pairMerchant {
		t.Fatalf("effective marketplace = %+v", m)
	}
}

// ADR-0171 §3-4 (ut-docs#4830): the pair answer's store.shop_type is kept
// only when it is an ADR-0026 value; unknown, mistyped or missing is "".
func TestPairFirstTillParsesShopType(t *testing.T) {
	answer := func(storeJSON string) string {
		return `{"data":{"store_id":"s","merchant_id":"m","device_id":"x","device_token":"` + pairToken + `","store":` + storeJSON + `},"error":null}`
	}
	cases := []struct{ name, store, want string }{
		{"valid", `{"name":"A","shop_type":"cafe"}`, "cafe"},
		{"valid market_stall", `{"name":"A","shop_type":"market_stall"}`, "market_stall"},
		{"unknown value", `{"name":"A","shop_type":"spaceship"}`, ""},
		{"wrong case is unknown", `{"name":"A","shop_type":"Cafe"}`, ""},
		{"empty", `{"name":"A","shop_type":""}`, ""},
		{"missing (old cloud)", `{"name":"A"}`, ""},
		{"wrong json type", `{"name":"A","shop_type":7}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cloud := newFirstTillCloud(t, 200, answer(tc.store))
			_, cfg := bootFirstBoot(t, cloud.srv.URL)
			p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
			if err != nil {
				t.Fatalf("PairFirstTill: %v", err)
			}
			if p.Store == nil || p.Store.ShopType != tc.want {
				t.Fatalf("ShopType = %+v, want %q", p.Store, tc.want)
			}
		})
	}
}

// ut-docs#5058 (ADR-0166 §3 amendment 2026-10-26): replace codes. A replace
// code without replace_device_id answers 409 replace_confirm_required with
// the old till and the store block; the wizard confirms by sending the same
// request again with replace_device_id.

const replaceConfirmAnswer = `{"data":{"existing_till":{"device_id":"till-old","name":"Counter till","last_used_at":"2026-10-25T18:04:00Z"},` +
	`"store":{"name":"Corner Shop","country":"GB","currency":"GBP","currency_decimals":2,"catalog_master":"cloud","shop_type":"cafe"}},` +
	`"error":{"code":"replace_confirm_required","message":"x"}}`

func TestPairFirstTillReplaceConfirmRequired(t *testing.T) {
	cloud := newFirstTillCloud(t, 409, replaceConfirmAnswer)
	kv, cfg := bootFirstBoot(t, cloud.srv.URL)
	before := len(kv.writes)
	_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
	var rc *ReplaceConfirmError
	if !errors.As(err, &rc) {
		t.Fatalf("err = %v, want *ReplaceConfirmError", err)
	}
	if rc.DeviceID != "till-old" || rc.Name != "Counter till" || rc.LastUsedAt != "2026-10-25T18:04:00Z" {
		t.Fatalf("existing till = %+v", rc)
	}
	if rc.Store == nil || rc.Store.Name != "Corner Shop" || rc.Store.CatalogMaster != "cloud" {
		t.Fatalf("store block = %+v", rc.Store)
	}
	if _, sent := cloud.sent()[replaceDeviceIDField]; sent {
		t.Fatal("an unconfirmed pairing sent replace_device_id")
	}
	if len(kv.writes) != before {
		t.Fatalf("a 409 wrote %d settings", len(kv.writes)-before)
	}
}

func TestPairFirstTillReplaceSendsTargetAndReadsReplaced(t *testing.T) {
	answer := `{"data":{"store_id":"store-paired","merchant_id":"merchant-paired","device_id":"x","device_token":"` + pairToken + `",` +
		`"store":{"name":"Corner Shop","country":"GB","currency":"GBP","catalog_master":"cloud"},` +
		`"replaced":{"device_id":"till-old","name":"Counter till"}},"error":null}`
	cloud := newFirstTillCloud(t, 200, answer)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "till-old")
	if err != nil {
		t.Fatalf("PairFirstTillReplace: %v", err)
	}
	b := cloud.sent()
	if b[replaceDeviceIDField] != "till-old" || b["first_till"] != true {
		t.Fatalf("request = %v, want first_till and replace_device_id=till-old", b)
	}
	if p.Replaced == nil || p.Replaced.DeviceID != "till-old" || p.Replaced.Name != "Counter till" {
		t.Fatalf("Replaced = %+v", p.Replaced)
	}
}

func TestPairFirstTillReplaceRefusesAnOversizedTarget(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "T", strings.Repeat("x", 201)); err == nil {
		t.Fatal("a 201-character replace_device_id was sent")
	}
	if cloud.sent() != nil {
		t.Fatal("the refused request reached the cloud and could burn the code")
	}
}

func TestPairFirstTillMainTillOnline(t *testing.T) {
	cloud := newFirstTillCloud(t, 409, `{"data":{"last_seen":"2026-10-26T10:01:00Z"},"error":{"code":"main_till_online","message":"x"}}`)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "till-old")
	var on *MainTillOnlineError
	if !errors.As(err, &on) || on.LastSeen != "2026-10-26T10:01:00Z" {
		t.Fatalf("err = %v, want *MainTillOnlineError with last_seen", err)
	}
}

func TestPairFirstTillReplaceRefusalsKeepTheirCode(t *testing.T) {
	for _, code := range []string{"replace_target_changed", "replace_needs_owner", "replace_code_main_till_only"} {
		cloud := newFirstTillCloud(t, 409, `{"data":null,"error":{"code":"`+code+`","message":"x"}}`)
		_, cfg := bootFirstBoot(t, cloud.srv.URL)
		_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "till-old")
		var he *RegisterHTTPError
		if !errors.As(err, &he) || he.Status != 409 || he.Code != code {
			t.Fatalf("%s: err = %v", code, err)
		}
	}
}

// ut-docs#5098: a failed pair attempt is a Problem the my. "Attention
// needed" panel shows; a later successful pairing resolves it, so a freshly
// joined, healthy shop does not report the first attempt's refusal.
func TestPairFailureProblemClearsOnSuccessfulPair_5098(t *testing.T) {
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	cloud := newFirstTillCloud(t, 409, `{"data":{"existing_till":{"name":"Front counter"}},"error":{"code":"store_has_till","message":"x"}}`)
	kv, cfg := bootFirstBoot(t, cloud.srv.URL)
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", ""); err == nil {
		t.Fatal("want the store_has_till refusal")
	}
	if open := logging.OpenProblems(time.Now(), 0); len(open) != 1 {
		t.Fatalf("open problems after the refusal = %+v, want 1", open)
	}
	cloud.mu.Lock()
	cloud.status, cloud.reply = 200, firstTillAnswer
	cloud.mu.Unlock()
	p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Commit(context.Background(), cfg, kv); err != nil {
		t.Fatal(err)
	}
	if open := logging.OpenProblems(time.Now(), 0); len(open) != 0 {
		t.Fatalf("open problems after the successful pair = %+v, want none", open)
	}
}

func TestSettingsPairFailureProblemClearsOnSuccessfulPair_5098(t *testing.T) {
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	cloud := newFirstTillCloud(t, 403, `{"data":null,"error":{"code":"invalid_code","message":"x"}}`)
	kv, cfg := bootFirstBoot(t, cloud.srv.URL)
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err == nil {
		t.Fatal("want the refusal")
	}
	if open := logging.OpenProblems(time.Now(), 0); len(open) != 1 {
		t.Fatalf("open problems after the refusal = %+v, want 1", open)
	}
	cloud.mu.Lock()
	cloud.status, cloud.reply = 200, firstTillAnswer
	cloud.mu.Unlock()
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
		t.Fatal(err)
	}
	if open := logging.OpenProblems(time.Now(), 0); len(open) != 0 {
		t.Fatalf("open problems after the successful pair = %+v, want none", open)
	}
}
