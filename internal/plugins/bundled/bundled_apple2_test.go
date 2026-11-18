package bundled

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

func grantedSet(t *testing.T, d *data.PluginRepo, id string) map[string]bool {
	t.Helper()
	perms, err := d.ListPermissions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range perms {
		out[p.Permission] = p.Granted
	}
	return out
}

// ut-docs#6627: a bundled first-party plugin is installed with its declared
// permissions granted (the lock vouches for the bytes, as the marketplace
// signature does for a download), by the marketplace rule: first declaration
// only, so a manager's revoke survives the next app update.
func TestSync_GrantsDeclaredPermissionsOnFirstInstallOnly(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.DB)
	got := grantedSet(t, repo, fakeID)
	for _, p := range []string{"pos.tender", "events:receive", "storage"} {
		if !got[p] {
			t.Errorf("%s not granted after a bundled first install: %v", p, got)
		}
	}
	if err := plugins.RevokePermission(ctx, d.DB, fakeID, "storage"); err != nil {
		t.Fatal(err)
	}
	fsys2, lock2 := fixture(t, "1.1.0")
	if _, err := syncFrom(ctx, d.DB, fsys2, lock2); err != nil {
		t.Fatal(err)
	}
	if grantedSet(t, repo, fakeID)["storage"] {
		t.Fatal("an app update re-granted a permission the manager revoked")
	}
}

// Finding 18 (version skew): a row NEWER than the embedded version (a
// marketplace copy restored from a backup or arriving with a join snapshot)
// is not loadable on the appstore channel (only the locked bytes are), so the
// embedded version is installed over it, keeping the shop's enabled state.
func TestSync_EmbeddedVersionReplacesNewerUnverifiableRow(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	restore := buildinfo.OverrideChannelForTest(buildinfo.ChannelDirect)
	fsysNew, _ := fixture(t, "1.1.0")
	raw, _ := fsysNew.ReadFile(fakeID + "/manifest.json")
	m, err := plugins.ParseManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.PersistManifest(ctx, d.DB, m, plugins.InstallOptions{TrustLevel: "trusted"}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.DB)
	if err := repo.SetPluginActive(ctx, nil, fakeID, true); err != nil {
		t.Fatal(err)
	}
	restore()

	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
	fsys, lock := fixture(t, "1.0.0")
	changed, err := syncFrom(ctx, d.DB, fsys, lock)
	if err != nil || !changed {
		t.Fatalf("syncFrom = %v, %v; want the embedded version installed", changed, err)
	}
	if v, _, _ := repo.GetInstalledPluginVersion(ctx, fakeID); v != "1.0.0" {
		t.Fatalf("installed version = %q, want the embedded 1.0.0", v)
	}
	if trust, _, _ := repo.PluginTrustLevel(ctx, fakeID); trust != "system" {
		t.Fatalf("trust = %q, want system (built in)", trust)
	}
	if on, _ := repo.PluginActive(ctx, fakeID); !on {
		t.Fatal("the shop's enabled state was lost")
	}
	// And the next boot is a no-op again.
	if changed, err := syncFrom(ctx, d.DB, fsys, lock); err != nil || changed {
		t.Fatalf("second sync = %v, %v; want no-op", changed, err)
	}
}

func TestParseLock_EnableForCountries(t *testing.T) {
	_, lock := fixture(t, "1.0.0")
	e := lock.Plugins[0]
	good := func(c ...string) []byte {
		e.EnableForCountries = c
		return mustJSON(t, &Lock{Schema: 1, Plugins: []Entry{e}})
	}
	if _, err := ParseLock(good("GB")); err != nil {
		t.Fatalf("GB: %v", err)
	}
	for _, bad := range []string{"gb", "GBR", "", "G1"} {
		if _, err := ParseLock(good(bad)); err == nil {
			t.Errorf("country %q accepted", bad)
		}
	}
}

func TestEnabledByDefaultFor(t *testing.T) {
	id, ok := EnabledByDefaultFor("gb")
	if !ok || id != "com.universaltill.tax-uk" {
		t.Fatalf("EnabledByDefaultFor(gb) = %q, %v; want the bundled UK tax plugin", id, ok)
	}
	if id, ok := EnabledByDefaultFor("DE"); ok {
		t.Fatalf("EnabledByDefaultFor(DE) = %q; only the UK set is bundled", id)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
