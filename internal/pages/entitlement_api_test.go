package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// entitlementResponse mirrors GET /api/entitlement's envelope (ut-docs#2547,
// the read surface ADR-0060 §6's follow-up UI — ut-docs#2569 — builds on).
type entitlementResponse struct {
	Data struct {
		EffectivePlan      string          `json:"effective_plan"`
		Plan               string          `json:"plan"`
		SubscriptionStatus string          `json:"subscription_status"`
		ExpiresAt          string          `json:"expires_at"`
		LastConfirmedAt    string          `json:"last_confirmed_at"`
		GraceHours         int             `json:"grace_hours"`
		Capabilities       map[string]bool `json:"capabilities"`
	} `json:"data"`
	Error any `json:"error"`
}

func newEntitlementTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	registerEntitlementAPI(mux, dp)
	return mux, dp
}

func seedEntitlement(t *testing.T, dp *common.Deps, vals map[string]string) {
	t.Helper()
	for k, v := range vals {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
}

func getEntitlement(t *testing.T, mux *http.ServeMux, role string) entitlementResponse {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/entitlement", nil), auth.User{ID: "u1", Role: role})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/entitlement as %s = %d (%s), want 200", role, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var resp entitlementResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("error = %v, want null", resp.Error)
	}
	return resp
}

func TestEntitlementAPI_RejectsUnauthenticatedAndNonManager(t *testing.T) {
	mux, _ := newEntitlementTestMux(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/entitlement", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no session = %d, want 403", rec.Code)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/entitlement", nil), auth.User{ID: "u1", Role: "cashier"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
}

func TestEntitlementAPI_FreshActiveShop(t *testing.T) {
	mux, dp := newEntitlementTestMux(t)
	confirmed := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	seedEntitlement(t, dp, map[string]string{
		entitlement.KeyPlan:               "shop",
		entitlement.KeySubscriptionStatus: "active",
		entitlement.KeyExpiresAt:          "2026-12-31T00:00:00Z",
		entitlement.KeyLastConfirmedAt:    confirmed,
	})

	for _, role := range []string{"manager", "admin"} {
		resp := getEntitlement(t, mux, role)
		d := resp.Data
		if d.EffectivePlan != "shop" || d.Plan != "shop" || d.SubscriptionStatus != "active" ||
			d.ExpiresAt != "2026-12-31T00:00:00Z" || d.LastConfirmedAt != confirmed {
			t.Fatalf("%s: data = %+v", role, d)
		}
		if d.GraceHours != 168 {
			t.Fatalf("grace_hours = %d, want 168", d.GraceHours)
		}
		if len(d.Capabilities) != len(entitlement.Capabilities()) {
			t.Fatalf("capabilities = %v, want every capability", d.Capabilities)
		}
		if !d.Capabilities["cloud_backup"] {
			t.Fatalf("cloud_backup = false, want true on shop")
		}
		if v, ok := d.Capabilities["central_catalog"]; !ok || v {
			t.Fatalf("central_catalog = %v (present %v), want false on shop", v, ok)
		}
	}
}

func TestEntitlementAPI_StaleCacheIsLocal(t *testing.T) {
	mux, dp := newEntitlementTestMux(t)
	stale := time.Now().Add(-entitlement.Grace - time.Hour).UTC().Format(time.RFC3339)
	seedEntitlement(t, dp, map[string]string{
		entitlement.KeyPlan:               "chain",
		entitlement.KeySubscriptionStatus: "active",
		entitlement.KeyLastConfirmedAt:    stale,
	})
	d := getEntitlement(t, mux, "manager").Data
	if d.EffectivePlan != "local" {
		t.Fatalf("effective_plan = %q, want local", d.EffectivePlan)
	}
	// The cached values are still reported as-is for display.
	if d.Plan != "chain" || d.LastConfirmedAt != stale {
		t.Fatalf("cached fields = %+v", d)
	}
	if len(d.Capabilities) == 0 {
		t.Fatal("capabilities empty, want every capability listed as false")
	}
	for c, on := range d.Capabilities {
		if on {
			t.Fatalf("capability %s = true on a stale cache, want false", c)
		}
	}
}

func TestEntitlementAPI_NoCacheIsLocal(t *testing.T) {
	mux, _ := newEntitlementTestMux(t)
	d := getEntitlement(t, mux, "manager").Data
	if d.EffectivePlan != "local" || d.Plan != "" || d.SubscriptionStatus != "" || d.ExpiresAt != "" || d.LastConfirmedAt != "" {
		t.Fatalf("data = %+v, want local with empty cached fields", d)
	}
	for c, on := range d.Capabilities {
		if on {
			t.Fatalf("capability %s = true with no cache", c)
		}
	}
}
