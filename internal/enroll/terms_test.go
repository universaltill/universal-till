package enroll

import (
	"context"
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
