package common

import (
	"context"
	"testing"
)

// ut-docs#2499: sale.browsing_mode is the sell screen's browsing layout —
// a closed enum (category_tabs | all_filter_chips | strip_overflow) with the
// same defensive-clamp shape as KeyWindowMode/KeyKioskPaymentMode. It
// replaces the two legacy booleans (settings.sale.show_all_tab and
// sell_screen_categories_tab_enabled), which are retired outright rather
// than left live alongside it.

func TestClampBrowsingMode(t *testing.T) {
	for _, mode := range []string{BrowsingModeCategoryTabs, BrowsingModeAllFilterChips, BrowsingModeStripOverflow} {
		if got := ClampBrowsingMode(mode); got != mode {
			t.Errorf("ClampBrowsingMode(%q) = %q, want unchanged", mode, got)
		}
	}
	for _, bad := range []string{"", "tabs", "CATEGORY_TABS", " category_tabs", "not-a-mode"} {
		if got := ClampBrowsingMode(bad); got != DefaultBrowsingMode {
			t.Errorf("ClampBrowsingMode(%q) = %q, want default %q", bad, got, DefaultBrowsingMode)
		}
	}
	if DefaultBrowsingMode != BrowsingModeCategoryTabs {
		t.Fatalf("DefaultBrowsingMode = %q, want %q (BA decision on ut-docs#2499)", DefaultBrowsingMode, BrowsingModeCategoryTabs)
	}
}

func TestLoadState_BrowsingModeDefaultsToCategoryTabs(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	st := LoadState(ctx, store, baseCfg())
	if st.BrowsingMode != BrowsingModeCategoryTabs {
		t.Fatalf("BrowsingMode = %q, want default %q", st.BrowsingMode, BrowsingModeCategoryTabs)
	}
}

func TestLoadState_BrowsingModeStoredAndClamped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Set(ctx, KeyBrowsingMode, BrowsingModeAllFilterChips); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := LoadState(ctx, store, baseCfg()).BrowsingMode; got != BrowsingModeAllFilterChips {
		t.Fatalf("BrowsingMode = %q, want stored %q", got, BrowsingModeAllFilterChips)
	}
	// A corrupt/hand-edited row must not be trusted outright — same
	// fallback shape as TestLoadState_InvalidStoredWindowModeFallsBackToDefault.
	if err := store.Set(ctx, KeyBrowsingMode, "not-a-mode"); err != nil {
		t.Fatalf("seed invalid: %v", err)
	}
	if got := LoadState(ctx, store, baseCfg()).BrowsingMode; got != DefaultBrowsingMode {
		t.Fatalf("BrowsingMode = %q from invalid stored value, want default %q", got, DefaultBrowsingMode)
	}
}

func TestSaveState_WritesAndClampsBrowsingMode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := SaveState(ctx, store, RuntimeState{BrowsingMode: BrowsingModeStripOverflow}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if raw, _, _ := store.Get(ctx, KeyBrowsingMode); raw != BrowsingModeStripOverflow {
		t.Fatalf("stored %s = %q, want %q", KeyBrowsingMode, raw, BrowsingModeStripOverflow)
	}
	// Defense in depth: the HTTP handler already validates, but a caller
	// that builds st by hand (or a zero-value RuntimeState) must never
	// persist an out-of-enum string.
	if err := SaveState(ctx, store, RuntimeState{BrowsingMode: "bogus"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if raw, _, _ := store.Get(ctx, KeyBrowsingMode); raw != DefaultBrowsingMode {
		t.Fatalf("stored %s = %q, want clamped default %q", KeyBrowsingMode, raw, DefaultBrowsingMode)
	}
}

// The two legacy keys must be gone from what SaveState writes — the BA
// decision (ut-docs#2499) is that the enum subsumes them; a till that
// keeps writing them would leave dead rows behind and invite a reader to
// come back.
func TestSaveState_DoesNotWriteRetiredSellScreenBooleans(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := SaveState(ctx, store, RuntimeState{}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	for _, legacy := range []string{"sale.show_all_tab", "sell_screen_categories_tab_enabled"} {
		if _, ok, _ := store.Get(ctx, legacy); ok {
			t.Errorf("SaveState still writes retired key %q", legacy)
		}
	}
}
