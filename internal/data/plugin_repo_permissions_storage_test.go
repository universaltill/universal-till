package data

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestPluginRepo_CheckPermission_NotDeclaredVsDenied(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.perm1", "1.0.0")

	// Never declared: exists=false, must fail closed at the caller
	// (internal/plugins.CheckPermission treats !exists as denied).
	granted, exists, err := repo.CheckPermission(ctx, "com.t.perm1", "net:*")
	if err != nil || exists || granted {
		t.Fatalf("undeclared permission: granted=%v exists=%v err=%v, want exists=false", granted, exists, err)
	}

	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.perm1", []string{"net:*"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	granted, exists, err = repo.CheckPermission(ctx, "com.t.perm1", "net:*")
	if err != nil || !exists || granted {
		t.Fatalf("declared-not-granted: granted=%v exists=%v err=%v, want exists=true granted=false", granted, exists, err)
	}
}

func TestPluginRepo_SetPermission(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.perm2", "1.0.0")
	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.perm2", []string{"storage"}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	if err := repo.SetPermission(ctx, "com.t.perm2", "storage", true); err != nil {
		t.Fatalf("grant: %v", err)
	}
	granted, exists, err := repo.CheckPermission(ctx, "com.t.perm2", "storage")
	if err != nil || !exists || !granted {
		t.Fatalf("after grant: granted=%v exists=%v err=%v", granted, exists, err)
	}

	if err := repo.SetPermission(ctx, "com.t.perm2", "storage", false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	granted, exists, err = repo.CheckPermission(ctx, "com.t.perm2", "storage")
	if err != nil || !exists || granted {
		t.Fatalf("after revoke: granted=%v exists=%v err=%v", granted, exists, err)
	}

	// Setting a permission that was never declared for the plugin must error,
	// not silently create it -- SetPermission is a toggle over declared
	// permissions, not a way to grant undeclared capabilities.
	err = repo.SetPermission(ctx, "com.t.perm2", "devices:usb", true)
	if err == nil {
		t.Fatal("expected an error setting an undeclared permission, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a 'not found' style error, got: %v", err)
	}
	// And it must not have silently created a row.
	if _, exists, _ := repo.CheckPermission(ctx, "com.t.perm2", "devices:usb"); exists {
		t.Fatal("SetPermission on an undeclared permission must not create a row")
	}
}

func TestPluginRepo_ListPermissions(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.perm3", "1.0.0")

	if got, err := repo.ListPermissions(ctx, "com.t.perm3"); err != nil || len(got) != 0 {
		t.Fatalf("expected no permissions yet, got %+v err=%v", got, err)
	}

	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.perm3", []string{"storage", "devices:printer"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := repo.SetPermission(ctx, "com.t.perm3", "storage", true); err != nil {
		t.Fatalf("grant: %v", err)
	}

	rows, err := repo.ListPermissions(ctx, "com.t.perm3")
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 permission rows, got %+v", rows)
	}
	// Ordered by permission name.
	if rows[0].Permission != "devices:printer" || rows[0].Granted {
		t.Fatalf("row 0 unexpected: %+v", rows[0])
	}
	if rows[1].Permission != "storage" || !rows[1].Granted {
		t.Fatalf("row 1 unexpected: %+v", rows[1])
	}
}

func TestPluginRepo_PluginActive(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)

	if active, err := repo.PluginActive(ctx, "com.t.notinstalled"); err != nil || active {
		t.Fatalf("uninstalled plugin: active=%v err=%v", active, err)
	}

	seedCatalogAndPlugin(t, ctx, repo, "com.t.active", "1.0.0")
	if active, err := repo.PluginActive(ctx, "com.t.active"); err != nil || !active {
		t.Fatalf("just-installed plugin should be active: active=%v err=%v", active, err)
	}

	if err := repo.SetPluginActive(ctx, nil, "com.t.active", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if active, err := repo.PluginActive(ctx, "com.t.active"); err != nil || active {
		t.Fatalf("deactivated plugin should not be active: active=%v err=%v", active, err)
	}
}

func TestPluginRepo_StorageGetSetDelete(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)

	// Not found is a distinguishable sentinel error, not a generic error or
	// (nil, nil) -- callers (wasm storage host fn) need to tell "absent" from
	// "read failed".
	_, err := repo.StorageGet(ctx, "com.t.store", "missing")
	if !errors.Is(err, ErrStorageNotFound) {
		t.Fatalf("expected ErrStorageNotFound, got %v", err)
	}

	if err := repo.StorageSet(ctx, "com.t.store", "k1", []byte("hello")); err != nil {
		t.Fatalf("StorageSet: %v", err)
	}
	v, err := repo.StorageGet(ctx, "com.t.store", "k1")
	if err != nil || string(v) != "hello" {
		t.Fatalf("StorageGet after set: v=%q err=%v", v, err)
	}

	// Upsert: setting the same key again updates the value in place.
	if err := repo.StorageSet(ctx, "com.t.store", "k1", []byte("world")); err != nil {
		t.Fatalf("StorageSet overwrite: %v", err)
	}
	v, err = repo.StorageGet(ctx, "com.t.store", "k1")
	if err != nil || string(v) != "world" {
		t.Fatalf("StorageGet after overwrite: v=%q err=%v", v, err)
	}

	// Namespace isolation: a different plugin's identical key is separate.
	if err := repo.StorageSet(ctx, "com.t.store2", "k1", []byte("other-plugin")); err != nil {
		t.Fatalf("StorageSet other plugin: %v", err)
	}
	v, err = repo.StorageGet(ctx, "com.t.store", "k1")
	if err != nil || string(v) != "world" {
		t.Fatalf("plugin storage bled across namespaces: v=%q err=%v", v, err)
	}

	// Oversized key/value are rejected (host-function violation surfaces as -4).
	oversizedKey := strings.Repeat("k", StorageMaxKeyBytes+1)
	if err := repo.StorageSet(ctx, "com.t.store", oversizedKey, []byte("v")); !errors.Is(err, ErrStorageTooLarge) {
		t.Fatalf("expected ErrStorageTooLarge for oversized key, got %v", err)
	}
	oversizedValue := make([]byte, StorageMaxValueBytes+1)
	if err := repo.StorageSet(ctx, "com.t.store", "k2", oversizedValue); !errors.Is(err, ErrStorageTooLarge) {
		t.Fatalf("expected ErrStorageTooLarge for oversized value, got %v", err)
	}

	// Key quota: fill up to the cap with other keys, then the next NEW key
	// for this plugin must be rejected. (k1 already counted above.) Seeded
	// directly via SQL rather than through StorageSet's own count-check path
	// -- exercising the cap boundary itself, not re-testing the insert path
	// 1000+ times.
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin bulk seed tx: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO plugin_storage (plugin_id, key, value) VALUES (?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare bulk seed: %v", err)
	}
	for i := 0; i < StorageMaxKeys-1; i++ {
		if _, err := stmt.ExecContext(ctx, "com.t.store", "bulk-"+strconv.Itoa(i), []byte("x")); err != nil {
			t.Fatalf("bulk seed %d: %v", i, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit bulk seed: %v", err)
	}
	// Now at exactly StorageMaxKeys keys for this plugin (k1 + bulk-0..N-2).
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ?`, "com.t.store").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != StorageMaxKeys {
		t.Fatalf("expected exactly %d keys before the boundary check, got %d", StorageMaxKeys, n)
	}
	if err := repo.StorageSet(ctx, "com.t.store", "one-too-many", []byte("x")); !errors.Is(err, ErrStorageTooLarge) {
		t.Fatalf("expected the (StorageMaxKeys+1)th NEW key to be rejected, got %v", err)
	}
	// But updating an EXISTING key at the cap must still work (it's not a
	// new key -- the cap check excludes the key being written from its count).
	if err := repo.StorageSet(ctx, "com.t.store", "k1", []byte("still-updatable-at-cap")); err != nil {
		t.Fatalf("expected update of an existing key at the cap to succeed, got %v", err)
	}

	if err := repo.DeleteStorage(ctx, "com.t.store"); err != nil {
		t.Fatalf("DeleteStorage: %v", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ?`, "com.t.store").Scan(&n); err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected all of com.t.store's keys gone, got %d", n)
	}
	// The other plugin's storage must be untouched by com.t.store's delete.
	if _, err := repo.StorageGet(ctx, "com.t.store2", "k1"); err != nil {
		t.Fatalf("other plugin's storage should be unaffected: %v", err)
	}
}
