package plugins

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

func recordRevoked(t *testing.T, db *sql.DB, id, version string) {
	t.Helper()
	if _, err := data.NewPluginRepo(db).ReplaceRevokedReleases(context.Background(),
		[]data.RevokedRelease{{ManifestID: id, Version: version, Reason: "test"}}, time.Now()); err != nil {
		t.Fatalf("record revoked: %v", err)
	}
}

func revokedTestManifest(id, version string) *Manifest {
	return &Manifest{ID: id, Name: "Rev", Version: version, Entrypoint: "./run"}
}

func TestPersistManifest_RefusesRevokedRelease_AfterUninstall(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	const id = "com.test.revpersist"
	if err := PersistManifest(ctx, db, revokedTestManifest(id, "1.0.0"), InstallOptions{}); err != nil {
		t.Fatalf("initial persist: %v", err)
	}
	recordRevoked(t, db, id, "1.0.0")
	if err := UninstallPlugin(ctx, db, id); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	err := PersistManifest(ctx, db, revokedTestManifest(id, "1.0.0"), InstallOptions{})
	if !errors.Is(err, ErrRevokedRelease) {
		t.Fatalf("want ErrRevokedRelease, got %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plugins rows = %d err=%v, want 0", n, err)
	}
}

func TestPersistManifest_AllowsOtherVersion(t *testing.T) {
	db := managerTestDB(t)
	const id = "com.test.revother"
	recordRevoked(t, db, id, "1.0.0")
	if err := PersistManifest(context.Background(), db, revokedTestManifest(id, "1.1.0"), InstallOptions{}); err != nil {
		t.Fatalf("a different version must persist: %v", err)
	}
}

func TestPersistManifest_EmptyVersionRevokesAll(t *testing.T) {
	db := managerTestDB(t)
	const id = "com.test.revall"
	recordRevoked(t, db, id, "")
	if err := PersistManifest(context.Background(), db, revokedTestManifest(id, "7.0.0"), InstallOptions{}); !errors.Is(err, ErrRevokedRelease) {
		t.Fatalf("want ErrRevokedRelease, got %v", err)
	}
}

func TestClassifyInstallError_Revoked(t *testing.T) {
	f := ClassifyInstallError(errors.Join(errors.New("persist"), ErrRevokedRelease), false)
	if f.MessageKey != "plugins.install.error.revoked" || f.Retryable {
		t.Fatalf("got %+v", f)
	}
}
