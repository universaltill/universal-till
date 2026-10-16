package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

// writePluginLocale plants paths.Plugins()/<id>/1.0.0/locales/<locale>.json
// and, when bundle != "", the package-root bundle.json.
func writePluginLocale(t *testing.T, dataDir, id, locale, msgs, bundle string) {
	t.Helper()
	root := filepath.Join(dataDir, "plugins", id, "1.0.0")
	if err := os.MkdirAll(filepath.Join(root, "locales"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "locales", locale+".json"), []byte(msgs), 0o644); err != nil {
		t.Fatalf("write locale: %v", err)
	}
	if bundle != "" {
		if err := os.WriteFile(filepath.Join(root, "bundle.json"), []byte(bundle), 0o644); err != nil {
			t.Fatalf("write bundle.json: %v", err)
		}
	}
}

// ADR-0160 §6 (ut-docs#4369): syncLocales sorts each plugin into a layer by
// its signed manifest id and its bundle.json generated_at against
// buildinfo.BuildDate.
func TestSyncLocales_RoutesFirstPartyBundlesByIDAndBuildDate(t *testing.T) {
	dataDir := t.TempDir()
	prev := paths.DataDir()
	paths.Init(dataDir)
	t.Cleanup(func() { paths.Init(prev) })
	prevBuild := buildinfo.BuildDate
	buildinfo.BuildDate = "2026-10-15T12:00:00Z"
	t.Cleanup(func() { buildinfo.BuildDate = prevBuild })

	db := managerTestDB(t)
	ctx := context.Background()
	for _, id := range []string{
		"com.universaltill.language-de", // fresh bundle
		"com.universaltill.language-es", // bundle older than the build
		"com.universaltill.language-pt", // today's pack: no bundle.json
		"com.universaltill.language-tr", // unparseable bundle.json
		"com.example.language-de",       // third party, even with a fresh bundle.json
	} {
		seedInstalledPlugin(t, db, id, id, "1.0.0", "none", true)
	}
	writePluginLocale(t, dataDir, "com.universaltill.language-de", "de", `{"k":"fresh"}`, `{"generated_at":"2026-10-16T00:00:00Z"}`)
	writePluginLocale(t, dataDir, "com.universaltill.language-es", "es", `{"k":"old"}`, `{"generated_at":"2026-10-01T00:00:00Z"}`)
	writePluginLocale(t, dataDir, "com.universaltill.language-pt", "pt", `{"k":"pack"}`, "")
	writePluginLocale(t, dataDir, "com.universaltill.language-tr", "tr", `{"k":"broken"}`, `{generated_at`)
	writePluginLocale(t, dataDir, "com.example.language-de", "de", `{"k":"third"}`, `{"generated_at":"2030-01-01T00:00:00Z"}`)

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	loc := &captureLocalizer{}
	m.SetLocalizer(loc)

	if got := loc.layers.FirstPartyFresh["de"]["k"]; got != "fresh" {
		t.Errorf("fresh first-party bundle not in FirstPartyFresh: %+v", loc.layers.FirstPartyFresh)
	}
	for locale, want := range map[string]string{"es": "old", "pt": "pack", "tr": "broken"} {
		if got := loc.layers.FirstPartyStale[locale]["k"]; got != want {
			t.Errorf("FirstPartyStale[%s][k] = %q, want %q (%+v)", locale, got, want, loc.layers.FirstPartyStale)
		}
		if _, ok := loc.layers.FirstPartyFresh[locale]; ok {
			t.Errorf("%s landed in FirstPartyFresh", locale)
		}
	}
	if got := loc.layers.ThirdParty["de"]["k"]; got != "third" {
		t.Errorf("third-party plugin not in ThirdParty: %+v", loc.layers.ThirdParty)
	}
	if len(loc.layers.ThirdParty) != 1 {
		t.Errorf("a first-party plugin leaked into ThirdParty: %+v", loc.layers.ThirdParty)
	}
}

// A source build (no BuildDate) treats every dated first-party bundle as
// newer than its built-in strings (ADR-0160 §6).
func TestSyncLocales_SourceBuildTreatsDatedBundlesAsFresh(t *testing.T) {
	dataDir := t.TempDir()
	prev := paths.DataDir()
	paths.Init(dataDir)
	t.Cleanup(func() { paths.Init(prev) })
	prevBuild := buildinfo.BuildDate
	buildinfo.BuildDate = ""
	t.Cleanup(func() { buildinfo.BuildDate = prevBuild })

	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.universaltill.language-de", "de", "1.0.0", "none", true)
	writePluginLocale(t, dataDir, "com.universaltill.language-de", "de", `{"k":"v"}`, `{"generated_at":"2020-01-01T00:00:00Z"}`)

	m, err := Init(context.Background(), &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	loc := &captureLocalizer{}
	m.SetLocalizer(loc)
	if got := loc.layers.FirstPartyFresh["de"]["k"]; got != "v" {
		t.Fatalf("source build: dated bundle not fresh: %+v", loc.layers)
	}
}
