package pages

import (
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2015 independent review: ThemeOption.Label is a translator key
// (a plugin theme entry's manifest label), so EVERY consumer has to resolve
// it — not just the Settings <select>. The cloud's Design picker
// (cloudsync_wire.go's DeviceExtra hook) is the other one, and it runs on a
// background goroutine with no request locale, so it resolves through the
// shop's configured default locale (httpx.DefaultLocale(), the same choice
// print_api.go / kitchen_print.go / alerts.go already make for their own
// non-request-bound surfaces). Without this the reseller portal's Design
// picker lists the raw key ("theme.midnight.label") as the option's display
// name while the till's own picker shows the translated one.
func TestCloudThemeOptions_ResolvesPluginLabelThroughTranslator(t *testing.T) {
	chdirRoot(t)
	isolatePluginsDir(t)
	restoreRealI18n(t) // httpx.InitI18n is process-global — don't leak this fixture

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })

	const pluginID, version = "com.test.midnight", "1.0.0"
	seedTestPlugin(t, db, pluginID, "Midnight Theme", version)
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active,sort_order) VALUES('pe-midnight',?,'theme','midnight','theme.midnight.label','{"css":"theme.css"}',1,0)`, pluginID); err != nil {
		t.Fatalf("seed theme entry: %v", err)
	}

	i18n, err := config.NewI18nFS(fstest.MapFS{
		"en.json": &fstest.MapFile{Data: []byte(`{"theme.midnight.label":"Midnight"}`)},
	}, "en")
	if err != nil {
		t.Fatalf("build test i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	opts := cloudThemeOptions(t.Context(), &common.Deps{Db: db})

	var got map[string]string
	for _, o := range opts {
		if o["key"] == "midnight" {
			got = o
		}
	}
	if got == nil {
		t.Fatalf("plugin theme entry missing from cloud theme options: %+v", opts)
	}
	if got["label"] == "theme.midnight.label" {
		t.Errorf("cloud Design picker gets the raw translator key as the theme's display label — ThemeOption.Label must be resolved through httpx.T for this consumer too (ut-docs#2015 review); got %+v", got)
	}
	if got["label"] != "Midnight" {
		t.Errorf("cloud theme option label = %q, want %q (resolved via httpx.T at the shop's default locale)", got["label"], "Midnight")
	}
	// The key stays the raw entry key: the cloud sends it back as a plain
	// `set_setting theme` directive, so translating it would break applying.
	if got["key"] != "midnight" {
		t.Errorf("cloud theme option key = %q, want the raw entry key %q", got["key"], "midnight")
	}
}
