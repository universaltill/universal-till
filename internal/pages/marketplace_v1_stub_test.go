package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The marketplace v1 stub is a dev-only echo server. These tests cover the
// gating (DevMode / UT_ENABLE_MARKETPLACE_STUB), every route's happy path and
// validation, and the decodeJSONBody / writeJSONResponse helpers.

func marketplaceStubMux(t *testing.T, cfg *config.Config) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerMarketplaceV1Stub(mux, &common.Deps{Cfg: cfg})
	return mux
}

func TestMarketplaceStub_DisabledWhenNotDevAndEnvUnset(t *testing.T) {
	mux := marketplaceStubMux(t, &config.Config{DevMode: false})
	req := httptest.NewRequest(http.MethodPost, "/v1/install/intents", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when stub disabled, got %d", rec.Code)
	}
}

func TestMarketplaceStub_NilDepsAndNilCfgAreNoOps(t *testing.T) {
	// Neither should register any route (and must not panic).
	mux := http.NewServeMux()
	registerMarketplaceV1Stub(mux, nil)
	registerMarketplaceV1Stub(mux, &common.Deps{Cfg: nil})
	req := httptest.NewRequest(http.MethodPost, "/v1/install/intents", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with no registration, got %d", rec.Code)
	}
}

func TestMarketplaceStub_EnabledViaEnv(t *testing.T) {
	t.Setenv("UT_ENABLE_MARKETPLACE_STUB", "true")
	mux := marketplaceStubMux(t, &config.Config{DevMode: false})
	body := `{"plugin_id":"p1","version":"1.0.0","merchant_id":"m1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/install/intents", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 when enabled via env, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestMarketplaceStub_InstallIntents(t *testing.T) {
	mux := marketplaceStubMux(t, &config.Config{DevMode: true})

	// Happy path echoes the payload back.
	body := `{"plugin_id":"p1","version":"1.0.0","merchant_id":"m1","device_id":"d1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/install/intents", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"plugin_id":"p1"`) {
		t.Fatalf("expected echoed plugin_id, got %s", rec.Body.String())
	}

	// Wrong method -> 405.
	req = httptest.NewRequest(http.MethodGet, "/v1/install/intents", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on GET, got %d", rec.Code)
	}

	// Missing required fields -> 400.
	req = httptest.NewRequest(http.MethodPost, "/v1/install/intents", strings.NewReader(`{"plugin_id":"p1"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing fields, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "required") {
		t.Fatalf("expected validation error, got %s", rec.Body.String())
	}

	// Unknown field is rejected (DisallowUnknownFields).
	req = httptest.NewRequest(http.MethodPost, "/v1/install/intents", strings.NewReader(`{"plugin_id":"p1","version":"1","merchant_id":"m","bogus":true}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on unknown field, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestMarketplaceStub_InstallStatus(t *testing.T) {
	mux := marketplaceStubMux(t, &config.Config{DevMode: true})

	req := httptest.NewRequest(http.MethodGet, "/v1/install/status?plugin_id=p1&merchant_id=m1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("expected pending status, got %s", rec.Body.String())
	}

	// Wrong method -> 405.
	req = httptest.NewRequest(http.MethodPost, "/v1/install/status", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on POST, got %d", rec.Code)
	}

	// Missing query params -> 400.
	req = httptest.NewRequest(http.MethodGet, "/v1/install/status?plugin_id=p1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing merchant_id, got %d", rec.Code)
	}
}

func TestMarketplaceStub_BundlesExportImport(t *testing.T) {
	mux := marketplaceStubMux(t, &config.Config{DevMode: true})
	valid := `{"plugin_id":"p1","version":"1.0.0","checksum":"abc","merchant_id":"m1"}`

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/install/bundles/export", http.StatusOK},
		{"/v1/install/bundles/import", http.StatusAccepted},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(valid))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d body %s", tc.path, tc.want, rec.Code, rec.Body.String())
		}

		// Missing checksum -> 400.
		req = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"plugin_id":"p1","version":"1","merchant_id":"m"}`))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400 on missing checksum, got %d", tc.path, rec.Code)
		}

		// Wrong method -> 405.
		req = httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: expected 405 on GET, got %d", tc.path, rec.Code)
		}
	}
}

func TestMarketplaceStub_Telemetry(t *testing.T) {
	mux := marketplaceStubMux(t, &config.Config{DevMode: true})

	valid := `{"plugin_id":"p1","version":"1.0.0","merchant_id":"m1","state":"installed","timestamp":"2026-07-29T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/telemetry/plugins", strings.NewReader(valid))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
		t.Fatalf("expected accepted status, got %s", rec.Body.String())
	}

	// Missing state -> 400.
	req = httptest.NewRequest(http.MethodPost, "/v1/telemetry/plugins", strings.NewReader(`{"plugin_id":"p1","version":"1","merchant_id":"m","timestamp":"t"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing state, got %d", rec.Code)
	}

	// Wrong method -> 405.
	req = httptest.NewRequest(http.MethodGet, "/v1/telemetry/plugins", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on GET, got %d", rec.Code)
	}
}

func TestDecodeJSONBody_NilBody(t *testing.T) {
	// httptest.NewRequest substitutes http.NoBody for a nil reader, so build
	// the request by hand to hit the r.Body == nil branch.
	req := &http.Request{Body: nil}
	var dst installIntentPayload
	if err := decodeJSONBody(req, &dst); err == nil {
		t.Fatalf("expected an error for a nil request body")
	}
}

func TestDecodeJSONBody_MalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{not-json`))
	var dst installIntentPayload
	if err := decodeJSONBody(req, &dst); err == nil {
		t.Fatalf("expected an error for malformed JSON")
	}
}
