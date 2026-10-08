package cloudsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2769, ADR-0116 D4, end to end through pushSync: a legacy-token
// sync answered with this till's own device_id + device_token makes the
// NEXT request carry the rotated credential as its bearer, with no restart;
// a credential for another device is ignored; and the rotated credential
// never leaves the till on the LAN (admin bundle) nor reaches the log.

const (
	rotLegacyToken = "legacy-shared-token-0001"
	rotDeviceToken = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	rotOwnDeviceID = "till-rotate-own"
)

type rotatingCloud struct {
	mu      sync.Mutex
	bearers []string
	// answer is the device_id/device_token pair the NEXT sync answers with
	// (once); empty = no rotation fields at all.
	answerID, answerToken string
}

func (c *rotatingCloud) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.bearers = append(c.bearers, r.Header.Get("Authorization"))
		data := map[string]any{"directives": []any{}}
		if c.answerID != "" || c.answerToken != "" {
			data["device_id"], data["device_token"] = c.answerID, c.answerToken
			c.answerID, c.answerToken = "", ""
		}
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": nil})
	})
	return mux
}

func (c *rotatingCloud) bearerAt(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.bearers) {
		return ""
	}
	return c.bearers[i]
}

// bootEnrolledTill seeds an enrolled main till holding the legacy token and
// runs the real enroll.Init against the till's own settings table, exactly
// as internal/app does. cfg.Marketplace.MerchantToken stays empty: the
// token is not pinned by the environment.
func bootEnrolledTill(t *testing.T, endpoint string) (*config.Config, *db.DB) {
	t.Helper()
	d := openMigratedDB(t, "rotate.db")
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()
	for k, v := range map[string]string{
		"marketplace.device_id":         rotOwnDeviceID,
		"marketplace.store_id":          "store-1",
		"marketplace.merchant_id":       "store-1",
		"marketplace.token":             rotLegacyToken,
		"marketplace.public_key":        strings.Repeat("cd", 32),
		"marketplace.device_registered": rotOwnDeviceID,
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	cfg := &config.Config{DBPath: "/nonexistent"}
	cfg.Marketplace.EndpointURL = endpoint
	enroll.Init(ctx, cfg, settings, &sync.WaitGroup{})
	t.Cleanup(func() {
		enroll.Init(context.Background(), &config.Config{}, emptyKV{}, &sync.WaitGroup{})
	})
	return cfg, d
}

func TestPushSync_RotatedCredentialIsTheNextBearer(t *testing.T) {
	var logs bytes.Buffer
	var logMu sync.Mutex
	restore := logging.CaptureForTest(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.Write(p)
	}))
	defer restore()

	cloud := &rotatingCloud{answerID: rotOwnDeviceID, answerToken: rotDeviceToken}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{})); err != nil {
			t.Fatalf("sync %d: %v", i+1, err)
		}
	}
	if got := cloud.bearerAt(0); got != "Bearer "+rotLegacyToken {
		t.Fatalf("first sync bearer = %q, want the legacy token", got)
	}
	if got := cloud.bearerAt(1); got != "Bearer "+rotDeviceToken {
		t.Fatalf("second sync bearer = %q, want the rotated device credential", got)
	}
	if v, _, _ := settings.Get(ctx, "marketplace.token"); v != rotDeviceToken {
		t.Fatalf("marketplace.token = %q, want the rotated credential persisted", v)
	}

	// Never on the LAN: the admin bundle a replica pulls does not carry it.
	bundle, err := data.NewSyncAdminRepo(d.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump admin: %v", err)
	}
	raw, _ := json.Marshal(bundle)
	if bytes.Contains(raw, []byte(rotDeviceToken)) {
		t.Fatal("the rotated credential is in the admin bundle a replica pulls")
	}
	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(logs.String(), rotDeviceToken) {
		t.Fatalf("the rotated credential reached the log:\n%s", logs.String())
	}
}

func TestPushSync_CredentialForAnotherDeviceIsIgnored(t *testing.T) {
	cloud := &rotatingCloud{answerID: "till-someone-else", answerToken: rotDeviceToken}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{})); err != nil {
			t.Fatalf("sync %d: %v", i+1, err)
		}
	}
	if got := cloud.bearerAt(1); got != "Bearer "+rotLegacyToken {
		t.Fatalf("second sync bearer = %q, want the legacy token still", got)
	}
	if v, _, _ := settings.Get(ctx, "marketplace.token"); v != rotLegacyToken {
		t.Fatalf("marketplace.token = %q, want the legacy token untouched", v)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// emptyKV resets enroll's package state after a test (nothing stored, and
// writes are dropped).
type emptyKV struct{}

func (emptyKV) Get(context.Context, string) (string, bool, error)            { return "", false, nil }
func (emptyKV) Set(context.Context, string, string) error                    { return nil }
func (emptyKV) GetOrCreate(_ context.Context, _, def string) (string, error) { return def, nil }

// ut-docs#3651: an unpaid till still on the legacy shared token gets its
// ADR-0116 D4 credential inside the sync's 402 plan_required too —
// otherwise the legacy token's retirement strands it on 401 token_retired.
// pushSync keeps it (same rules as on a 200) and still fails the tick with
// the 402 exactly as before; the token never reaches the error text or the
// log.

// planRequiredSyncCloud answers every /v1/stores/sync with a 402
// plan_required carrying the entitlement block and, when set, the rotation
// fields beside it.
func planRequiredSyncCloud(t *testing.T, deviceID, deviceToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := map[string]any{"entitlement": json.RawMessage(planRequiredBlock)}
		if deviceID != "" || deviceToken != "" {
			d["device_id"], d["device_token"] = deviceID, deviceToken
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  d,
			"error": map[string]string{"code": "plan_required", "message": "cloud sync needs an active plan"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// assertSync402 checks pushSync failed exactly as a 402 always has: a
// plan_required statusError with its Retry-After, the entitlement block
// cached, and the unchanged error text.
func assertSync402(t *testing.T, settings *data.SettingsRepo, err error) {
	t.Helper()
	se, ok := planRequired(err)
	if !ok {
		t.Fatalf("pushSync err = %v, want the 402 plan_required", err)
	}
	if se.RetryAfter != time.Hour {
		t.Fatalf("RetryAfter = %v, want 1h", se.RetryAfter)
	}
	if want := "cloudsync: /v1/stores/sync returned 402"; err.Error() != want {
		t.Fatalf("Error() = %q, want the unchanged %q", err.Error(), want)
	}
	if v, ok, _ := settings.Get(context.Background(), entitlement.KeyPlan); !ok || v != "local" {
		t.Fatalf("%s = %q (set=%v), want the 402's block cached", entitlement.KeyPlan, v, ok)
	}
}

func TestPushSync402_RotatedCredentialIsKept(t *testing.T) {
	var logs bytes.Buffer
	var logMu sync.Mutex
	restore := logging.CaptureForTest(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.Write(p)
	}))
	defer restore()

	srv := planRequiredSyncCloud(t, rotOwnDeviceID, rotDeviceToken)
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()

	_, err := pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{}))
	assertSync402(t, settings, err)
	if v, _, _ := settings.Get(ctx, "marketplace.token"); v != rotDeviceToken {
		t.Fatalf("marketplace.token = %q, want the 402's rotated credential persisted", v)
	}
	if got := enroll.Effective(cfg).Marketplace.MerchantToken; got != rotDeviceToken {
		t.Fatalf("effective token = %q, want the rotated credential for the next request", got)
	}
	if strings.Contains(err.Error(), rotDeviceToken) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), rotDeviceToken) {
		t.Fatal("the rotated credential is in the 402 error")
	}
	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(logs.String(), rotDeviceToken) {
		t.Fatalf("the rotated credential reached the log:\n%s", logs.String())
	}
}

func TestPushSync402_CredentialForAnotherDeviceIsIgnored(t *testing.T) {
	srv := planRequiredSyncCloud(t, "till-someone-else", rotDeviceToken)
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()

	_, err := pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{}))
	assertSync402(t, settings, err)
	if v, _, _ := settings.Get(ctx, "marketplace.token"); v != rotLegacyToken {
		t.Fatalf("marketplace.token = %q, want the legacy token untouched", v)
	}
}

func TestPushSync402_WithoutRotationFieldsIsUnchanged(t *testing.T) {
	srv := planRequiredSyncCloud(t, "", "")
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()

	_, err := pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{}))
	assertSync402(t, settings, err)
	if v, _, _ := settings.Get(ctx, "marketplace.token"); v != rotLegacyToken {
		t.Fatalf("marketplace.token = %q, want the legacy token untouched", v)
	}
}

// Only the sync applies it: post (every other endpoint's path) hands back
// no body on a 402, so a credential in another endpoint's refusal can never
// be kept, and the error carries none of it.
func TestPost402_OtherEndpointNeverAppliesCredential(t *testing.T) {
	srv := planRequiredSyncCloud(t, rotOwnDeviceID, rotDeviceToken)
	cfg, d := bootEnrolledTill(t, srv.URL)
	settings := data.NewSettingsRepo(d.DB)
	ctx := context.Background()

	body, err := post(ctx, cfg, salesAggregatePath, []byte("{}"))
	if _, ok := planRequired(err); !ok || body != nil {
		t.Fatalf("post = (%q, %v), want (nil, a 402)", body, err)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), rotDeviceToken) {
		t.Fatal("the rotated credential is in another endpoint's 402 error")
	}
	if v, _, _ := settings.Get(ctx, "marketplace.token"); v != rotLegacyToken {
		t.Fatalf("marketplace.token = %q, want the legacy token untouched", v)
	}
}
