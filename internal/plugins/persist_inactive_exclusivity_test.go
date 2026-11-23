package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6930: a persist that lands the plugin inactive
// (InstallOptions.InstallInactive) cannot mint a second ACTIVE owner of an
// exclusive group, so the two active-owner checks (fiscal-sign hook group,
// layout preset) are skipped for it; the installed-based fiscal.* provides
// check still runs (ADR-0129 §2).

func pluginIsActive(t *testing.T, repo *data.PluginRepo, id string) bool {
	t.Helper()
	on, err := repo.PluginActive(context.Background(), id)
	if err != nil {
		t.Fatalf("PluginActive(%s): %v", id, err)
	}
	return on
}

func TestPersistManifest_InactiveInstallSkipsLayoutPresetCheck(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.a.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatalf("first preset: %v", err)
	}
	b := layoutPresetManifest("com.b.preset", map[string]any{"key": "/kitchen-stations", "hide": true})
	if err := PersistManifest(ctx, d.DB, b, InstallOptions{InstallInactive: true, SkipActiveOwnerChecks: true}); err != nil {
		t.Fatalf("inactive second preset must install: %v", err)
	}
	if pluginIsActive(t, repo, "com.b.preset") {
		t.Fatal("inactive install committed is_active=1")
	}
	if !pluginIsActive(t, repo, "com.a.preset") {
		t.Fatal("existing preset owner was switched off")
	}
	id, _, found, err := repo.ActiveLayoutRoleOwner(ctx, nil, "preset", "")
	if err != nil || !found || id != "com.a.preset" {
		t.Fatalf("active preset owner = %q found=%v err=%v, want com.a.preset", id, found, err)
	}
	// The same shape installed active is still refused.
	c := layoutPresetManifest("com.c.preset", map[string]any{"key": "/orders", "hide": true})
	err = PersistManifest(ctx, d.DB, c, InstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "com.a.preset") {
		t.Fatalf("active second preset must be refused naming the owner, got %v", err)
	}
}

func TestPersistManifest_InactiveInstallSkipsFiscalSignCheck(t *testing.T) {
	for _, ev := range []string{"fiscal.sign.ask", "fiscal.sign.start"} {
		t.Run(ev, func(t *testing.T) {
			d := openRealDB(t)
			ctx := context.Background()
			repo := data.NewPluginRepo(d.DB)
			if err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.a.signer"), InstallOptions{}); err != nil {
				t.Fatal(err)
			}
			b := fiscalSignManifest("com.b.signer")
			b.Hooks[0].Event = ev
			if err := PersistManifest(ctx, d.DB, b, InstallOptions{InstallInactive: true, SkipActiveOwnerChecks: true}); err != nil {
				t.Fatalf("inactive second signer must install: %v", err)
			}
			if pluginIsActive(t, repo, "com.b.signer") {
				t.Fatal("inactive install committed is_active=1")
			}
			if !pluginIsActive(t, repo, "com.a.signer") {
				t.Fatal("existing owner was switched off")
			}
			c := fiscalSignManifest("com.c.signer")
			c.Hooks[0].Event = ev
			if err := PersistManifest(ctx, d.DB, c, InstallOptions{}); err == nil {
				t.Fatal("active second signer must still be refused")
			}
		})
	}
}

func TestPersistManifest_InactiveInstallStillRefusesSecondFiscalProvider(t *testing.T) {
	for _, incumbentOff := range []bool{false, true} {
		name := "incumbent-active"
		if incumbentOff {
			name = "incumbent-disabled"
		}
		t.Run(name, func(t *testing.T) {
			d := openRealDB(t)
			ctx := context.Background()
			repo := data.NewPluginRepo(d.DB)
			if err := PersistManifest(ctx, d.DB, providesManifest("com.first.fiscal", []string{CapabilityFiscalRegister}, nil), InstallOptions{}); err != nil {
				t.Fatal(err)
			}
			if incumbentOff {
				if err := repo.SetPluginActive(ctx, nil, "com.first.fiscal", false); err != nil {
					t.Fatal(err)
				}
			}
			err := PersistManifest(ctx, d.DB, providesManifest("com.second.fiscal", []string{CapabilityFiscalRegister}, nil), InstallOptions{InstallInactive: true, SkipActiveOwnerChecks: true})
			if err == nil || !strings.Contains(err.Error(), "com.first.fiscal") {
				t.Fatalf("inactive second fiscal provider must be refused (ADR-0129 §2), got %v", err)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.fiscal'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("refused install left %d rows (err %v)", n, err)
			}
		})
	}
}

func TestPersistManifest_InactiveUpdateOfActivePluginLandsOff(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	repo := data.NewPluginRepo(d.DB)
	m := layoutPresetManifest("com.a.preset", map[string]any{"key": "/tables", "hide": true})
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	v2 := layoutPresetManifest("com.a.preset", map[string]any{"key": "/tables", "hide": true})
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{InstallInactive: true, SkipActiveOwnerChecks: true}); err != nil {
		t.Fatal(err)
	}
	if pluginIsActive(t, repo, "com.a.preset") {
		t.Fatal("inactive update left the plugin active")
	}
	var ver string
	if err := d.DB.QueryRow(`SELECT version FROM plugins WHERE id = 'com.a.preset'`).Scan(&ver); err != nil || ver != "2.0.0" {
		t.Fatalf("version = %q err %v, want 2.0.0", ver, err)
	}
}

// A bundled-style install (InstallInactive without SkipActiveOwnerChecks) is
// still checked against the active owner: the bundled installer's later
// enables are not gated by pluginEnableRefusal (ut-docs#6930).
func TestPersistManifest_BundledStyleInactiveInstallStillChecksActiveOwner(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutPresetManifest("com.a.preset", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	b := layoutPresetManifest("com.b.preset", map[string]any{"key": "/kitchen-stations", "hide": true})
	err := PersistManifest(ctx, d.DB, b, InstallOptions{InstallInactive: true})
	if err == nil || !strings.Contains(err.Error(), "com.a.preset") {
		t.Fatalf("bundled-style inactive second preset must be refused naming the owner, got %v", err)
	}
	if err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.a.signer"), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := PersistManifest(ctx, d.DB, fiscalSignManifest("com.b.signer"), InstallOptions{InstallInactive: true}); err == nil {
		t.Fatal("bundled-style inactive second fiscal signer must be refused")
	}
}
