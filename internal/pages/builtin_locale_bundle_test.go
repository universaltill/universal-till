package pages

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#5479: a till on a built-in language (ar, en, fa, tr) queues that
// language's first-party bundle; a language core does not ship is left to the
// catalog flows; an installed bundle or a recent check stops a re-queue.

func TestBuiltinBundleSpec(t *testing.T) {
	for _, loc := range []string{"tr", "tr-TR", "ar", "fa-IR", "en", "en-GB"} {
		if spec, ok := builtinBundleSpec(loc); !ok || spec.CanonicalType != "language" || spec.Locale != baseLang(loc) {
			t.Errorf("builtinBundleSpec(%q) = %+v, %v; want a language spec for %q", loc, spec, ok, baseLang(loc))
		}
	}
	for _, loc := range []string{"", "de", "pt-BR", "xx", "zh-Hans"} {
		if _, ok := builtinBundleSpec(loc); ok {
			t.Errorf("builtinBundleSpec(%q) queued a language core does not ship", loc)
		}
	}
}

func TestHasFirstPartyBundle(t *testing.T) {
	ids := []string{"com.universaltill.language-tr", "com.universaltill.language-pt-br", "com.vendor.language-fa", "com.universaltill.tax-gb"}
	for lang, want := range map[string]bool{"tr": true, "pt": true, "fa": false, "ar": false} {
		if got := hasFirstPartyBundle(ids, lang); got != want {
			t.Errorf("hasFirstPartyBundle(%q) = %v, want %v", lang, got, want)
		}
	}
	if !hasFirstPartyBundle([]string{"com.universaltill.language-pt-br"}, "pt-br") {
		t.Error("a regional bundle id must match its own tag")
	}
	if !hasFirstPartyBundle([]string{"com.universaltill.language-PT-BR"}, "pt") {
		t.Error("a regional bundle satisfies its base language")
	}
	if hasFirstPartyBundle([]string{"com.universaltill.language-tr-glossary"}, "tr") {
		t.Error("a longer first-party id is not the tr bundle")
	}
}

func TestInstallBuiltinLocaleBundle(t *testing.T) {
	db := openRealSchemaPagesDB(t)
	st := settings.NewStore(db)
	d := &common.Deps{Db: db, Settings: st}
	ctx := t.Context()
	now := time.Now()
	var calls []basePluginSpec
	var official bool
	var failWith error
	orig := builtinBundleInstallFn
	builtinBundleInstallFn = func(_ context.Context, _ *common.Deps, spec basePluginSpec, accept func(*marketplace.PluginSummary) bool) error {
		calls = append(calls, spec)
		official = accept(&marketplace.PluginSummary{TrustTier: "official"}) && !accept(&marketplace.PluginSummary{TrustTier: "community"})
		return failWith
	}
	t.Cleanup(func() { builtinBundleInstallFn = orig })
	setLocale := func(l string) {
		if err := st.Set(ctx, "store.locale", l); err != nil {
			t.Fatal(err)
		}
	}
	pendingEmpty := func() {
		if p, _ := loadPendingBasePlugins(ctx, d); len(p) != 0 {
			t.Fatalf("a built-in bundle must never use the pending list (it would show a Settings chip): %v", p)
		}
	}

	setLocale("de-DE") // not built in
	installBuiltinLocaleBundle(ctx, d, now)
	if len(calls) != 0 {
		t.Fatalf("de attempted: %v", calls)
	}

	setLocale("tr-TR")
	failWith = errors.New("catalog unreachable")
	installBuiltinLocaleBundle(ctx, d, now) // failed: retried next tick
	installBuiltinLocaleBundle(ctx, d, now.Add(time.Minute))
	if len(calls) != 2 || calls[0].Locale != "tr" {
		t.Fatalf("a failed attempt must be retried on the next tick: %v", calls)
	}
	if !official {
		t.Fatal("the install filter must accept official listings only")
	}

	failWith = nil
	installBuiltinLocaleBundle(ctx, d, now.Add(2*time.Minute)) // answered: wait a day
	installBuiltinLocaleBundle(ctx, d, now.Add(time.Hour))
	if len(calls) != 3 {
		t.Fatalf("an answered look-up repeated within the day: %d calls", len(calls))
	}
	installBuiltinLocaleBundle(ctx, d, now.Add(25*time.Hour))
	if len(calls) != 4 {
		t.Fatalf("not asked again after a day: %d calls", len(calls))
	}

	// Installed: marked done, never reinstalled even if later removed.
	seedInstalledPluginManifest(t, db, "com.universaltill.language-tr", "Turkish", "universaltill", "2.20261104.1", "language")
	installBuiltinLocaleBundle(ctx, d, now.Add(72*time.Hour))
	installBuiltinLocaleBundle(ctx, d, now.Add(200*time.Hour))
	if len(calls) != 4 {
		t.Fatalf("attempted although the bundle is installed: %d calls", len(calls))
	}
	if v, _, _ := st.Get(ctx, keyBuiltinBundleChecked); v != "tr@done" {
		t.Fatalf("marker = %q, want tr@done", v)
	}
	pendingEmpty()
}
