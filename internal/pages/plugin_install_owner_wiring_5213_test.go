package pages

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/plugins"
)

// TestInstallFromMarketplace_OwnerWiring pins ut-docs#5213: the install route
// hands plugins.ClassifyInstallError the store's real ownership
// (storeHasOwner), not a constant. ClassifyInstallError itself is unit-tested
// in internal/plugins, but nothing exercised this caller, so passing a
// hard-coded true/false would have stayed green. token_retired is the code
// whose operator advice differs: an anonymous store must register a new
// device, an owned one must pair again.
func TestInstallFromMarketplace_OwnerWiring(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, tc := range []struct {
		name     string
		paidPlan bool
		key      string
	}{
		{"anonymous store", false, "plugins.install.error.register_new"},
		{"owned store", true, "plugins.install.error.pair_again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatePluginsDir(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"data":null,"error":{"code":"token_retired","message":"nope"}}`))
			}))
			t.Cleanup(srv.Close)

			pub, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("keygen: %v", err)
			}
			db := openRealSchemaPagesDB(t)
			cfg := basePluginCfg()
			cfg.Marketplace.EndpointURL = srv.URL
			cfg.Marketplace.PublicKey = hex.EncodeToString(pub)
			cfg.Marketplace.ClientID = "merchant-1"
			cfg.Marketplace.StoreID = "store-1"
			cfg.Marketplace.DeviceID = "device-1"
			d := newPluginAPIDeps(t, db, cfg)
			if tc.paidPlan {
				if err := d.Settings.Set(t.Context(), entitlement.KeyPlan, "shop"); err != nil {
					t.Fatalf("seed plan: %v", err)
				}
			}
			mux := http.NewServeMux()
			registerPluginAPI(mux, d)

			body, _ := json.Marshal(map[string]any{"listing_id": "lst-owner", "accept_terms": true})
			req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("install = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			var env struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode envelope: %v (%s)", err, rec.Body.String())
			}
			if env.Error != tc.key {
				t.Fatalf("envelope error = %q, want %q", env.Error, tc.key)
			}
			st, found, err := plugins.NewInstallStatusStore(db).Get(t.Context(), "lst-owner")
			if err != nil || !found {
				t.Fatalf("get status: found=%v err=%v", found, err)
			}
			if st.State != plugins.InstallStateFailed || st.MessageKey != tc.key {
				t.Fatalf("persisted status = %s/%q, want failed/%q", st.State, st.MessageKey, tc.key)
			}
		})
	}
}
