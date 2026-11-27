package credheal

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The engine against the real enroll.Recover, a real migrated SQLite file
// (the same one a sale writes to) and a fake cloud that verifies the signed
// recover message the way ut-cloud will.

const (
	itStore  = "11111111-2222-4333-8444-555555555555"
	itDevice = "till-it-1"
	itOld    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	itNew    = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	itNonce  = "nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"
)

type itCloud struct {
	srv       *httptest.Server
	gate      chan struct{} // the challenge waits for it when non-nil
	reached   chan struct{} // closed when the challenge arrived
	once      sync.Once
	status    int // recover answer; 0 = good
	body      string
	chalCode  int
	hits      atomic.Int32
	registers atomic.Int32
	auth      atomic.Value
}

func newITCloud(t *testing.T) *itCloud {
	c := &itCloud{reached: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		c.registers.Add(1)
		w.WriteHeader(500)
	})
	mux.HandleFunc("/api/v1/stores/devices/recover/challenge", func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		c.auth.Store(r.Header.Get("Authorization"))
		c.once.Do(func() { close(c.reached) })
		if c.gate != nil {
			<-c.gate
		}
		if c.chalCode != 0 {
			w.WriteHeader(c.chalCode)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"challenge_id":"c-1","nonce":"` + itNonce + `","expires_at":"2031-01-01T00:05:00Z"}}`))
	})
	mux.HandleFunc("/api/v1/stores/devices/recover", func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		var req struct {
			StoreID, DeviceID string `json:"-"`
			S                 string `json:"store_id"`
			D                 string `json:"device_id"`
			C                 string `json:"challenge_id"`
			K                 struct {
				PublicKey string `json:"public_key"`
			} `json:"approval_key"`
			Sig string `json:"sig"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		pub, _ := base64.RawURLEncoding.DecodeString(req.K.PublicKey)
		msg, _ := approvalkey.RecoverMessage(req.S, req.D, req.C, itNonce, "2031-01-01T00:05:00Z", req.K.PublicKey)
		sig, _ := base64.RawURLEncoding.DecodeString(req.Sig)
		if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, msg, sig) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"recover_refused"}}`))
			return
		}
		if c.status != 0 {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"store_id":"` + req.S + `","device_id":"` + req.D + `","device_token":"` + itNew + `","role":"main"}}`))
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

type itTill struct {
	cfg *config.Config
	kv  *data.SettingsRepo
	db  *sql.DB
}

func newITTill(t *testing.T, c *itCloud, mutate func(*config.Config)) *itTill {
	t.Helper()
	orig := paths.DataDir()
	t.Cleanup(func() { paths.Init(orig) })
	paths.Init(t.TempDir())
	if _, err := approvalkey.Shared().LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(testsupport.MigratedDBFile(t, "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, s := range []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1', 'SKU1', 'Coffee Beans', 1000, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv1', 'itm1', NULL, 'loc1', 5000, datetime('now'))`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	kv := data.NewSettingsRepo(d.DB)
	ctx := context.Background()
	for k, v := range map[string]string{
		"marketplace.device_id": itDevice, "marketplace.store_id": itStore, "marketplace.merchant_id": itStore,
		"marketplace.token": itOld, "marketplace.public_key": strings.Repeat("cd", 32), "marketplace.device_registered": itDevice,
	} {
		if err := kv.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{StoreName: "Corner Shop", Marketplace: config.MarketplaceConfig{EndpointURL: c.srv.URL + "/api"}}
	if mutate != nil {
		mutate(cfg)
	}
	cctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	enroll.Init(cctx, cfg, kv, &wg)
	t.Cleanup(func() { cancel(); wg.Wait() })
	return &itTill{cfg: cfg, kv: kv, db: d.DB}
}

func (tl *itTill) start(t *testing.T) (*sync.WaitGroup, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	SetHooks(Hooks{})
	Start(ctx, Deps{Cfg: tl.cfg, KV: tl.kv, Pause: func(context.Context, time.Duration) {}}, &wg)
	t.Cleanup(func() { cancel(); wg.Wait() })
	return &wg, cancel
}

func (tl *itTill) identity() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"marketplace.device_id", "marketplace.store_id", "marketplace.merchant_id", "marketplace.token",
		"marketplace.public_key", "marketplace.device_registered", "marketplace.paired_at", "marketplace.enrolled_at"} {
		v, _, _ := tl.kv.Get(context.Background(), k)
		out[k] = v
	}
	return out
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func sale(t *testing.T, sqlDB *sql.DB) error {
	_, err := pos.CompleteSale(context.Background(), sqlDB, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true, CashierID: "system", ActorID: "system", AllowNegativeInventory: true,
		Lines:    []pos.SaleLineInput{{ItemID: "itm1", Name: "Coffee Beans", Qty: 1, UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc1"}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	return err
}

// AC1 + AC8: the first 401 heals the till through the real client while sales
// keep completing on the same database; the heal is held open mid-network
// until every sale has finished, so a blocked sale path would hang the test.
func TestFirstUnauthorizedHealsWhileSalesComplete(t *testing.T) {
	c := newITCloud(t)
	c.gate = make(chan struct{})
	tl := newITTill(t, c, nil)
	var healedHook atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	SetHooks(Hooks{OnHealed: func() { healedHook.Add(1) }})
	t.Cleanup(func() { SetHooks(Hooks{}); cancel(); wg.Wait() })
	Start(ctx, Deps{Cfg: tl.cfg, KV: tl.kv, Pause: func(context.Context, time.Duration) {}}, &wg)

	Report(Signal{Source: "checkin", Code: "unauthorized", FromCheckin: true})
	if st := Current(); st.State != StateHealing || st.Reason != ReasonReconnecting {
		t.Fatalf("chip data right after the first 401 = %+v", st)
	}
	<-c.reached // the engine is now inside the network call, holding no lock a sale needs

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 25; i++ {
			if err := sale(t, tl.db); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a sale failed during the heal: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sales were blocked while the heal was in flight")
	}
	if Current().State != StateHealing {
		t.Fatal("the heal finished before the gate opened: the test proved nothing")
	}
	close(c.gate)
	waitUntil(t, "heal", func() bool { return Current().State == StateOK && healedHook.Load() == 1 })

	id := tl.identity()
	if id["marketplace.token"] != itNew || id["marketplace.device_id"] != itDevice || id["marketplace.store_id"] != itStore {
		t.Fatalf("identity after heal: device %q store %q, token changed %v", id["marketplace.device_id"], id["marketplace.store_id"], id["marketplace.token"] == itNew)
	}
	if v, _, _ := tl.kv.Get(context.Background(), data.CredHealLastHealedAtKey); v == "" {
		t.Fatal("last_healed_at not saved")
	}
	if a, _ := c.auth.Load().(string); a != "" {
		t.Fatalf("the challenge carried an Authorization header: %q", a)
	}
	if c.registers.Load() != 0 {
		t.Fatal("register was called (AC7)")
	}
}

// AC8: every refusal class, through the engine, leaves the identity keys byte
// for byte as they were; the sale path is unaffected.
func TestRefusalClassesLeaveIdentityUntouched(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		chal   int
		state  State
		reason string
	}{
		{"removed", 409, `{"error":{"code":"device_revoked"},"data":{"reason":"removed"}}`, 0, StateNeedsPerson, ReasonRemoved},
		{"replaced", 409, `{"error":{"code":"device_revoked"},"data":{"reason":"replaced","replaced_by":"Back till"}}`, 0, StateNeedsPerson, ReasonReplaced},
		{"moved", 409, `{"error":{"code":"device_revoked"},"data":{"reason":"moved","moved_to":"Other shop"}}`, 0, StateNeedsPerson, ReasonMoved},
		{"quarantined", 409, `{"error":{"code":"key_quarantined"},"data":{"usable_from":"2031-01-02T00:00:00Z"}}`, 0, StateWaiting, ReasonKeyQuarantined},
		{"conflict", 409, `{"error":{"code":"key_conflict"}}`, 0, StateNeedsPerson, ReasonCannotProve},
		{"role", 409, `{"error":{"code":"role_conflict"}}`, 0, StateNeedsPerson, ReasonRoleConflict},
		{"old cloud", 0, ``, 404, StateNeedsPerson, ReasonPairAgain},
		{"disabled", 409, `{"error":{"code":"recover_disabled"}}`, 0, StateNeedsPerson, ReasonPairAgain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newITCloud(t)
			c.status, c.body, c.chalCode = tc.status, tc.body, tc.chal
			tl := newITTill(t, c, nil)
			before := tl.identity()
			tl.start(t)
			Report(Signal{Source: "checkin", Code: "unauthorized", FromCheckin: true})
			waitUntil(t, "the final state", func() bool {
				st := Current()
				return st.State == tc.state && st.Reason == tc.reason
			})
			after := tl.identity()
			for k, v := range before {
				if after[k] != v {
					t.Fatalf("identity key %s changed", k)
				}
			}
			if err := sale(t, tl.db); err != nil {
				t.Fatalf("sale after a refusal: %v", err)
			}
			if c.registers.Load() != 0 {
				t.Fatal("register was called")
			}
		})
	}
}

// AC6: the stop switch and an env-pinned token make no cloud call at all and
// fall back to today's behaviour.
func TestStopSwitchAndEnvPinMakeNoCall(t *testing.T) {
	for name, setup := range map[string]struct {
		mutate func(*config.Config)
		stop   bool
	}{
		"stop switch": {stop: true},
		"env pin":     {mutate: func(c *config.Config) { c.Marketplace.MerchantToken = "env-pinned-token-0001" }},
	} {
		t.Run(name, func(t *testing.T) {
			c := newITCloud(t)
			tl := newITTill(t, c, setup.mutate)
			if setup.stop {
				if err := tl.kv.Set(context.Background(), enroll.KeyStopped, "true"); err != nil {
					t.Fatal(err)
				}
			}
			tl.start(t)
			Report(Signal{Source: "checkin", Code: "unauthorized", FromCheckin: true})
			waitUntil(t, "pair_again", func() bool {
				st := Current()
				return st.State == StateNeedsPerson && st.Reason == ReasonPairAgain
			})
			if c.hits.Load() != 0 {
				t.Fatalf("the cloud was called %d times", c.hits.Load())
			}
			if Current().ShowsChip() {
				t.Fatal("pair_again must leave today's chips in charge")
			}
		})
	}
}

// A saved state is picked up by the next process: a till that was waiting
// resumes (here: the date already passed, so it heals at start-up).
func TestStartupResumesASavedWait(t *testing.T) {
	c := newITCloud(t)
	tl := newITTill(t, c, nil)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	for k, v := range map[string]string{
		data.CredHealStateKey: "waiting", data.CredHealReasonKey: ReasonKeyQuarantined,
		data.CredHealSinceKey: past, data.CredHealUntilKey: past, data.CredHealNextAttemptAtKey: past,
	} {
		_ = tl.kv.Set(ctx, k, v)
	}
	tl.start(t)
	waitUntil(t, "the resumed heal", func() bool { return Current().State == StateOK && tl.identity()["marketplace.token"] == itNew })
}
