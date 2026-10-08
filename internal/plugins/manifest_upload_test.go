package plugins

import (
	"context"
	"strings"
	"testing"
)

// A page entry's config.upload_max_mb (ut-docs#3793) is the largest file,
// in MiB, an operator may submit through its plugin view: an integer
// 1..32, refused at install otherwise.

func uploadEntry(maxMB string) string {
	return `,"entries":[{"type":"page","key":"p","label":"P","route":"/plugin/t/p","view":"t.home","config":{"upload_max_mb":` + maxMB + `}}]`
}

func TestParseManifest_UploadMaxMB_3793(t *testing.T) {
	for _, bad := range []string{`0`, `-1`, `33`, `1.5`, `"8"`, `true`, `null`, `1e9`} {
		_, err := ParseManifest(strings.NewReader(manifestWith(uploadEntry(bad))))
		if err == nil {
			t.Errorf("upload_max_mb %s accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "upload_max_mb") {
			t.Errorf("upload_max_mb %s: error %q does not name the field", bad, err)
		}
	}
	for _, ok := range []string{`1`, `32`, `8.0`} {
		if _, err := ParseManifest(strings.NewReader(manifestWith(uploadEntry(ok)))); err != nil {
			t.Errorf("upload_max_mb %s refused: %v", ok, err)
		}
	}
}

// PersistManifest refuses it too (a manifest that never went through
// ParseManifest, e.g. one built in code), and persists a good one in
// config_json for data.PageEntryRow.UploadMaxMB.
func TestPersistManifest_UploadMaxMB_3793(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	m := pageIconManifest("com.upload.bad", "upbad", "")
	m.Entries[0].Config = map[string]interface{}{"upload_max_mb": float64(64)}
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "upload_max_mb") {
		t.Fatalf("PersistManifest with upload_max_mb 64: err = %v, want a refusal naming upload_max_mb", err)
	}

	m = pageIconManifest("com.upload.good", "upgood", "")
	m.Entries[0].View = "upgood.home"
	m.Entries[0].Config = map[string]interface{}{"upload_max_mb": float64(8)}
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
	var cfg string
	if err := d.DB.QueryRow(`SELECT config_json FROM plugin_entries WHERE plugin_id = 'com.upload.good'`).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, `"upload_max_mb":8`) {
		t.Fatalf("config_json = %s, want upload_max_mb kept", cfg)
	}
}
