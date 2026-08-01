package pages

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#177: Init's boot-time "persist resolved defaults" call used to
// hand-copy a partial common.RuntimeState{} literal that dropped
// IdleLockMinutes/KioskIdleResetSeconds. SaveState writes those two keys
// unconditionally (unlike UIScale/OSKMode, which are guarded against a zero
// value), so first boot persisted 0 for both, and every boot after that
// LoadState read 0 back — silently disabling the unattended-till auto-lock
// and the self-order kiosk's idle-reset with no error, no log line, and no
// UI indication. This drives Init itself across two real, consecutive boots
// against the same on-disk DB (exactly what a till restart is) and asserts
// the documented defaults still resolve afterward, not 0.
func TestInit_IdleAndKioskDefaultsSurviveTwoConsecutiveBoots(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	d, err := db.Open(filepath.Join(t.TempDir(), "boot_twice.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	boot := func(n int) {
		pm, err := plugins.Init(ctx, cfg, d.DB)
		if err != nil {
			t.Fatalf("boot %d: plugins.Init: %v", n, err)
		}
		var wg sync.WaitGroup
		Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
	}

	boot(1) // fresh install: config-derived defaults get persisted for the first time
	boot(2) // restart: must re-resolve the SAME defaults, not zeros left by a lossy persist

	store := settings.NewStore(d.DB)

	// Check the raw persisted rows directly, not just LoadState's resolved
	// view — the bug WROTE "0" to the store, and an absent key would also
	// resolve correctly via LoadState's own default fallback, which alone
	// wouldn't prove the boot-persist step is what's actually correct here.
	if v, ok, err := store.Get(ctx, common.KeyIdleLock); err != nil || !ok || v != "10" {
		t.Errorf("raw store %s = (%q, ok=%v, err=%v), want (\"10\", true, nil)", common.KeyIdleLock, v, ok, err)
	}
	if v, ok, err := store.Get(ctx, common.KeyKioskIdleReset); err != nil || !ok || v != "60" {
		t.Errorf("raw store %s = (%q, ok=%v, err=%v), want (\"60\", true, nil)", common.KeyKioskIdleReset, v, ok, err)
	}

	st := common.LoadState(ctx, store, cfg)
	if st.IdleLockMinutes != common.DefaultIdleLockMinutes {
		t.Errorf("after two boots, IdleLockMinutes = %d, want default %d (auto-lock silently disabled)",
			st.IdleLockMinutes, common.DefaultIdleLockMinutes)
	}
	if st.KioskIdleResetSeconds != common.DefaultKioskIdleResetSeconds {
		t.Errorf("after two boots, KioskIdleResetSeconds = %d, want default %d (kiosk idle-reset silently disabled)",
			st.KioskIdleResetSeconds, common.DefaultKioskIdleResetSeconds)
	}
}
