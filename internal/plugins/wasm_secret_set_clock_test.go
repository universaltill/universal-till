package plugins

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0121 §3 (ut-docs#3159): a guest's time.Now() is the host's real wall
// clock, and its monotonic clock advances — not wazero's fake fixed epoch
// (2022-01-01), which silently broke plugins' token-expiry checks.
func TestWasmGuestSeesRealClocks(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.clock"
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	before := time.Now()
	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "clock"})
	after := time.Now()

	wall, _ := res["wall_unix_nano"].(float64)
	guestNow := time.Unix(0, int64(wall))
	if guestNow.Before(before.Add(-time.Second)) || guestNow.After(after.Add(time.Second)) {
		t.Fatalf("guest wall clock = %s, want between %s and %s (the host's real time)", guestNow, before, after)
	}
	elapsed, _ := res["elapsed_nano"].(float64)
	if time.Duration(elapsed) < 10*time.Millisecond {
		t.Fatalf("guest monotonic clock advanced %s across a 20ms sleep, want >= 10ms", time.Duration(elapsed))
	}
}

func secretSetManifest(pluginID string) string {
	return `{"id":"` + pluginID + `","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm",
		"permissions":["secret:write","storage"],
		"settings":[{"key":"tse_admin_puk","type":"secret"},{"key":"erp_url","type":"endpoint"},{"key":"note"}]}`
}

func withSecretKeyStore(t *testing.T) {
	t.Helper()
	prev := secrets.Default()
	secrets.SetDefault(secrets.NewKeyStoreAt(filepath.Join(t.TempDir(), "secrets", "plugin_settings_key.bin"), nil))
	t.Cleanup(func() { secrets.SetDefault(prev) })
}

func storedSettingValue(t *testing.T, w *WasmRuntime, pluginID, key string) (string, bool) {
	t.Helper()
	var v string
	err := w.db.QueryRowContext(context.Background(),
		`SELECT value_json FROM plugin_settings WHERE plugin_id = ? AND key = ?`, pluginID, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// ADR-0121 §3 secret_set (ut-docs#3159): a plugin holding secret:write can
// store a credential it obtained under one of its own `type: "secret"`
// settings. The row is sealed at rest (ADR-0082), settings_get reads back the
// plaintext, and every other case is refused without writing anything.
func TestHostSecretSet(t *testing.T) {
	guest := buildHostfnGuest(t)
	withSecretKeyStore(t)

	newRuntime := func(t *testing.T, pluginID string, perms ...string) *WasmRuntime {
		t.Helper()
		d := hostfnTestDB(t)
		seedPlugin(t, d, pluginID)
		for _, p := range perms {
			grantPerm(t, d, pluginID, p)
		}
		w := NewWasmRuntime(t.TempDir())
		if err := w.load(pluginID, "1.0.0", guest); err != nil {
			t.Fatalf("load: %v", err)
		}
		w.db = d
		return w
	}

	t.Run("declared secret is sealed and reads back", func(t *testing.T) {
		const pluginID = "com.test.secretset"
		withInstalledManifest(t, pluginID, secretSetManifest(pluginID))
		w := newRuntime(t, pluginID, "storage", "secret:write")
		res := runGuestPayload(t, w, w.db, pluginID, map[string]string{"mode": "secret_set", "key": "tse_admin_puk", "value": "puk-123456"})
		if res["secret_code"] != float64(0) {
			t.Fatalf("secret_set = %v, want 0", res["secret_code"])
		}
		if res["get_val"] != "puk-123456" {
			t.Fatalf("settings_get after secret_set = %q (code %v), want the plaintext", res["get_val"], res["get_code"])
		}
		stored, ok := storedSettingValue(t, w, pluginID, "tse_admin_puk")
		if !ok || !secrets.IsSealed(stored) || strings.Contains(stored, "puk-123456") {
			t.Fatalf("stored value_json = %q, want a sealed value with no plaintext", stored)
		}
		// Overwrite: a second secret_set replaces the value.
		res = runGuestPayload(t, w, w.db, pluginID, map[string]string{"mode": "secret_set", "key": "tse_admin_puk", "value": "puk-999"})
		if res["secret_code"] != float64(0) || res["get_val"] != "puk-999" {
			t.Fatalf("overwrite: code %v val %q, want 0 and puk-999", res["secret_code"], res["get_val"])
		}
	})

	refused := []struct {
		name  string
		perms []string
		key   string
		size  int
		want  int
	}{
		{"no secret:write permission", []string{"storage"}, "tse_admin_puk", 0, hostErrDenied},
		{"undeclared key", []string{"storage", "secret:write"}, "undeclared_key", 0, hostErrDenied},
		{"plain setting", []string{"storage", "secret:write"}, "note", 0, hostErrDenied},
		{"endpoint setting", []string{"storage", "secret:write"}, "erp_url", 0, hostErrDenied},
		{"empty key", []string{"storage", "secret:write"}, "", 0, hostErrInvalid},
		{"value over 64 KiB", []string{"storage", "secret:write"}, "tse_admin_puk", secretSetMaxValue + 1, hostErrInvalid},
		{"key over 128 bytes", []string{"storage", "secret:write"}, strings.Repeat("k", 129), 0, hostErrInvalid},
		{"value not UTF-8", []string{"storage", "secret:write"}, "tse_admin_puk", 0, hostErrInvalid},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			const pluginID = "com.test.secretrefused"
			withInstalledManifest(t, pluginID, secretSetManifest(pluginID))
			w := newRuntime(t, pluginID, tc.perms...)
			payload := map[string]any{"mode": "secret_set", "key": tc.key, "value": "v", "size": tc.size, "invalid_utf8": tc.name == "value not UTF-8"}
			res := runGuestPayload(t, w, w.db, pluginID, payload)
			if res["secret_code"] != float64(tc.want) {
				t.Fatalf("secret_set = %v, want %d", res["secret_code"], tc.want)
			}
			if tc.key != "" {
				if v, ok := storedSettingValue(t, w, pluginID, tc.key); ok {
					t.Fatalf("a refused secret_set wrote %q", v)
				}
			}
		})
	}

	t.Run("no installed manifest declares nothing", func(t *testing.T) {
		const pluginID = "com.test.secretnomanifest"
		withInstalledManifest(t, "com.test.other", secretSetManifest("com.test.other"))
		w := newRuntime(t, pluginID, "storage", "secret:write")
		res := runGuestPayload(t, w, w.db, pluginID, map[string]string{"mode": "secret_set", "key": "tse_admin_puk", "value": "v"})
		if res["secret_code"] != float64(hostErrDenied) {
			t.Fatalf("secret_set = %v, want %d", res["secret_code"], hostErrDenied)
		}
	})

	t.Run("unreadable manifest fails closed", func(t *testing.T) {
		const pluginID = "com.test.secretbadmanifest"
		withInstalledManifest(t, pluginID, `{not json`)
		w := newRuntime(t, pluginID, "storage", "secret:write")
		res := runGuestPayload(t, w, w.db, pluginID, map[string]string{"mode": "secret_set", "key": "tse_admin_puk", "value": "v"})
		if res["secret_code"] != float64(hostErrInternal) {
			t.Fatalf("secret_set = %v, want %d", res["secret_code"], hostErrInternal)
		}
		if v, ok := storedSettingValue(t, w, pluginID, "tse_admin_puk"); ok {
			t.Fatalf("wrote %q despite an unreadable manifest", v)
		}
	})

	t.Run("no seal key fails closed, never plaintext", func(t *testing.T) {
		const pluginID = "com.test.secretnokey"
		withInstalledManifest(t, pluginID, secretSetManifest(pluginID))
		w := newRuntime(t, pluginID, "storage", "secret:write")
		prev := secrets.Default()
		secrets.SetDefault(nil)
		t.Cleanup(func() { secrets.SetDefault(prev) })
		res := runGuestPayload(t, w, w.db, pluginID, map[string]string{"mode": "secret_set", "key": "tse_admin_puk", "value": "plain-puk"})
		if res["secret_code"] != float64(hostErrInternal) {
			t.Fatalf("secret_set = %v, want %d", res["secret_code"], hostErrInternal)
		}
		if v, ok := storedSettingValue(t, w, pluginID, "tse_admin_puk"); ok {
			t.Fatalf("wrote %q with no seal key", v)
		}
	})
}
