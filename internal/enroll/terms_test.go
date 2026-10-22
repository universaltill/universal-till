package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#4717/#4720: the till sends the cloud-terms acceptance with the
// registration, only when the operator has accepted the current version.

func TestRegisterSendsTermsOnlyWhenAccepted(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		resetState()
		var got map[string]any
		srv := registerTestServer(t, &got)
		kv := newFakeKV()
		if err := RecordTermsAcceptance(context.Background(), kv, "de-AT"); err != nil {
			t.Fatalf("RecordTermsAcceptance: %v", err)
		}
		m := config.MarketplaceConfig{EndpointURL: srv.URL + "/api"}
		if err := register(context.Background(), m, "Corner Shop", kv); err != nil {
			t.Fatalf("register: %v", err)
		}
		if got["terms_version"] != TermsVersion {
			t.Fatalf("terms_version = %v, want %q", got["terms_version"], TermsVersion)
		}
		if got["terms_locale"] != "de" {
			t.Fatalf("terms_locale = %v, want \"de\"", got["terms_locale"])
		}
	})
	t.Run("not accepted", func(t *testing.T) {
		resetState()
		var got map[string]any
		srv := registerTestServer(t, &got)
		m := config.MarketplaceConfig{EndpointURL: srv.URL + "/api"}
		if err := register(context.Background(), m, "Corner Shop", newFakeKV()); err != nil {
			t.Fatalf("register: %v", err)
		}
		for _, k := range []string{"terms_version", "terms_locale"} {
			if _, ok := got[k]; ok {
				t.Fatalf("payload carries %s without an acceptance: %#v", k, got)
			}
		}
	})
	t.Run("empty locale omitted", func(t *testing.T) {
		resetState()
		var got map[string]any
		srv := registerTestServer(t, &got)
		kv := newFakeKV()
		if err := RecordTermsAcceptance(context.Background(), kv, ""); err != nil {
			t.Fatal(err)
		}
		m := config.MarketplaceConfig{EndpointURL: srv.URL + "/api"}
		if err := register(context.Background(), m, "Corner Shop", kv); err != nil {
			t.Fatal(err)
		}
		if got["terms_version"] != TermsVersion {
			t.Fatalf("terms_version = %v", got["terms_version"])
		}
		if _, ok := got["terms_locale"]; ok {
			t.Fatalf("empty locale must not be sent: %#v", got)
		}
	})
}

func TestTermsURLMapsLocaleToRegionTaggedPath(t *testing.T) {
	for in, site := range map[string]string{
		"de": "de-de", "de-DE": "de-de", "de-AT": "de-de", "DE": "de-de", "de_CH": "de-de",
		"tr": "tr-tr", "tr-TR": "tr-tr",
		"en": "en-gb", "en-US": "en-gb", "ar": "en-gb", "fa": "en-gb", "": "en-gb", "xx": "en-gb",
	} {
		want := "https://universaltill.com/" + site + "/legal/cloud-terms/"
		if got := TermsURL(in); got != want {
			t.Errorf("TermsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAcceptedTermsIgnoresAnOlderVersion(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV()
	if _, _, _, ok := AcceptedTerms(ctx, kv); ok {
		t.Fatal("fresh till reports accepted terms")
	}
	kv.m[keyTermsVersion] = "v0.0-old"
	kv.m[keyTermsLocale] = "de"
	kv.m[keyTermsAcceptedAt] = "2026-01-01T00:00:00Z"
	if _, _, _, ok := AcceptedTerms(ctx, kv); ok {
		t.Fatal("an older accepted version must not count")
	}
	before := time.Now().UTC().Add(-time.Second)
	if err := RecordTermsAcceptance(ctx, kv, "TR-tr"); err != nil {
		t.Fatal(err)
	}
	v, loc, at, ok := AcceptedTerms(ctx, kv)
	if !ok || v != TermsVersion || loc != "tr" {
		t.Fatalf("AcceptedTerms = %q %q %q %v", v, loc, at, ok)
	}
	ts, err := time.Parse(time.RFC3339, at)
	if err != nil || ts.Before(before) {
		t.Fatalf("accepted_at %q not a fresh RFC3339 time (%v)", at, err)
	}
	if kv.m[keyTermsVersion] != TermsVersion {
		t.Fatalf("stored version = %q", kv.m[keyTermsVersion])
	}
}

// ut-docs#4792: a till registered before the terms step (or via a plugin
// install) accepts later; the acceptance is sent to POST /v1/stores/terms.

// termsEndpoint fakes the cloud's terms endpoint, recording the request.
type termsEndpoint struct {
	calls  int
	auth   string
	ctype  string
	body   map[string]string
	status int
}

func newTermsEndpoint(t *testing.T, status int) (*httptest.Server, *termsEndpoint) {
	t.Helper()
	rec := &termsEndpoint{status: status}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/terms", func(w http.ResponseWriter, r *http.Request) {
		rec.calls++
		rec.auth = r.Header.Get("Authorization")
		rec.ctype = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&rec.body)
		w.WriteHeader(rec.status)
		if rec.status == http.StatusOK {
			_, _ = w.Write([]byte(`{"data":{"store_id":"store-abc","terms_version":"` + TermsVersion + `","terms_locale":"de","accepted_at":"2026-10-22T10:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"code":"terms_version_unknown","message":"nope"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

func termsCfg(url string) *config.Config {
	return &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: url + "/api", StoreID: "store-abc", MerchantToken: "tok-123",
	}}
}

func TestSendTermsAcceptanceSendsStoredAcceptanceAndSetsMarker(t *testing.T) {
	resetState()
	srv, rec := newTermsEndpoint(t, http.StatusOK)
	kv := newFakeKV()
	if err := RecordTermsAcceptance(context.Background(), kv, "de-AT"); err != nil {
		t.Fatal(err)
	}
	if TermsSent(context.Background(), kv) {
		t.Fatal("fresh acceptance reports sent")
	}
	if err := SendTermsAcceptance(context.Background(), termsCfg(srv.URL), kv); err != nil {
		t.Fatalf("SendTermsAcceptance: %v", err)
	}
	if rec.calls != 1 || rec.auth != "Bearer tok-123" || rec.ctype != "application/json" {
		t.Fatalf("request calls=%d auth=%q ctype=%q", rec.calls, rec.auth, rec.ctype)
	}
	want := map[string]string{"store_id": "store-abc", "terms_version": TermsVersion, "terms_locale": "de"}
	for k, v := range want {
		if rec.body[k] != v {
			t.Fatalf("body[%s] = %q, want %q (body %v)", k, rec.body[k], v, rec.body)
		}
	}
	if !TermsSent(context.Background(), kv) {
		t.Fatal("marker not set after a 200")
	}
}

func TestSendTermsAcceptanceRefusedKeepsMarkerUnset(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		resetState()
		srv, rec := newTermsEndpoint(t, status)
		kv := newFakeKV()
		_ = RecordTermsAcceptance(context.Background(), kv, "en")
		err := SendTermsAcceptance(context.Background(), termsCfg(srv.URL), kv)
		var he *RegisterHTTPError
		if err == nil || !errors.As(err, &he) || he.Status != status || rec.calls != 1 {
			t.Fatalf("status %d: err=%v calls=%d, want a *RegisterHTTPError", status, err, rec.calls)
		}
		if TermsSent(context.Background(), kv) {
			t.Fatalf("status %d: marker set on a refusal", status)
		}
	}
}

func TestSendTermsAcceptanceNotRegisteredMakesNoRequest(t *testing.T) {
	resetState()
	srv, rec := newTermsEndpoint(t, http.StatusOK)
	kv := newFakeKV()
	_ = RecordTermsAcceptance(context.Background(), kv, "en")
	cfg := termsCfg(srv.URL)
	cfg.Marketplace.StoreID, cfg.Marketplace.MerchantToken = "", ""
	if err := SendTermsAcceptance(context.Background(), cfg, kv); !errors.Is(err, ErrTillNotRegistered) {
		t.Fatalf("err = %v, want ErrTillNotRegistered", err)
	}
	if rec.calls != 0 {
		t.Fatalf("%d requests while unregistered", rec.calls)
	}
}

func TestSendTermsAcceptanceWithoutAcceptanceMakesNoRequest(t *testing.T) {
	resetState()
	srv, rec := newTermsEndpoint(t, http.StatusOK)
	kv := newFakeKV()
	if err := SendTermsAcceptance(context.Background(), termsCfg(srv.URL), kv); !errors.Is(err, ErrNoTermsAcceptance) {
		t.Fatalf("err = %v, want ErrNoTermsAcceptance", err)
	}
	if rec.calls != 0 {
		t.Fatalf("%d requests with nothing accepted", rec.calls)
	}
}

func TestRegisterSendingTermsSetsSentMarker(t *testing.T) {
	resetState()
	var got map[string]any
	srv := registerTestServer(t, &got)
	kv := newFakeKV()
	m := config.MarketplaceConfig{EndpointURL: srv.URL + "/api"}
	if err := register(context.Background(), m, "Corner Shop", kv); err != nil {
		t.Fatal(err)
	}
	if TermsSent(context.Background(), kv) {
		t.Fatal("registration without terms set the sent marker")
	}
	resetState()
	_ = RecordTermsAcceptance(context.Background(), kv, "en")
	if err := register(context.Background(), m, "Corner Shop", kv); err != nil {
		t.Fatal(err)
	}
	if !TermsSent(context.Background(), kv) {
		t.Fatal("registration that sent terms_version did not set the sent marker")
	}
}
