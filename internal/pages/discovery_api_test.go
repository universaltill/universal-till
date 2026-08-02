package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
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
		{Name: "Task Runner", TillID: "till-abc123"},
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
				Name   string `json:"name"`
				TillID string `json:"till_id"`
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
	if len(out.Data.Primaries) != 1 || out.Data.Primaries[0].Name != "Task Runner" || out.Data.Primaries[0].TillID != "till-abc123" {
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
