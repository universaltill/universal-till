package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newDiscoveryAPITestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		// AuthSvc unset here is harmless today (every test in this file
		// sends no session, so canPerform returns at auth.FromContext
		// before ever touching AuthSvc) -- but the next test added here
		// that DOES send a real session would nil-deref, same latent gap
		// ut-docs#707 review caught and fixed in newPairingAPITestDeps.
		AuthSvc: auth.NewService(db),
	}
}

// stubBrowse lets tests swap in a canned Browse result instead of waiting
// out a real (bounded but multi-second) LAN scan on every test run.
func stubBrowse(t *testing.T, candidates []discovery.Candidate, err error) {
	t.Helper()
	orig := discoveryBrowse
	discoveryBrowse = func(ctx context.Context, timeout time.Duration) ([]discovery.Candidate, error) {
		return candidates, err
	}
	t.Cleanup(func() { discoveryBrowse = orig })
}

func TestDiscoverPrimariesAPI_RejectsNonManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDiscoverPrimariesAPI_ReturnsCandidatesFound(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)
	stubBrowse(t, []discovery.Candidate{
		{Name: "Task Runner", TillID: "till-abc123", BaseURL: "http://192.168.1.50:8080"},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Primaries []struct {
				Name    string `json:"name"`
				TillID  string `json:"till_id"`
				BaseURL string `json:"base_url"`
			} `json:"primaries"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("expected error: null, got %v", out.Error)
	}
	if len(out.Data.Primaries) != 1 || out.Data.Primaries[0].Name != "Task Runner" ||
		out.Data.Primaries[0].TillID != "till-abc123" || out.Data.Primaries[0].BaseURL != "http://192.168.1.50:8080" {
		t.Fatalf("unexpected primaries payload: %+v", out.Data.Primaries)
	}
}

func TestDiscoverPrimariesAPI_ReturnsEmptyArrayNotNullWhenNoneFound(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)
	stubBrowse(t, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// A literal "null" body would break an HTMX/JS caller iterating the
	// list without a nil check — Browse's own nil-safety (browse.go
	// initializes candidates as make([]Candidate, 0)) plus the handler
	// must both hold for this to stay true end to end.
	body := rec.Body.String()
	if !strings.Contains(body, `"primaries":[]`) {
		t.Fatalf("expected an empty JSON array for no candidates, got body: %s", body)
	}
}

func TestDiscoverPrimariesAPI_SurfacesBrowseErrorAs500(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)
	stubBrowse(t, nil, context.DeadlineExceeded)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when Browse fails, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDiscoverPrimariesAPI_ExcludesOwnAdvertisement — ut-docs#1261: a till
// that is currently primary/standalone advertises itself over mDNS (see
// discovery.Advertiser), so its own "join an existing shop" scan can see
// its own advertisement come back as a "candidate primary." Joining
// yourself makes no sense and every such pairing attempt fails, so the
// handler must drop any candidate whose till_id matches this till's own
// (the same stable identity discovery.TillID/RoleCheckFromSettings already
// use elsewhere for pairing verification codes) before returning results.
func TestDiscoverPrimariesAPI_ExcludesOwnAdvertisement(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)

	myID, err := discovery.TillID(t.Context(), data.NewSettingsRepo(dp.Db))
	if err != nil {
		t.Fatalf("seed own till id: %v", err)
	}
	stubBrowse(t, []discovery.Candidate{
		{Name: "My Store", TillID: myID, BaseURL: "http://127.0.0.1:37241"},
		{Name: "Task Runner", TillID: "till-other-1", BaseURL: "http://192.168.1.50:8080"},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Primaries []struct {
				TillID string `json:"till_id"`
			} `json:"primaries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Data.Primaries) != 1 || out.Data.Primaries[0].TillID != "till-other-1" {
		t.Fatalf("expected only the other till's candidate, got: %+v", out.Data.Primaries)
	}
}

// TestDiscoverPrimariesAPI_AloneOnLANSeesEmptyArrayNotItself pins the
// literal ut-docs#1261 acceptance-criteria scenario: a standalone till,
// alone on the LAN, scans and sees only its own advertisement (nothing
// else responds) — the response must be an empty array, same
// "primaries":[] contract as
// TestDiscoverPrimariesAPI_ReturnsEmptyArrayNotNullWhenNoneFound, not a
// literal JSON null and not a self-referential result.
func TestDiscoverPrimariesAPI_AloneOnLANSeesEmptyArrayNotItself(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)

	myID, err := discovery.TillID(t.Context(), data.NewSettingsRepo(dp.Db))
	if err != nil {
		t.Fatalf("seed own till id: %v", err)
	}
	stubBrowse(t, []discovery.Candidate{
		{Name: "My Store", TillID: myID, BaseURL: "http://192.168.1.163:8080"},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"primaries":[]`) {
		t.Fatalf("expected an empty JSON array, not null and not self, got body: %s", body)
	}
}

// TestDiscoverPrimariesAPI_FailsOpenWhenOwnTillIDLookupErrors — the
// filtering is best-effort: if looking up this till's own id fails, the
// handler must not 500 an otherwise-successful scan (this endpoint has
// nothing better to fall back to, and 500ing would break "Join an
// existing shop" wholesale over a transient settings read), it just skips
// self-filtering for that one response.
func TestDiscoverPrimariesAPI_FailsOpenWhenOwnTillIDLookupErrors(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)
	stubBrowse(t, []discovery.Candidate{
		{Name: "Task Runner", TillID: "till-other-1", BaseURL: "http://192.168.1.50:8080"},
	}, nil)

	origTillID := discoveryTillID
	discoveryTillID = func(ctx context.Context, settings *data.SettingsRepo) (string, error) {
		return "", errors.New("settings db unavailable")
	}
	t.Cleanup(func() { discoveryTillID = origTillID })

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (fail open, not 500) when own-id lookup errors, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"till_id":"till-other-1"`) {
		t.Fatalf("expected the unfiltered candidate list on a lookup error, got body: %s", body)
	}
}

// TestDiscoverPrimariesAPI_NeverLeaksRawErrorText — ut-docs#538, and the
// same standing rule #303 already fixed elsewhere: a raw driver/network
// error must never reach the operator-facing response body. The handler
// must log the real error server-side and respond with a generic message.
func TestDiscoverPrimariesAPI_NeverLeaksRawErrorText(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerDiscoveryAPI(mux, dp)
	rawErr := errors.New("write udp6 [::]:57143->[ff02::fb]:5353: sendto: no route to host")
	stubBrowse(t, nil, rawErr)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/discover-primaries", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when Browse fails, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "sendto") || strings.Contains(body, "udp6") {
		t.Fatalf("response body leaks the raw driver error: %s", body)
	}
}
