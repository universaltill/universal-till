package pages

// ut-docs#6327 driven run (Tester): a manager changes the default tax rate
// and basis through the real /api/settings/upsert handler; the sale engine
// is reconfigured from that change, and the real cloudsync loop (Start →
// tick → buildSyncRequest with buildCloudHooks' DeviceExtra) POSTs a device
// report whose JSON carries exactly the rate and basis the engine charges.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

func TestTaxReport6327_SettingsChangeReachesSyncPayload(t *testing.T) {
	// DeviceExtra mints the directive/approval keys under paths.Data: keep
	// them out of the repo (as newCloudSyncTestDeps does).
	origData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origData) })
	mux, _, d := newFullAuthDeps(t)
	for _, kv := range [][2]string{{"store.tax_rate", "8.1"}, {"store.tax_inclusive", "false"}} {
		if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {kv[0]}, "value": {kv[1]}}, &mgrUser); rec.Code != 204 {
			t.Fatalf("upsert %s = %d %s", kv[0], rec.Code, rec.Body.String())
		}
	}
	ec := d.Engine.Config()
	if ec.TaxRateBasisPoints != 810 || ec.TaxInclusive {
		t.Fatalf("engine charges %d bp inclusive=%v, want 810 bp exclusive", ec.TaxRateBasisPoints, ec.TaxInclusive)
	}

	var mu sync.Mutex
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stores/sync") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			if raw == nil {
				raw = b
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"directives":[]},"error":null}`))
	}))
	defer srv.Close()
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = srv.URL
	cfg.Marketplace.StoreID = "test-6327-store"
	cfg.Marketplace.MerchantToken = "test-6327-token"
	cfg.DBPath = "/nonexistent"
	hooks := buildCloudHooks(d, nil)
	kick := make(chan struct{}, 1)
	kick <- struct{}{}
	hooks.Kick = kick
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	cloudsync.Start(ctx, cfg, d.Db, hooks, &wg)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := raw != nil
		mu.Unlock()
		if got {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if raw == nil {
		t.Fatal("no /stores/sync request was sent")
	}
	var body struct {
		Devices []map[string]json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Devices) == 0 {
		t.Fatalf("decode sync body: %v %s", err, raw)
	}
	dev := body.Devices[0]
	t.Logf("device report tax fields: tax_rate_bp=%s tax_inclusive=%s", dev["tax_rate_bp"], dev["tax_inclusive"])
	if string(dev["tax_rate_bp"]) != "810" || string(dev["tax_inclusive"]) != "false" {
		t.Fatalf("payload tax_rate_bp=%s tax_inclusive=%s, want 810/false (what the engine charges)", dev["tax_rate_bp"], dev["tax_inclusive"])
	}
}
