package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// TestMain registers a throwaway plugin-settings key store for this
// package's whole test binary (ADR-0082, ut-docs#1739; ut-docs#1746). After
// ut-docs#1746, ReconcilePluginSettings' default-value seeding also seals a
// secret-named or manifest-declared-secret key through the same seam every
// other plugin-settings write uses, and refuses the write when no store is
// registered rather than fall back to plaintext — so a PersistManifest test
// whose fixture happens to declare a realistic key like "api_key" (several
// do, incidentally, in the settings-key-validation suite) now needs one,
// exactly like internal/data's and internal/pages' own TestMain already
// provide for their packages. The store self-generates its key on first use
// (no fetch closure), exactly like a standalone till.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ut-plugins-secrets-")
	if err != nil {
		panic("TestMain: temp dir: " + err.Error())
	}
	secrets.SetDefault(secrets.NewKeyStoreAt(filepath.Join(dir, "plugin_settings_key.bin"), nil))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
