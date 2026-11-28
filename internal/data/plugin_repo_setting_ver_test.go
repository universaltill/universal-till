package data

import (
	"context"
	"testing"
)

// Per-field version of a plugin setting (design B, group U2): a till edit
// bumps ver and sets origin till; an explicit cloud write stores exactly the
// ver the cloud sent with origin cloud; an unchanged re-save moves nothing;
// a seeded manifest default is origin default and counts as "no local edit".

func TestPluginSettingVer_TillEditBumpsVerOriginTill(t *testing.T) {
	_, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "label", `"a"`); err != nil {
		t.Fatal(err)
	}
	v1, o1, found, err := repo.PluginSettingVer(ctx, "com.example.pay", "label")
	if err != nil || !found || v1 != 2 || o1 != PluginSettingOriginTill {
		t.Fatalf("first till write = ver %d origin %q found %v err %v, want ver 2 origin till", v1, o1, found, err)
	}
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "label", `"a"`); err != nil {
		t.Fatal(err)
	}
	if v, _, _, _ := repo.PluginSettingVer(ctx, "com.example.pay", "label"); v != 2 {
		t.Fatalf("unchanged re-save moved ver to %d, want 2", v)
	}
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "label", `"b"`); err != nil {
		t.Fatal(err)
	}
	if v, o, _, _ := repo.PluginSettingVer(ctx, "com.example.pay", "label"); v != 3 || o != PluginSettingOriginTill {
		t.Fatalf("second change = ver %d origin %q, want 3 till", v, o)
	}
}

func TestPluginSettingVer_CloudWriteStoresTheSentVerOriginCloud(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	_, err := repo.SavePluginSettingsBatch(ctx, "com.example.pay", []PluginSettingWrite{
		{Key: "label", Scope: "global", ValueJSON: `"x"`, Meta: &PluginSettingMeta{Ver: 7, Origin: PluginSettingOriginCloud}},
		{Key: "merchant_code", Scope: "global", ValueJSON: `"M-1"`, DeclaredSecret: true, Meta: &PluginSettingMeta{Ver: 7, Origin: PluginSettingOriginCloud}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"label", "merchant_code"} {
		v, o, found, err := repo.PluginSettingVer(ctx, "com.example.pay", k)
		if err != nil || !found || v != 7 || o != PluginSettingOriginCloud {
			t.Fatalf("%s = ver %d origin %q found %v err %v, want 7 cloud", k, v, o, found, err)
		}
	}
	var at string
	if err := d.QueryRow(`SELECT COALESCE(ver_at,'') FROM plugin_settings WHERE plugin_id = ? AND key = 'label'`, "com.example.pay").Scan(&at); err != nil || at == "" {
		t.Fatalf("ver_at = %q err %v, want the till clock", at, err)
	}
}

func TestPluginSettingVer_AbsentRowAndSeededDefaultCountAsNoLocalEdit(t *testing.T) {
	_, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if v, _, found, err := repo.PluginSettingVer(ctx, "com.example.pay", "nope"); err != nil || found || v != 0 {
		t.Fatalf("absent key = ver %d found %v err %v, want 0 not found", v, found, err)
	}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.example.pay", []PluginSettingRow{
		{Key: "mode", ValueJSON: `"live"`, Scope: "global"},
	}); err != nil {
		t.Fatal(err)
	}
	v, o, found, err := repo.PluginSettingVer(ctx, "com.example.pay", "mode")
	if err != nil || !found || v != 0 || o != PluginSettingOriginDefault {
		t.Fatalf("seeded default = ver %d origin %q found %v err %v, want effective ver 0 origin default", v, o, found, err)
	}
	// The first real edit moves it to ver 2 (the report said 1 for the default).
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "mode", `"test"`); err != nil {
		t.Fatal(err)
	}
	if v, o, _, _ := repo.PluginSettingVer(ctx, "com.example.pay", "mode"); v != 2 || o != PluginSettingOriginTill {
		t.Fatalf("first edit of a default = ver %d origin %q, want 2 till", v, o)
	}
}

func TestPluginSettingVer_ListCarriesVerOriginAt(t *testing.T) {
	_, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "label", `"a"`); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListPluginSettings(ctx, "com.example.pay")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if rows[0].Ver != 2 || rows[0].Origin != PluginSettingOriginTill || rows[0].VerAt.IsZero() {
		t.Fatalf("row = ver %d origin %q at %v, want 2 till and a time", rows[0].Ver, rows[0].Origin, rows[0].VerAt)
	}
}

func TestPluginSettingVer_MergeAdditiveIsATillEdit(t *testing.T) {
	_, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if _, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.pay", "rates", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	v1, o1, _, _ := repo.PluginSettingVer(ctx, "com.example.pay", "rates")
	if _, err := repo.MergeAdditiveJSONMapSetting(ctx, "com.example.pay", "rates", map[string]int{"b": 2}); err != nil {
		t.Fatal(err)
	}
	v2, o2, _, _ := repo.PluginSettingVer(ctx, "com.example.pay", "rates")
	if v1 != 2 || v2 != 3 || o1 != PluginSettingOriginTill || o2 != PluginSettingOriginTill {
		t.Fatalf("ver %d then %d origins %s/%s, want 2 then 3 till", v1, v2, o1, o2)
	}
}
