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

	"github.com/universaltill/universal-till/internal/config"
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

	p, err := PairFirstTill(context.Background(), cfg, "  "+pairCode+" ", "Till 1")
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
	p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1")
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
	_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1")
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
		_, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1")
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
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "T"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no endpoint: err = %v, want ErrNotConfigured", err)
	}

	_, cfg = bootFirstBoot(t, cloud.srv.URL)
	mu.Lock()
	tokenExplicit = true
	mu.Unlock()
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "T"); !errors.Is(err, ErrPairIdentityPinned) {
		t.Fatalf("pinned identity: err = %v, want ErrPairIdentityPinned", err)
	}
	if cloud.sent() != nil {
		t.Fatal("a refused pairing reached the cloud (it would burn the code)")
	}

	_, cfg = bootFirstBoot(t, cloud.srv.URL)
	if _, err := PairFirstTill(context.Background(), cfg, "  ", "T"); err == nil {
		t.Fatal("blank code accepted")
	}
}

func TestPairFirstTillFreshDeviceIDEveryRun(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)
	_, cfg := bootFirstBoot(t, cloud.srv.URL)
	a, err1 := PairFirstTill(context.Background(), cfg, pairCode, "T")
	b, err2 := PairFirstTill(context.Background(), cfg, pairCode, "T")
	if err1 != nil || err2 != nil || a.DeviceID == b.DeviceID {
		t.Fatalf("device ids (%v %v), want two fresh ones", err1, err2)
	}
}

func TestFirstTillPairingCommitPersistsCredential(t *testing.T) {
	cloud := newFirstTillCloud(t, 200, firstTillAnswer)
	kv, cfg := bootFirstBoot(t, cloud.srv.URL)
	p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1")
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
			p, err := PairFirstTill(context.Background(), cfg, pairCode, "Till 1")
			if err != nil {
				t.Fatalf("PairFirstTill: %v", err)
			}
			if p.Store == nil || p.Store.ShopType != tc.want {
				t.Fatalf("ShopType = %+v, want %q", p.Store, tc.want)
			}
		})
	}
}
