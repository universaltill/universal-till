package pages

import (
	"context"
	"strconv"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// retireLocaleOverrides advances the shop's locale generation, which
// invalidates every browser's per-page `ut_lang` override at once
// (ut-docs#2135 — see httpx.LocaleOverride).
//
// Call it wherever an operator EXPLICITLY sets the shop's language. Not from
// a derivation: deriving a locale from the country, or catching up after a
// language pack installs, is the system guessing, and a guess must not throw
// away a choice a human made — the same line KeyLocaleConfirmed already draws.
//
// A failure here is logged, not surfaced: the language has already been saved
// and applied by the time this runs, so the shop default is correct either
// way. The only cost is that an existing stale override survives until the
// shop default next moves.
func retireLocaleOverrides(ctx context.Context, store *settings.Store) {
	if store == nil {
		return
	}
	current, _, err := store.Get(ctx, common.KeyLocaleGeneration)
	if err != nil {
		logging.L().Errorf("settings: read %s: %v", common.KeyLocaleGeneration, err)
	}
	// A missing or unparseable value reads as 0, so the first explicit
	// choice moves to 1 and retires every override recorded before it.
	n, _ := strconv.ParseInt(current, 10, 64)
	n++
	if err := store.Set(ctx, common.KeyLocaleGeneration, strconv.FormatInt(n, 10)); err != nil {
		logging.L().Errorf("settings: write %s: %v", common.KeyLocaleGeneration, err)
		return
	}
	httpx.SetLocaleGeneration(n)
}

// loadLocaleGeneration publishes the persisted locale generation at boot. A
// generation that reset to zero on every restart would silently re-validate
// every override it had previously retired.
func loadLocaleGeneration(ctx context.Context, store *settings.Store) {
	if store == nil {
		return
	}
	v, _, err := store.Get(ctx, common.KeyLocaleGeneration)
	if err != nil {
		logging.L().Errorf("settings: read %s: %v", common.KeyLocaleGeneration, err)
		return
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	httpx.SetLocaleGeneration(n)
}
