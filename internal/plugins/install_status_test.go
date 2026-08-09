package plugins

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/web/locales"
	_ "modernc.org/sqlite"
)

func TestClassifyInstallError(t *testing.T) {
	tests := []struct {
		name      string
		err       string
		key       string
		retryable bool
	}{
		{name: "incompatible", err: "Incompatible architecture: plugin requires linux/arm64, system is linux/amd64", key: "plugins.install.error.incompatible", retryable: false},
		{name: "invalid package", err: "signature mismatch between marketplace metadata and manifest", key: "plugins.install.error.invalid_package", retryable: false},
		{name: "configuration", err: "marketplace public key not configured", key: "plugins.install.error.configuration", retryable: false},
		{name: "generic retryable", err: "request failed: dial tcp timeout", key: "plugins.install.error.retryable", retryable: true},
		// ut-docs#169: a payment key/label collision is a permanent failure
		// (retrying with the same manifest always fails the same way) until
		// the plugin author picks a different key/label — must not fall
		// into the generic retryable bucket.
		{name: "payment key collides with built-in", err: `payment entry key "cash" collides with an existing built-in or shop-configured tender — pick a plugin-specific key`, key: "plugins.install.error.payment_conflict", retryable: false},
		{name: "payment key owned by uninstalled plugin", err: `payment entry key "shared" belongs to plugin com.gone.pay, which is no longer installed — its tender row is retained for sales history; reinstall it or pick a different key`, key: "plugins.install.error.payment_conflict", retryable: false},
		{name: "payment key owned by another plugin", err: `payment entry key "shared" is already provided by plugin com.other.pay — pick a different key`, key: "plugins.install.error.payment_conflict", retryable: false},
		{name: "payment label collides with built-in", err: `payment entry label "Cash" collides with an existing built-in or shop-configured tender's name — pick a distinct label`, key: "plugins.install.error.payment_conflict", retryable: false},
		{name: "payment label owned by uninstalled plugin", err: `payment entry label "Shared Label" belongs to plugin com.gone.pay, which is no longer installed — its tender row is retained for sales history; reinstall it or pick a different label`, key: "plugins.install.error.payment_conflict", retryable: false},
		{name: "payment label owned by another plugin", err: `payment entry label "Shared Label" is already used by plugin com.other.pay — pick a distinct label`, key: "plugins.install.error.payment_conflict", retryable: false},
		// Production never hands ClassifyInstallError the bare
		// validatePaymentEntryKeys error — installer_marketplace.go wraps it
		// as "persist plugin manifest: %w" first. Pin the wrapped shape too:
		// the existing "manifest validation" case a few lines above matches
		// on a substring of that same wrapper text, so a future wording
		// change to either case could silently make this one unreachable
		// while every other test here stays green (review finding, ut-docs#169).
		{name: "payment key conflict, wrapped as production sends it", err: `persist plugin manifest: payment entry key "shared" belongs to plugin com.gone.pay, which is no longer installed — its tender row is retained for sales history; reinstall it or pick a different key`, key: "plugins.install.error.payment_conflict", retryable: false},
		// ut-docs#472: a page entry key collision is the same permanent-
		// failure shape as the payment case above.
		{name: "page key owned by another plugin", err: `page entry key "shared" is already provided by plugin com.other.page — pick a different key`, key: "plugins.install.error.page_conflict", retryable: false},
		{name: "page key conflict, wrapped as production sends it", err: `persist plugin manifest: page entry key "shared" is already provided by plugin com.other.page — pick a different key`, key: "plugins.install.error.page_conflict", retryable: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failure := ClassifyInstallError(assertErr(tc.err))
			if failure.MessageKey != tc.key {
				t.Fatalf("message key = %q, want %q", failure.MessageKey, tc.key)
			}
			if failure.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", failure.Retryable, tc.retryable)
			}
			if failure.Message == "" {
				t.Fatalf("expected safe human-readable message")
			}
		})
	}
}

// TestClassifyInstallError_KeysResolveInLocales guards a gap guard-i18n.sh
// can't see (review finding, ut-docs#169): the UI renders a MessageKey via
// {{ T .StatusMessageKey }} — a DYNAMIC key, not a literal `{{ T "..." }}`
// call — so guard-i18n.sh's static template scan never checks any key
// ClassifyInstallError can actually return. Had en.json been missed for a
// new key, CI would still be green and a shop owner would see the raw
// locale key string in a red badge instead of a translated message. T falls
// back to returning the key unchanged when it's missing, which is exactly
// what this test would catch.
func TestClassifyInstallError_KeysResolveInLocales(t *testing.T) {
	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("load embedded locales: %v", err)
	}
	keys := []string{
		"plugins.install.error.retryable",
		"plugins.install.error.incompatible",
		"plugins.install.error.configuration",
		"plugins.install.error.invalid_package",
		"plugins.install.error.payment_conflict",
		"plugins.install.error.page_conflict",
	}
	for _, key := range keys {
		if got := i18n.T("en", key); got == key {
			t.Errorf("locale key %q has no translation in en.json — ClassifyInstallError can return this key, but it would render as the raw key string in the plugin install-failure badge", key)
		}
	}
}

func TestInstallStatusStoreRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE plugin_install_status (
		listing_id TEXT PRIMARY KEY, plugin_id TEXT, plugin_name TEXT,
		target_version TEXT, current_version TEXT, state TEXT NOT NULL,
		message_key TEXT, retryable INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create plugin_install_status: %v", err)
	}

	store := NewInstallStatusStore(db)
	ctx := context.Background()

	first := InstallStatusRecord{
		ListingID:      "listing-active",
		PluginID:       "com.example.active",
		PluginName:     "Active Plugin",
		TargetVersion:  "1.2.3",
		CurrentVersion: "1.2.3",
		State:          InstallStateActive,
	}
	second := InstallStatusRecord{
		ListingID:      "listing-failed",
		PluginName:     "Failed Plugin",
		TargetVersion:  "2.0.0",
		CurrentVersion: "2.0.0",
		State:          InstallStateFailed,
		MessageKey:     "plugins.install.error.retryable",
		Retryable:      true,
	}

	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("save first record: %v", err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("save second record: %v", err)
	}

	got, ok, err := store.Get(ctx, first.ListingID)
	if err != nil {
		t.Fatalf("get record: %v", err)
	}
	if !ok {
		t.Fatalf("expected persisted record to exist")
	}
	if got.State != InstallStateActive {
		t.Fatalf("state = %q, want %q", got.State, InstallStateActive)
	}
	if got.CurrentVersion != "1.2.3" {
		t.Fatalf("current version = %q, want 1.2.3", got.CurrentVersion)
	}
	if got.UpdatedAt == "" {
		t.Fatalf("expected updated_at to be populated")
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 records, got %d", len(all))
	}
	if !all[second.ListingID].Retryable {
		t.Fatalf("expected failed status to remain retryable")
	}
	if all[second.ListingID].MessageKey != "plugins.install.error.retryable" {
		t.Fatalf("message key = %q, want retryable key", all[second.ListingID].MessageKey)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

func assertErr(message string) error { return testErr(message) }
