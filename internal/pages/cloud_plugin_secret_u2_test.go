package pages

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Design B 4.3 end to end on the till: a v2 directive from the shared
// vector file reaches a plugin setting through the REAL wiring (verified
// manifest seam, repository, audit); an edited manifest is refused.

type vectorFile struct {
	RecipientSK string `json:"recipient_sk"`
	StoreID     string `json:"store_external_id"`
	Type        string `json:"type"`
	CasesV2     struct {
		Cases []struct {
			Name         string `json:"name"`
			DirectiveID  string `json:"directive_id"`
			PluginID     string `json:"plugin_id"`
			SettingKey   string `json:"setting_key"`
			Plaintext    string `json:"plaintext"`
			SecretSealed string `json:"secret_sealed"`
		} `json:"cases"`
	} `json:"cases_v2"`
}

func installSignedFixture(t *testing.T, tamper bool) (pub string) {
	t.Helper()
	pubK, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	dir := filepath.Join(root, "plugins", "com.example.fixture", "1.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := plugins.Manifest{ID: "com.example.fixture", Name: "Fixture", Version: "1.0", Entrypoint: "./plugin.wasm",
		Runtime: "wasm", CanonicalType: "page", DeviceArch: "any",
		Settings: []plugins.ManifestSetting{{Key: "merchant_code", Type: "text", MaxLen: 32}}}
	digest, err := plugins.ComputeContentDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.ContentDigest = digest
	sign := func(c plugins.Manifest) string {
		raw, _ := json.Marshal(c)
		return hex.EncodeToString(ed25519.Sign(priv, raw))
	}
	content := m
	content.Signature, content.ContentSignature = "", ""
	legacy := m
	legacy.Signature, legacy.ContentDigest, legacy.ContentSignature = "", "", ""
	m.ContentSignature, m.Signature = sign(content), sign(legacy)
	raw, _ := json.Marshal(m)
	if tamper {
		raw = []byte(strings.Replace(string(raw), `"max_len":32`, `"max_len":64`, 1))
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(pubK)
}

func TestCloudSetPluginSecret_V2PlainThroughVerifiedManifest(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		name := map[bool]string{false: "verified manifest applies", true: "edited manifest refused"}[tamper]
		t.Run(name, func(t *testing.T) {
			t.Setenv("UT_AUTH", "off")
			_, dp := newPluginSettingsTestDeps(t)
			pub := installSignedFixture(t, tamper)
			raw, err := os.ReadFile(filepath.Join("internal", "pluginsecretapply", "testdata", "plugin-secret-v1.json"))
			if err != nil {
				t.Fatal(err)
			}
			var v vectorFile
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			c := v.CasesV2.Cases[0] // plain_ok: com.example.fixture / merchant_code, ver 3 base_ver 2
			dp.Cfg.Marketplace = config.MarketplaceConfig{PublicKey: pub, StoreID: v.StoreID}
			if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,is_deprecated,min_pos_version,api_version,published_at) VALUES('com.example.fixture','1.0','F','d','wasm','entry','url','sha','a','s','[]',0,'0.0.0','1',datetime('now'))`); err != nil {
				t.Fatal(err)
			}
			if _, err := dp.Db.Exec(`INSERT INTO plugins(id,name,version,entrypoint,is_active) VALUES('com.example.fixture','F','1.0','entry',1)`); err != nil {
				t.Fatal(err)
			}
			repo := data.NewPluginRepo(dp.Db)
			// The till holds "old" at ver 2 (the cloud's base_ver is 2).
			if _, err := repo.SavePluginSettingsBatch(context.Background(), "com.example.fixture", []data.PluginSettingWrite{
				{Key: "merchant_code", Scope: "global", ValueJSON: `"old"`, Meta: &data.PluginSettingMeta{Ver: 2, Origin: data.PluginSettingOriginTill}},
			}); err != nil {
				t.Fatal(err)
			}
			sk, _ := hex.DecodeString(v.RecipientSK)
			kp := filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")
			if err := os.MkdirAll(filepath.Dir(kp), 0o700); err != nil || os.WriteFile(kp, sk, 0o600) != nil {
				t.Fatal("key setup")
			}
			keys := directivekey.NewAt(kp)

			msg, err := cloudSetPluginSecret(context.Background(), dp, keys, cloudsync.PluginSecretDirective{
				DirectiveID: c.DirectiveID, Type: v.Type, PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 3, SecretSealed: c.SecretSealed,
			})
			got, _, _ := repo.GetPluginSetting(context.Background(), "com.example.fixture", "merchant_code")
			ver, origin, _, _ := repo.PluginSettingVer(context.Background(), "com.example.fixture", "merchant_code")
			if tamper {
				if err == nil || err.Error() != "not_allowed" || got != `"old"` || ver != 2 {
					t.Fatalf("edited manifest: msg %q err %v value %s ver %d, want not_allowed and nothing written", msg, err, got, ver)
				}
				return
			}
			if err != nil || msg != "applied" {
				t.Fatalf("apply: %q %v", msg, err)
			}
			if got != `"MC-43"` || ver != 3 || origin != "cloud" {
				t.Fatalf("stored %s ver %d origin %s, want \"MC-43\" 3 cloud", got, ver, origin)
			}
			var n int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_setting_cloud_applied' AND data_json NOT LIKE '%MC-43%'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("audit rows = %d err %v, want exactly one without the value", n, err)
			}
		})
	}
}
