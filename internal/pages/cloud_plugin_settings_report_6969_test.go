package pages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#6969 (ADR-0214): the check-in carries `plugin_settings`, and a
// secret value can never be on that wire. The canary is stored sealed through
// the real repository, then the whole DeviceExtra map (what the sync POST
// marshals) is searched for it.
const settingsCanary6969 = "CANARY-sk_live_0123456789abcdefXYZ4821"

func TestDeviceExtra_PluginSettingsReport_NeverCarriesASecret(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	root := t.TempDir()
	orig := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(orig) })

	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,is_deprecated,min_pos_version,api_version,published_at) VALUES('com.example.pay','2.0.0','Pay','d','none','','url','sha','a','w','[]',0,'0.0.0','1',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins(id,name,version,entrypoint,is_active) VALUES('com.example.pay','Pay','2.0.0','',1)`); err != nil {
		t.Fatal(err)
	}
	dir := paths.Plugins("com.example.pay", "2.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"com.example.pay","name":"Pay","version":"2.0.0","runtime":"none","settings":[
		{"key":"merchant_code","type":"text"},
		{"key":"account_ref","type":"secret","secret_last4":true},
		{"key":"api_key","type":"text"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(dp.Db)
	for k, v := range map[string]string{"merchant_code": "M-42", "account_ref": settingsCanary6969, "api_key": settingsCanary6969} {
		raw, _ := json.Marshal(v)
		if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", k, string(raw), "global", k == "account_ref"); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	extra := buildCloudHooks(dp, nil).DeviceExtra(ctx)
	rep, ok := extra["plugin_settings"]
	if !ok {
		t.Fatal("DeviceExtra has no plugin_settings")
	}
	wire, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), settingsCanary6969) {
		t.Fatalf("a secret value crossed the check-in payload: %s", wire)
	}
	got, _ := json.Marshal(rep)
	for _, want := range []string{`"id":"com.example.pay"`, `"key":"merchant_code"`, `"value":"M-42"`, `"key":"account_ref"`, `"set":true`, `"last4":"4821"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("report %s lacks %s", got, want)
		}
	}
	if !strings.Contains(string(got), `"key":"api_key"`) {
		t.Fatal("secret-named key must still be listed (set only)")
	}
}
