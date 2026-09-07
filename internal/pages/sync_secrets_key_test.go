package pages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0082 (ut-docs#1739): GET /api/sync/secrets-key — the primary hands
// its shop-scoped plugin-settings key to an enrolled replica, once.

// withFreshSecretsStore swaps the process-wide key store for an empty one
// under a temp dir (restored on cleanup) so a test can observe first-use
// generation. The pages package runs no t.Parallel tests, so swapping the
// singleton is race-free here.
func withFreshSecretsStore(t *testing.T) *secrets.KeyStore {
	t.Helper()
	prev := secrets.Default()
	ks := secrets.NewKeyStoreAt(filepath.Join(t.TempDir(), "secrets", "plugin_settings_key.bin"), nil)
	secrets.SetDefault(ks)
	t.Cleanup(func() { secrets.SetDefault(prev) })
	return ks
}

func TestSyncSecretsKeyAPI_RejectsUnauthorized(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	withFreshSecretsStore(t)
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	for _, bearer := range []string{"", "token-nobody"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/secrets-key", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q: expected 401, got %d: %s", bearer, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"key"`) {
			t.Fatalf("bearer %q: an unauthorized response must never carry a key: %s", bearer, rec.Body.String())
		}
	}
}

func TestSyncSecretsKeyAPI_ReturnsKeyGeneratingOnFirstCall(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	ctx := t.Context()
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	ks := withFreshSecretsStore(t)
	if ks.Exists() {
		t.Fatal("test premise: no key stored yet")
	}
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)

	get := func() []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/sync/secrets-key", nil)
		req.Header.Set("Authorization", "Bearer token-abc")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data  secretsKeyResponse `json:"data"`
			Error any                `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v: %s", err, rec.Body.String())
		}
		if out.Error != nil {
			t.Fatalf("error must be null: %s", rec.Body.String())
		}
		key, err := base64.StdEncoding.DecodeString(out.Data.Key)
		if err != nil {
			t.Fatalf("key is not base64: %v", err)
		}
		if len(key) != secrets.KeySize {
			t.Fatalf("key is %d bytes, want %d", len(key), secrets.KeySize)
		}
		return key
	}

	first := get()
	if !ks.Exists() {
		t.Fatal("the primary must persist the key it generated on first call")
	}
	local, err := ks.Load(ctx)
	if err != nil || !bytes.Equal(local, first) {
		t.Fatalf("served key must be the primary's own stored key: %x vs %x (%v)", first, local, err)
	}
	if second := get(); !bytes.Equal(first, second) {
		t.Fatal("second call must serve the same key, not mint another")
	}
}

// fakeSettings is a SyncSettingsReader over a map.
type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := f[key]
	return v, ok, nil
}

func TestSecretsKeyFetcher_FetchesFromPrimaryWithBearer(t *testing.T) {
	want := bytes.Repeat([]byte{0xAB}, secrets.KeySize)
	var gotAuth, gotPath string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if gotAuth != "Bearer token-abc" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": secretsKeyResponse{Key: base64.StdEncoding.EncodeToString(want)}, "error": nil})
	}))
	t.Cleanup(primary.Close)
	client := &http.Client{Timeout: 5 * time.Second}

	// Enrolled replica: one GET with the bearer, key bytes back.
	fetch := SecretsKeyFetcher(fakeSettings{"sync.primary_url": primary.URL + "/", "sync.bearer": "token-abc"}, client)
	got, err := fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("fetched key = %x, want %x", got, want)
	}
	if gotPath != "/api/sync/secrets-key" {
		t.Fatalf("fetched path = %q", gotPath)
	}

	// Rejected bearer surfaces as an error, never as a key.
	got, err = SecretsKeyFetcher(fakeSettings{"sync.primary_url": primary.URL, "sync.bearer": "wrong"}, client)(context.Background())
	if err == nil || got != nil {
		t.Fatalf("rejected bearer: want error and no key, got %x %v", got, err)
	}

	// Unreachable primary: error (the KeyStore's retry floor paces it).
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, err := SecretsKeyFetcher(fakeSettings{"sync.primary_url": dead.URL, "sync.bearer": "x"}, client)(context.Background()); err == nil {
		t.Fatal("unreachable primary must error")
	}

	// Not a replica (no primary configured): declines with (nil, nil) so the
	// store self-generates — the wiring passes one closure for every role.
	for _, s := range []fakeSettings{{}, {"sync.primary_url": primary.URL}, {"sync.bearer": "x"}} {
		got, err := SecretsKeyFetcher(s, client)(context.Background())
		if got != nil || err != nil {
			t.Fatalf("settings %v: want (nil, nil) decline, got %x %v", s, got, err)
		}
	}
}

// End to end across the two sides: a replica's KeyStore wired with the
// fetcher against a primary's real handler ends up holding the primary's
// key on disk and can open a value the primary sealed.
func TestSecretsKey_ReplicaOpensValueSealedByPrimary(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	primaryStore := withFreshSecretsStore(t)
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)
	primary := httptest.NewServer(mux)
	t.Cleanup(primary.Close)

	sealed, err := secrets.SealWithDefault(t.Context(), []byte(`"sk_live_shop"`))
	if err != nil {
		t.Fatal(err)
	}

	replica := secrets.NewKeyStoreAt(filepath.Join(t.TempDir(), "secrets", "plugin_settings_key.bin"),
		SecretsKeyFetcher(fakeSettings{"sync.primary_url": primary.URL, "sync.bearer": "token-abc"}, &http.Client{Timeout: 5 * time.Second}))
	rk, err := replica.Load(t.Context())
	if err != nil {
		t.Fatalf("replica Load: %v", err)
	}
	pk, _ := primaryStore.Load(t.Context())
	if !bytes.Equal(rk, pk) {
		t.Fatal("replica must hold the primary's key")
	}
	if !replica.Exists() {
		t.Fatal("replica must persist the fetched key")
	}
	pt, err := secrets.Open(rk, sealed)
	if err != nil || string(pt) != `"sk_live_shop"` {
		t.Fatalf("replica cannot open the primary's sealed value: %q %v", pt, err)
	}
}
