package data

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0121 amendment S2 "one save, one commit" (ut-docs#4157): the settings
// page saves the whole form through SavePluginSettingsBatch — every value
// sealed first, every upsert in ONE transaction, the settings generation
// bumped once, and nothing written when any part fails.

func TestSavePluginSettingsBatch_OneCommitOneBumpChangedKeys(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "label", `"same"`, "global", false); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "limit", `"5"`, "global", false); err != nil {
		t.Fatal(err)
	}
	fired := 0
	repo.OnSettingsChanged(func() { fired++ })

	changed, err := repo.SavePluginSettingsBatch(ctx, "com.example.pay", []PluginSettingWrite{
		{Key: "limit", Scope: "global", ValueJSON: `7`},                                   // update, new shape
		{Key: "label", Scope: "global", ValueJSON: `"same"`},                              // unchanged
		{Key: "enabled", Scope: "global", ValueJSON: `true`},                              // insert
		{Key: "reader", Scope: "register", ValueJSON: `"r-1"`},                            // insert, per-till
		{Key: "merchant_code", Scope: "global", ValueJSON: `"M-1"`, DeclaredSecret: true}, // sealed
	})
	if err != nil {
		t.Fatalf("SavePluginSettingsBatch: %v", err)
	}
	if fired != 1 {
		t.Fatalf("settings generation bumped %d times, want exactly 1", fired)
	}
	want := []string{"enabled", "limit", "merchant_code", "reader"}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("changedKeys = %v, want %v", changed, want)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "limit"); raw != `7` {
		t.Fatalf("limit at rest = %s, want the JSON integer 7", raw)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "enabled"); raw != `true` {
		t.Fatalf("enabled at rest = %s, want true", raw)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "merchant_code"); !secrets.IsSealed(raw) || strings.Contains(raw, "M-1") {
		t.Fatalf("declared secret at rest = %q, want sealed", raw)
	}
	if got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "merchant_code"); err != nil || !found || got != `"M-1"` {
		t.Fatalf("sealed secret reads back %q %v %v", got, found, err)
	}
	var scope string
	if err := d.QueryRow(`SELECT scope FROM plugin_settings WHERE plugin_id = 'com.example.pay' AND key = 'reader'`).Scan(&scope); err != nil || scope != "register" {
		t.Fatalf("reader scope = %q %v, want register", scope, err)
	}

	// A batch that changes nothing commits nothing worth announcing.
	changed, err = repo.SavePluginSettingsBatch(ctx, "com.example.pay", []PluginSettingWrite{{Key: "label", Scope: "global", ValueJSON: `"same"`}})
	if err != nil || len(changed) != 0 || fired != 1 {
		t.Fatalf("no-op batch: changed %v err %v fired %d, want none/nil/1", changed, err, fired)
	}
	// An empty batch is a no-op too.
	if changed, err = repo.SavePluginSettingsBatch(ctx, "com.example.pay", nil); err != nil || len(changed) != 0 || fired != 1 {
		t.Fatalf("empty batch: changed %v err %v fired %d", changed, err, fired)
	}
}

// A failure on any write rolls back every earlier one in the batch, and the
// hook does not fire.
func TestSavePluginSettingsBatch_AllOrNothingOnWriteError(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "label", `"old"`, "global", false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TRIGGER test_boom BEFORE INSERT ON plugin_settings WHEN NEW.key = 'boom'
BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	fired := 0
	repo.OnSettingsChanged(func() { fired++ })
	_, err := repo.SavePluginSettingsBatch(ctx, "com.example.pay", []PluginSettingWrite{
		{Key: "label", Scope: "global", ValueJSON: `"new"`},
		{Key: "boom", Scope: "global", ValueJSON: `1`},
	})
	if err == nil {
		t.Fatal("a failing write must fail the batch")
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "label"); raw != `"old"` {
		t.Fatalf("label = %s after a failed batch, want the earlier write rolled back", raw)
	}
	if fired != 0 {
		t.Fatalf("hook fired %d times for a rolled-back batch", fired)
	}
}

// A secret that can't be sealed (no key store) fails the whole batch before
// anything is written — never a plaintext fallback, never a half save.
func TestSavePluginSettingsBatch_SealFailureWritesNothing(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "label", `"old"`, "global", false); err != nil {
		t.Fatal(err)
	}
	prev := secrets.Default()
	secrets.SetDefault(nil)
	t.Cleanup(func() { secrets.SetDefault(prev) })
	_, err := repo.SavePluginSettingsBatch(ctx, "com.example.pay", []PluginSettingWrite{
		{Key: "label", Scope: "global", ValueJSON: `"new"`},
		{Key: "merchant_code", Scope: "global", ValueJSON: `"M-1"`, DeclaredSecret: true},
	})
	if err == nil {
		t.Fatal("an unsealable secret must fail the batch")
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "label"); raw != `"old"` {
		t.Fatalf("label = %s, want untouched", raw)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE key = 'merchant_code'`).Scan(&n)
	if n != 0 {
		t.Fatalf("unsealable secret left %d rows", n)
	}
}
