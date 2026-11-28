package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#6583 (S7a, till side): the main till uploads its queued neutral
// availability.set / service.set signals with the check-in cycle. The cloud
// endpoint is a PROPOSED contract (till-a-notes.md); until the cloud answers
// it, rows stay pending and the till backs off, never blocking anything.

type signalsCloud struct {
	mu       sync.Mutex
	status   int
	reassert bool
	got      []map[string]any
	auth     string
}

func (c *signalsCloud) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.URL.Path != "/v1/stores/channel/delivery-signals" {
			http.NotFound(w, r)
			return
		}
		c.auth = r.Header.Get("Authorization")
		if c.status != 0 && c.status != 200 {
			w.WriteHeader(c.status)
			return
		}
		var body struct {
			StoreID string           `json:"store_id"`
			Signals []map[string]any `json:"signals"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.got = append(c.got, body.Signals...)
		var ids []string
		for _, s := range body.Signals {
			ids = append(ids, s["signal_id"].(string))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": ids, "reassert": c.reassert}, "error": nil})
	})
}

func resetSignalsState() {
	deliverySignalsMu.Lock()
	deliverySignalsHoldUntil = time.Time{}
	deliverySignalsMu.Unlock()
}

func TestUploadDeliverySignals_SendsAndMarksSent(t *testing.T) {
	resetSignalsState()
	d := openMigratedDB(t, "sig1.db").DB
	testsupport.SeedTaxCode(t, d, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, d, testsupport.ItemSeed{ID: "itm1", SKU: "A-1", Name: "Soup", BasePrice: 500, TaxCodeID: "tc1", IsActive: true})
	repo := data.NewDeliveryAvailabilityRepo(d)
	now := time.Now().UTC()
	if err := repo.SetItemUnavailable(context.Background(), "itm1", time.Time{}, "", now); err != nil {
		t.Fatal(err)
	}
	cloud := &signalsCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	uploadDeliverySignals(context.Background(), testCfg(srv.URL), d, now)
	if len(cloud.got) != 1 || cloud.got[0]["kind"] != "availability.set" || cloud.got[0]["payload"] == nil || cloud.auth != "Bearer tok-1" {
		t.Fatalf("cloud got %v auth=%q", cloud.got, cloud.auth)
	}
	if left, _ := repo.PendingSignals(context.Background(), 10); len(left) != 0 {
		t.Fatalf("pending after upload: %+v", left)
	}
}

func TestUploadDeliverySignals_CloudWithoutTheEndpointKeepsRowsAndBacksOff(t *testing.T) {
	resetSignalsState()
	d := openMigratedDB(t, "sig2.db").DB
	testsupport.SeedTaxCode(t, d, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, d, testsupport.ItemSeed{ID: "itm1", SKU: "A-1", Name: "Soup", BasePrice: 500, TaxCodeID: "tc1", IsActive: true})
	repo := data.NewDeliveryAvailabilityRepo(d)
	now := time.Now().UTC()
	_ = repo.SetItemUnavailable(context.Background(), "itm1", time.Time{}, "", now)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.NotFound(w, r) }))
	defer srv.Close()
	uploadDeliverySignals(context.Background(), testCfg(srv.URL), d, now)
	uploadDeliverySignals(context.Background(), testCfg(srv.URL), d, now.Add(time.Minute)) // inside the hold
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (held after a 404)", calls)
	}
	if left, _ := repo.PendingSignals(context.Background(), 10); len(left) != 1 {
		t.Fatalf("rows must be kept: %+v", left)
	}
}

func TestUploadDeliverySignals_ReassertWhenTheCloudSaysTheProviderDiffers(t *testing.T) {
	resetSignalsState()
	d := openMigratedDB(t, "sig3.db").DB
	testsupport.SeedTaxCode(t, d, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, d, testsupport.ItemSeed{ID: "itm1", SKU: "A-1", Name: "Soup", BasePrice: 500, TaxCodeID: "tc1", IsActive: true})
	repo := data.NewDeliveryAvailabilityRepo(d)
	now := time.Now().UTC()
	_ = repo.SetItemUnavailable(context.Background(), "itm1", time.Time{}, "", now)
	cloud := &signalsCloud{reassert: true}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	uploadDeliverySignals(context.Background(), testCfg(srv.URL), d, now)
	left, _ := repo.PendingSignals(context.Background(), 10)
	kinds := map[string]int{}
	for _, s := range left {
		kinds[s.Kind]++
	}
	if kinds[data.SignalAvailabilitySet] != 1 || kinds[data.SignalServiceSet] != 1 {
		t.Fatalf("a reassert must queue the whole state again (till wins): %v", kinds)
	}
}

func TestUploadDeliverySignals_GateCases(t *testing.T) {
	resetSignalsState()
	d := openMigratedDB(t, "sig4.db").DB
	testsupport.SeedTaxCode(t, d, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, d, testsupport.ItemSeed{ID: "itm1", SKU: "A-1", Name: "Soup", BasePrice: 500, TaxCodeID: "tc1", IsActive: true})
	repo := data.NewDeliveryAvailabilityRepo(d)
	now := time.Now().UTC()
	_ = repo.SetItemUnavailable(context.Background(), "itm1", time.Time{}, "", now)
	cloud := &signalsCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	// a satellite never uploads
	if _, err := d.Exec(`INSERT INTO settings(key,value) VALUES('sync.primary_url','http://main.lan')`); err != nil {
		t.Fatal(err)
	}
	uploadDeliverySignals(context.Background(), testCfg(srv.URL), d, now)
	// an unenrolled till never uploads
	if _, err := d.Exec(`DELETE FROM settings WHERE key='sync.primary_url'`); err != nil {
		t.Fatal(err)
	}
	cfg := testCfg(srv.URL)
	cfg.Marketplace.MerchantToken = ""
	uploadDeliverySignals(context.Background(), cfg, d, now)
	if len(cloud.got) != 0 {
		t.Fatalf("the cloud was called: %v", cloud.got)
	}
}
