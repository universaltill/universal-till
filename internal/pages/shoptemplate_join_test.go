package pages

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/secrets"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/shoptemplate/shoptemplatetest"
)

func useThemeTemplate(t *testing.T) {
	t.Helper()
	set := shoptemplatetest.TestSet(t, &shoptemplate.Template{
		Schema: 1, ID: "retail", Version: 1, ShopTypes: []string{"retail"},
		Seed: []shoptemplate.Seed{
			{Key: data.OrderTypePromptModeKey, Value: data.OrderTypePromptModeOff},
			{Key: "theme", Value: "dark"},
		},
	})
	prev := shopTemplateFor
	shopTemplateFor = func(_ *common.Deps, st string) *shoptemplate.Resolved { return set.ForType(st) }
	t.Cleanup(func() { shopTemplateFor = prev })
}

func getSetting(t *testing.T, d *common.Deps, k string) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A till that joined a main till (type arrived with the snapshot) seeds only
// its own theme: no shop-wide key, no ledger, once, never over a set value.
func TestSeedJoinedTillTemplateSeedsOnlyPerTillTheme(t *testing.T) {
	useThemeTemplate(t)
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	for k, v := range map[string]string{"sync.primary_url": "http://primary.example", common.KeyShopType: "retail"} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	seedJoinedTillTemplate(ctx, d)
	if got := getSetting(t, d, "theme"); got != "dark" {
		t.Fatalf("theme = %q, want dark", got)
	}
	if got := getSetting(t, d, shoptemplate.ThemeSeededKey); got != "dark" {
		t.Fatalf("theme marker = %q, want dark", got)
	}
	if got := getSetting(t, d, data.OrderTypePromptModeKey); got != "" {
		t.Fatalf("shop-wide %s = %q, want unset", data.OrderTypePromptModeKey, got)
	}
	if got := getSetting(t, d, shoptemplate.LedgerKey); got != "" {
		t.Fatalf("ledger = %q, want none", got)
	}
	// Owner changes the theme; a later pass (next tick / boot) keeps it.
	if err := d.Settings.Set(ctx, "theme", "amber"); err != nil {
		t.Fatal(err)
	}
	seedJoinedTillTemplate(ctx, d)
	if got := getSetting(t, d, "theme"); got != "amber" {
		t.Fatalf("theme after re-run = %q, want amber", got)
	}
}

func TestSeedJoinedTillTemplateMainTillAndNoTypeWriteNothing(t *testing.T) {
	useThemeTemplate(t)
	// Main till (no primary_url) with a type: not this path's job.
	_, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyShopType, "retail"); err != nil {
		t.Fatal(err)
	}
	seedJoinedTillTemplate(t.Context(), d)
	if got := getSetting(t, d, "theme"); got != "" {
		t.Fatalf("main till theme = %q, want unset", got)
	}
	// Joined till whose type has not arrived yet.
	_, _, d2 := newFullAuthDeps(t)
	if err := d2.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	seedJoinedTillTemplate(t.Context(), d2)
	if got := getSetting(t, d2, "theme"); got != "" {
		t.Fatalf("typeless joined till theme = %q, want unset", got)
	}
}

// The 30 s reconcile tick is what lands a shop type pulled from the admin on a
// joined till: drive the real StartTemplateStateReconcile and expect the theme.
func TestTemplateStateReconcileTickSeedsJoinedTillTheme(t *testing.T) {
	useThemeTemplate(t)
	_, _, d := newFullAuthDeps(t)
	ctx, cancel := context.WithCancel(t.Context())
	for k, v := range map[string]string{"sync.primary_url": "http://primary.example", common.KeyShopType: "retail"} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	prev := templateStateInterval
	templateStateInterval = 10 * time.Millisecond
	t.Cleanup(func() { templateStateInterval = prev })

	var wg sync.WaitGroup
	StartTemplateStateReconcile(ctx, d, &wg)
	deadline := time.Now().Add(5 * time.Second)
	// Wait for the tick's whole effect (setting AND in-memory state): the state
	// is published after the setting row, so cancelling on the row alone could
	// cut the tick off between the two (seed-22 flake).
	for (getSetting(t, d, "theme") != "dark" || d.CurrentState().Theme != "dark") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if got := getSetting(t, d, "theme"); got != "dark" {
		t.Fatalf("theme after reconcile tick = %q, want dark", got)
	}
	if got := d.CurrentState().Theme; got != "dark" {
		t.Fatalf("CurrentState().Theme after reconcile tick = %q, want dark", got)
	}
}

// The real boot path (ut-docs#6347): app.Run's LoadRuntimeConfig/
// SaveRuntimeConfig and Init's own SaveState both used to persist the config
// default theme before the joined-till seed ran, and ApplyPerTill skips a set
// theme, so the seed never took effect on a booted till. A joined till whose
// theme row is unset must come out of a boot seeded, and a theme the owner set
// must survive it.
func TestInitBootSeedsJoinedTillTheme(t *testing.T) {
	for _, tc := range []struct{ name, stored, want string }{
		{"unset theme is seeded from the template", "", "dark"},
		{"owner-chosen theme is kept", "amber", "amber"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useThemeTemplate(t)
			chdirRoot(t)
			paths.Init(t.TempDir())
			d := &db.DB{DB: openPagesTestDB(t)}
			defer d.Close()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			store := settings.NewStore(d.DB)
			rows := map[string]string{"sync.primary_url": "http://primary.example", common.KeyShopType: "retail"}
			if tc.stored != "" {
				rows["theme"] = tc.stored
			}
			for k, v := range rows {
				if err := store.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
			// What app.Run does before pages.Init.
			store.LoadRuntimeConfig(ctx, cfg)
			if err := store.SaveRuntimeConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			pm, err := plugins.Init(ctx, cfg, d.DB)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
			t.Cleanup(func() { httpx.InitRailVisibility(nil) })
			if got, _, _ := store.Get(ctx, "theme"); got != tc.want {
				t.Fatalf("theme after boot = %q, want %q", got, tc.want)
			}
		})
	}
}

// A real join (ut-docs#6347): the till's DB is the main till's snapshot, so it
// arrives with the main till's theme (amber) and seeded marker, the shop type
// and the config-default theme. ApplyReplicaIdentity must give it an unset
// theme, so the boot seed lands the template theme in the DB AND in the live
// state, without a restart.
func TestInitBootJoinedTillFromMainSnapshotEndsWithTemplateTheme(t *testing.T) {
	useThemeTemplate(t)
	chdirRoot(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	dbPath := filepath.Join(t.TempDir(), "data", "unitill-pos.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	store := settings.NewStore(d.DB)
	// What the main till's DB held when the snapshot was taken.
	for k, v := range map[string]string{"theme": "amber", shoptemplate.ThemeSeededKey: "amber", common.KeyShopType: "retail"} {
		if err := store.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.StageReplicaIdentity(dbPath, db.ReplicaIdentity{PrimaryURL: "http://primary.example", TillID: "till-2", Bearer: "b", ReceiptPrefix: "T2-"}); err != nil {
		t.Fatal(err)
	}
	// ApplyReplicaIdentity drops the process-wide sealing key store (a joined
	// till must not keep the main's); later tests in this package need TestMain's.
	prevKS := secrets.Default()
	t.Cleanup(func() { secrets.SetDefault(prevKS) })
	if applied, err := db.ApplyReplicaIdentity(d.DB, dbPath); err != nil || !applied {
		t.Fatalf("apply identity: %v %v", applied, err)
	}
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	store.LoadRuntimeConfig(ctx, cfg)
	if err := store.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	pm, err := plugins.Init(ctx, cfg, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	_, deps := Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
	t.Cleanup(func() { httpx.InitRailVisibility(nil) })
	if got, _, _ := store.Get(ctx, "theme"); got != "dark" {
		t.Fatalf("stored theme after join boot = %q, want dark", got)
	}
	if got := deps.CurrentState().Theme; got != "dark" {
		t.Fatalf("CurrentState().Theme = %q, want dark (live, no restart)", got)
	}
}
