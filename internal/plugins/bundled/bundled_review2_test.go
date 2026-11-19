package bundled

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
)

// Review 2 (L3): a same-version bundled row that lost its system trust (a
// trust change, or a marketplace copy at the very version the app embeds that
// arrived with a restore or a join snapshot) is put back on system trust at
// the next sync, keeping the shop's switch. Without that it leaves the "Built
// in" group and the set a main till serves to its satellites.
func TestSync_SameVersionRowWithLostSystemTrustIsReconciled(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.DB)
	if err := repo.SetPluginActive(ctx, nil, fakeID, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdatePluginTrust(ctx, nil, fakeID, "untrusted"); err != nil {
		t.Fatal(err)
	}
	changed, err := syncFrom(ctx, d.DB, fsys, lock)
	if err != nil || !changed {
		t.Fatalf("sync = %v, %v; want the row reconciled", changed, err)
	}
	if trust, _, _ := repo.PluginTrustLevel(ctx, fakeID); trust != "system" {
		t.Fatalf("trust after sync = %q, want system", trust)
	}
	if on, _ := repo.PluginActive(ctx, fakeID); !on {
		t.Fatal("the reconcile switched the plugin off")
	}
	// And a healthy row is left alone.
	if changed, err := syncFrom(ctx, d.DB, fsys, lock); err != nil || changed {
		t.Fatalf("second sync = %v, %v; want a no-op", changed, err)
	}
}

// Review 2: the lock pins the manifest by hash, and Sync also refuses an
// embedded manifest that is not the lock entry's own plugin (another id or
// version, a runtime that is not wasm, an entrypoint that is not the pinned
// wasm): a lock edit that pins the wrong manifest must not install anything.
func TestSync_RefusesEmbeddedManifestThatIsNotTheLockEntry(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	for name, mutate := range map[string]func(string) string{
		"other id": func(m string) string {
			return strings.Replace(m, `"id":"com.universaltill.payment-fake"`, `"id":"com.universaltill.payment-other"`, 1)
		},
		"other version": func(m string) string { return strings.Replace(m, `"version":"1.0.0"`, `"version":"9.9.9"`, 1) },
		"other entrypoint": func(m string) string {
			return strings.Replace(m, `"entrypoint":"./bin/plugin.wasm"`, `"entrypoint":"./bin/other.wasm"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := openTestDB(t)
			ctx := context.Background()
			fsys, lock := fixture(t, "1.0.0")
			key := fakeID + "/manifest.json"
			changedManifest := mutate(string(fsys[key].Data))
			if changedManifest == string(fsys[key].Data) {
				t.Fatal("fixture manifest did not change")
			}
			fsys[key].Data = []byte(changedManifest)
			lock.Plugins[0].ManifestSHA256 = sum([]byte(changedManifest)) // the lock pins the wrong manifest
			if _, err := syncFrom(ctx, d.DB, fsys, lock); err == nil {
				t.Fatal("sync accepted an embedded manifest that is not the lock entry's plugin")
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("a refused bundle left %d plugin rows (%v)", n, err)
			}
		})
	}
}
