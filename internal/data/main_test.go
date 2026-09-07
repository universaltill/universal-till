package data

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// TestMain registers a throwaway plugin-settings key store for this
// package's whole test binary (ADR-0082, ut-docs#1739): PluginRepo seals a
// secret-named setting on every write, and refuses the write outright when
// no store is registered rather than fall back to plaintext — so any test
// that upserts e.g. "secret_key" needs a store, and doing it once here beats
// re-adding the bootstrap to every such test. The store self-generates its
// key on first use (no fetch closure), exactly like a standalone till.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ut-data-secrets-")
	if err != nil {
		panic("TestMain: temp dir: " + err.Error())
	}
	secrets.SetDefault(secrets.NewKeyStoreAt(filepath.Join(dir, "plugin_settings_key.bin"), nil))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
