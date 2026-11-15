package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#4791: the plugin store's lazy registration asks for the cloud terms.

func TestTermsGateLazyRegistration(t *testing.T) {
	ctx := t.Context()
	t.Run("unregistered not ticked refuses and records nothing", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		_ = newTermsCloud(t, d)
		refuse, err := termsGateLazyRegistration(ctx, d, false, "en")
		if err != nil || !refuse {
			t.Fatalf("refuse=%v err=%v, want refuse", refuse, err)
		}
		if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
			t.Fatalf("terms_version = %q, want nothing recorded", v)
		}
	})
	t.Run("ticked records and allows", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		_ = newTermsCloud(t, d)
		refuse, err := termsGateLazyRegistration(ctx, d, true, "en")
		if err != nil || refuse {
			t.Fatalf("refuse=%v err=%v, want allowed", refuse, err)
		}
		if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
			t.Fatalf("terms_version = %q, want %q", v, enroll.TermsVersion)
		}
	})
	t.Run("already accepted allows without re-recording", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		_ = newTermsCloud(t, d)
		if err := enroll.RecordTermsAcceptance(ctx, d.Settings, "de"); err != nil {
			t.Fatal(err)
		}
		refuse, err := termsGateLazyRegistration(ctx, d, true, "en")
		if err != nil || refuse {
			t.Fatalf("refuse=%v err=%v, want allowed", refuse, err)
		}
		if v := settingVal(t, d, "marketplace.terms_locale"); v != "de" {
			t.Fatalf("terms_locale = %q, want the original de kept", v)
		}
	})
	t.Run("registered allows and records nothing", func(t *testing.T) {
		_, _, d := newFullAuthDeps(t)
		_ = newRegisteredTill(t, d)
		enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
		if !enroll.CurrentStatus().Registered {
			t.Fatal("fake till did not register; the registered case must run")
		}
		refuse, err := termsGateLazyRegistration(ctx, d, false, "en")
		if err != nil || refuse {
			t.Fatalf("refuse=%v err=%v, want allowed", refuse, err)
		}
		if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
			t.Fatalf("terms_version = %q, want nothing recorded", v)
		}
	})
}

func TestStoreDownload_UnregisteredWithoutTickIs428AndDoesNotRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}}, nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("download = %d, want 428 (%s)", rec.Code, rec.Body.String())
	}
	if ok, msg := storeRespOf(t, rec); ok || msg != "settings.enrol.terms_required" {
		t.Fatalf("envelope ok=%v msg=%q, want the terms_required key", ok, msg)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("cloud hit %d times; nothing may be registered or downloaded", n)
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
		t.Fatalf("terms_version = %q, want nothing recorded", v)
	}
}

func TestStoreDownload_TickRecordsAcceptanceAndProceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})

	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}, "accept_terms": {"on"}}, nil)
	if rec.Code != http.StatusBadGateway { // gate passed; the empty marketplace fails the download
		t.Fatalf("ticked download = %d, want 502 past the gate (%s)", rec.Code, rec.Body.String())
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
		t.Fatalf("terms_version = %q, want %q", v, enroll.TermsVersion)
	}
}

func TestInstallFromMarketplace_UnregisteredWithoutAcceptTerms428(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{EndpointURL: srv.URL})
	registerPluginAPI(mux, d)

	body, _ := json.Marshal(map[string]any{"listing_id": "lst-gate"})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPreconditionRequired || !strings.Contains(rec.Body.String(), `"settings.enrol.terms_required"`) {
		t.Fatalf("install = %d %s, want 428 terms_required", rec.Code, rec.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("cloud hit %d times", hits.Load())
	}
	if _, ok, _ := plugins.NewInstallStatusStore(d.Db).Get(t.Context(), "lst-gate"); ok {
		t.Fatal("a refused install left a status record behind")
	}

	// accept_terms true passes the gate and records the acceptance.
	body, _ = json.Marshal(map[string]any{"listing_id": "lst-gate", "accept_terms": true})
	req = httptest.NewRequest(http.MethodPost, "/api/plugins/install-from-marketplace", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusPreconditionRequired {
		t.Fatalf("accepted install still refused: %s", rec.Body.String())
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
		t.Fatalf("terms_version = %q", v)
	}
}

func TestStoreDetail_ShowsTermsBoxOnlyWhenGated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	get := func(mux *http.ServeMux) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store/lst-a", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET detail = %d: %.500s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	sum := marketplace.PluginSummary{ID: "lst-a", Name: "Alpha", Version: "1.0", CanonicalType: "theme"}

	mux, d := newStoreDetailMuxDeps(t, sum)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	body := get(mux)
	for _, want := range []string{`name="accept_terms"`, `id="store-accept-terms"`, "'settings.enrol.terms_required':"} {
		if !strings.Contains(body, want) {
			t.Errorf("unregistered, unaccepted detail page missing %q", want)
		}
	}

	if err := enroll.RecordTermsAcceptance(t.Context(), d.Settings, "en"); err != nil {
		t.Fatal(err)
	}
	if body := get(mux); strings.Contains(body, `name="accept_terms"`) {
		t.Error("terms already accepted: the box must not render")
	}
}

func TestStoreDetail_HidesTermsBoxOnRegisteredOrReplicaTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	sum := marketplace.PluginSummary{ID: "lst-a", Name: "Alpha", Version: "1.0", CanonicalType: "theme"}
	get := func(mux *http.ServeMux) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store/lst-a", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET detail = %d: %.500s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	t.Run("registered", func(t *testing.T) {
		mux, d := newStoreDetailMuxDeps(t, sum)
		_ = newRegisteredTill(t, d)
		enroll.EnsureRegistered(t.Context(), d.Cfg, d.Settings)
		if !enroll.CurrentStatus().Registered {
			t.Fatal("fake till did not register")
		}
		if body := get(mux); strings.Contains(body, `name="accept_terms"`) {
			t.Error("registered till: the box must not render")
		}
	})
	t.Run("replica", func(t *testing.T) {
		mux, d := newStoreDetailMuxDeps(t, sum)
		enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
		t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
		if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
			t.Fatal(err)
		}
		if body := get(mux); strings.Contains(body, `name="accept_terms"`) {
			t.Error("till following a main till: the box must not render")
		}
	})
}

func TestStoreDownload_TickSendsTermsVersionOnRegister(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{})
	c := newTermsCloud(t, d)
	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}, "accept_terms": {"on"}}, nil)
	if rec.Code == http.StatusPreconditionRequired {
		t.Fatalf("ticked download refused: %s", rec.Body.String())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body == nil || c.body["terms_version"] != enroll.TermsVersion {
		t.Fatalf("register body = %v, want terms_version %q", c.body, enroll.TermsVersion)
	}
}

func TestStoreDownload_RegisteredTillNeedsNoTick(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d := newStoreAPIMux(t, config.MarketplaceConfig{})
	_ = newRegisteredTill(t, d)
	enroll.EnsureRegistered(t.Context(), d.Cfg, d.Settings)
	if !enroll.CurrentStatus().Registered {
		t.Fatal("fake till did not register")
	}
	rec := postForm(mux, "/api/plugins/store/download", url.Values{"listing_id": {"lst-1"}}, nil)
	if rec.Code == http.StatusPreconditionRequired {
		t.Fatalf("registered till's download refused for terms: %s", rec.Body.String())
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
		t.Fatalf("terms_version = %q, want nothing recorded", v)
	}
}
