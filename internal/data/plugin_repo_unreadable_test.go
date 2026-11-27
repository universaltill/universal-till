package data

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// U4 / S7: after a restore the sealed secrets cannot be opened. The list names
// plugin and key only: never a value, never ciphertext.
func TestUnreadableSecretSettings_ListsOnlyUnopenableSealedRows(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	other := make([]byte, secrets.KeySize)
	for i := range other {
		other[i] = byte(i + 7)
	}
	foreign, err := secrets.Seal(other, []byte(`"sk_from_the_lost_till"`))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('r1', 'com.example.pay', 'stripe_secret_key', ?, 'global')`, foreign)
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('r2', 'com.example.pay', 'endpoint', '"https://x"', 'global')`)
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "sumup_api_key", `"readable"`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.UnreadableSecretSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PluginID != "com.example.pay" || got[0].Key != "stripe_secret_key" {
		t.Fatalf("want exactly com.example.pay/stripe_secret_key, got %+v", got)
	}
	if strings.Contains(strings.ToLower(got[0].PluginID+got[0].Key), "sk_") {
		t.Fatalf("a value leaked into the list: %+v", got)
	}
	// The unopenable row still reads as not configured, never as the old ciphertext.
	if v, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "stripe_secret_key"); err != nil || found && v != "" {
		t.Fatalf("restored secret must read as not configured, got %q found=%v err=%v", v, found, err)
	}
}
