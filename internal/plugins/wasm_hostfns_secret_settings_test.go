package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0082 (ut-docs#1739) on the real consumer path: a plugin reading a
// SEALED setting through the settings_get host function gets the plaintext
// — the repository opens it, the WASM boundary never sees ciphertext — and
// a sealed row this till cannot open reads as "not set" (hostErrNotFound),
// never as the raw sealed string. This is exactly how ut-plugin-payment-
// stripe/sumup will read stripe_secret_key/sumup_api_key once sealed.
func TestHostSettingsGet_OpensSealedValueAndHidesUnopenable(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.sealedsettings"

	prev := secrets.Default()
	secrets.SetDefault(secrets.NewKeyStoreAt(filepath.Join(t.TempDir(), "secrets", "plugin_settings_key.bin"), nil))
	t.Cleanup(func() { secrets.SetDefault(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	// The guest reads the fixed key "endpoint"; seed it SEALED directly (as a
	// manifest-declared secret would be stored), bypassing the repository.
	const plain = `"https://erp.example.com/sealed-hook"`
	sealed, err := secrets.SealWithDefault(context.Background(), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(context.Background(), `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('s1', ?, 'endpoint', ?, 'global')`, pluginID, sealed); err != nil {
		t.Fatalf("seed sealed setting: %v", err)
	}

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	res := runGuest(t, w, d, pluginID, srv.URL+"/ping")
	if res["setting_val"] != "https://erp.example.com/sealed-hook" {
		t.Fatalf("settings_get returned %q (code %v), want the opened, unwrapped URL", res["setting_val"], res["setting_code"])
	}

	// Now a row sealed under a DIFFERENT key (another shop's, or a replica
	// that never received its primary's key): not configured, no ciphertext.
	other := make([]byte, secrets.KeySize)
	for i := range other {
		other[i] = byte(0x42 + i)
	}
	foreign, err := secrets.Seal(other, []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(context.Background(), `UPDATE plugin_settings SET value_json = ? WHERE id = 's1'`, foreign); err != nil {
		t.Fatal(err)
	}
	res = runGuest(t, w, d, pluginID, srv.URL+"/ping")
	if v, _ := res["setting_val"].(string); v != "" || strings.Contains(v, "sealed:") {
		t.Fatalf("an unopenable sealed setting must read as empty, got %q", v)
	}
	if res["setting_code"] != float64(hostErrNotFound) {
		t.Fatalf("an unopenable sealed setting must report hostErrNotFound (%d), got %v", hostErrNotFound, res["setting_code"])
	}
}
