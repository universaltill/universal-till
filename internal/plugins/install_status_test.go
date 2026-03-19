package plugins

import (
	"context"
	"database/sql"
	"testing"

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

func TestInstallStatusStoreRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
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
