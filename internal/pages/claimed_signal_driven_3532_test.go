package pages

// ut-docs#3532 driven run (Tester): a real ut-cloud 402 plan_required body
// (captured from ut-cloud's /api/v1/stores/sync for a free store, before and
// after a claim through the claim-code flow) goes through the till's real
// cloudsync loop (Start → tick → planRequiredEntitlement → cacheEntitlement →
// settings KV), and /ui/cloud-auth-chip then renders from that cache. Only
// the 401 streak counter is pinned (cloudAuthChipStatus), as every chip test
// does.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// Captured verbatim from ut-cloud feat/3532-store-claimed (2026-11-09).
const (
	cloud402FreeClaimed = `{"data":{"entitlement":{"catalog_master":"till","claimed":true,"cloud_link":"periodic","expires_at":null,"plan":"local","refreshed_at":"2026-11-09T10:27:30Z","subscription_status":"none"}},"error":{"code":"plan_required","message":"cloud sync needs an active paid plan; the till keeps working offline"}}`  // fixed-date:ok refreshed_at is display-only
	cloud402FreeAnon    = `{"data":{"entitlement":{"catalog_master":"till","claimed":false,"cloud_link":"periodic","expires_at":null,"plan":"local","refreshed_at":"2026-11-09T10:27:30Z","subscription_status":"none"}},"error":{"code":"plan_required","message":"cloud sync needs an active paid plan; the till keeps working offline"}}` // fixed-date:ok refreshed_at is display-only
)

func TestClaimedSignalDriven3532(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantClaimed, wantVariant string
	}{
		{"claimed free store", cloud402FreeClaimed, "true", `data-cloud-auth="repair"`},
		{"anonymous free store", cloud402FreeAnon, "false", `data-cloud-auth="register"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeTokenRetired})

			var hits sync.Map
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Store(r.Method+" "+r.URL.Path, true)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cfg := &config.Config{}
			cfg.Marketplace.EndpointURL = srv.URL
			cfg.Marketplace.StoreID = "test-3532-store"
			cfg.Marketplace.MerchantToken = "test-3532-token"
			cfg.DBPath = "/nonexistent"

			kick := make(chan struct{}, 1)
			kick <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			cloudsync.Start(ctx, cfg, d.Db, cloudsync.Hooks{Kick: kick}, &wg)
			deadline := time.Now().Add(20 * time.Second)
			var got string
			for time.Now().Before(deadline) {
				if v, ok, _ := d.Settings.Get(t.Context(), entitlement.KeyStoreClaimed); ok {
					got = v
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			cancel()
			wg.Wait()
			var paths []string
			hits.Range(func(k, _ any) bool { paths = append(paths, k.(string)); return true })
			t.Logf("cloud saw: %v", paths)
			if got != tc.wantClaimed {
				t.Fatalf("entitlement.claimed = %q, want %q", got, tc.wantClaimed)
			}
			if p, _, _ := d.Settings.Get(t.Context(), entitlement.KeyPlan); p != "local" {
				t.Fatalf("plan = %q, want local (rest of the block cached)", p)
			}
			body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
			t.Logf("chip HTML: %s", strings.Join(strings.Fields(body), " "))
			if !strings.Contains(body, tc.wantVariant) {
				t.Fatalf("chip lacks %s: %s", tc.wantVariant, body)
			}
		})
	}
}
