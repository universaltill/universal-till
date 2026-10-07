package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
)

// ut-docs#2774 in-process: the replica's main till moved and mDNS finds
// nothing (client isolation). The cloud — answering the real lookup client
// over HTTP — says where the main till is; the replica challenges it through
// the main till's REAL /api/sync/primary-proof handler and re-points
// sync.primary_url only once that proof checks out. A cloud answer pointing
// anywhere else changes nothing.
func TestReplicaRecoversViaCloudLookupThroughTheRealProof(t *testing.T) {
	chdirRoot(t)
	primary := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	tillID, err := data.NewTillsRepo(primary.Db).InsertTill(ctx, "Back office", hashBearer("token-abc"), data.TillRoleAdditional)
	if err != nil {
		t.Fatal(err)
	}
	primaryID, err := discovery.TillID(ctx, data.NewSettingsRepo(primary.Db))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSyncAdmin(mux, primary)
	registerPrimaryProof(mux, primary)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// An impostor at another address: answers the proof path, but cannot
	// hold this replica's pairing.
	impostor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": discovery.ProofResponse{
			PrimaryTillID: primaryID, Proof: "00",
		}})
	}))
	t.Cleanup(impostor.Close)

	var cloudAddr atomic.Value
	var cloudHits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != discovery.CloudLookupPath || r.Header.Get("Authorization") != "Bearer replica-device-token" ||
			r.URL.Query().Get("store_id") != "store-1" {
			http.NotFound(w, r)
			return
		}
		cloudHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"lan_address": cloudAddr.Load()}})
	}))
	t.Cleanup(cloud.Close)
	host := func(raw string) string {
		u, _ := url.Parse(raw)
		return u.Host
	}

	const dead = "http://127.0.0.1:1"
	replica := newPullTestReplica(t, dead)
	for k, v := range map[string]string{
		"sync.till_id":                    tillID,
		"sync.last_contact_at":            time.Now().Add(-9 * 24 * time.Hour).UTC().Format(time.RFC3339),
		discovery.PrimaryTillIDSettingKey: primaryID,
	} {
		if err := replica.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	noLAN := func(context.Context, time.Duration) ([]discovery.Candidate, error) { return nil, nil }
	newWatch := func() {
		replica.PrimaryWatch = discovery.NewPrimaryWatch(replica.Settings, noLAN)
		replica.PrimaryWatch.SetCloudLookup(discovery.NewCloudLookup(func() (string, string, string) {
			return cloud.URL, "store-1", "replica-device-token"
		}))
	}
	client := &http.Client{Timeout: 5 * time.Second}

	// 1. The cloud points at the impostor: proof refused, nothing moves.
	cloudAddr.Store(host(impostor.URL))
	newWatch()
	syncPullTick(ctx, replica, client, func(context.Context) {})
	if cloudHits.Load() != 1 {
		t.Fatalf("cloud lookups = %d, want 1", cloudHits.Load())
	}
	if got, _, _ := replica.Settings.Get(ctx, "sync.primary_url"); got != dead {
		t.Fatalf("primary_url = %q after a cloud answer that failed the proof, want unchanged", got)
	}

	// 2. The cloud points at the real main till: proven, re-linked.
	cloudAddr.Store(host(srv.URL))
	newWatch() // stands in for MinBrowseInterval having passed
	syncPullTick(ctx, replica, client, func(context.Context) {})
	if got, _, _ := replica.Settings.Get(ctx, "sync.primary_url"); got != srv.URL {
		t.Fatalf("primary_url = %q, want the cloud-reported, proven %q", got, srv.URL)
	}
	if got, _, _ := replica.Settings.Get(ctx, "sync.bearer"); got != "token-abc" {
		t.Fatalf("bearer changed to %q", got)
	}
}
