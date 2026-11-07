package pages

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// localeBase is the language part of a BCP-47-ish tag, cut at the first '-' or
// '_' and lower-cased ("en_US" -> "en"). pages.baseLang splits only on '-' and
// loadContentBundle relies on that, so this is a separate helper.
func localeBase(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		return tag[:i]
	}
	return tag
}

// normLocale lower-cases a tag and uses '-' as the separator, so "en_US",
// "EN-us" and "en-US" compare equal.
func normLocale(tag string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(tag)), "_", "-")
}

// backfillRegionalLocaleFromCountry (ut-docs#5751): a till whose store.locale
// was never chosen carries the compiled UT_DEFAULT_LOCALE (en-US) even though
// its country (default GB) says en-GB, so dates print month-first. Moves the
// locale to the country's own default ONLY when that is the same language, so
// the UI language, IsRTL and digit shape cannot change; only region-driven
// formatting (date order) does. A derivation, not a choice: never sets
// KeyLocaleConfirmed, never bumps KeyLocaleGeneration.
func backfillRegionalLocaleFromCountry(ctx context.Context, d *common.Deps) {
	if tillFollowsMain(ctx, d) {
		return // ut-docs#2998: shop-wide; the main till's value arrives with the next pull
	}
	confirmed, _, err := d.Settings.Get(ctx, common.KeyLocaleConfirmed)
	if err != nil {
		logging.L().Warnf("locale region backfill: read %s: %v", common.KeyLocaleConfirmed, err)
		return
	}
	if confirmed == "true" {
		return
	}
	st := d.CurrentState()
	if st.Country == "" || strings.TrimSpace(st.Locale) == "" {
		return
	}
	cs, ok, err := data.NewCountrySettingsRepo(d.Db).Get(ctx, st.Country)
	if err != nil {
		logging.L().Warnf("locale region backfill: read country %s: %v", st.Country, err)
		return
	}
	want := strings.TrimSpace(cs.DefaultLocale)
	if !ok || want == "" {
		return
	}
	if localeBase(want) != localeBase(st.Locale) {
		return // never switches language
	}
	if normLocale(want) == normLocale(st.Locale) {
		return // already applied (idempotent)
	}
	if !localeSafeToPreset(want) {
		return
	}
	// settings-write:allow one-time boot derivation of store.locale's region from this till's country (same language); main till only (an additional till returns above, ut-docs#2998)
	if err := d.Settings.Set(ctx, common.KeyLocale, want); err != nil {
		logging.L().Errorf("locale region backfill: persist %s: %v", common.KeyLocale, err)
		return
	}
	cand := st
	cand.Locale = want
	d.SetState(cand)
	httpx.SetDefaultLocale(want)
	logging.L().Infof("locale region backfill: store.locale %s -> %s for country=%s (unconfirmed, same language; ut-docs#5751)", st.Locale, want, st.Country)
}

// shopLocaleForPick (ut-docs#5751) maps a validated Language-card / cloud pick
// (a bare AvailableLocales code like "en") to the tag to store, so confirming
// the shop's own language never drops its region (en-US -> "en" would turn a
// US shop day-first).
func shopLocaleForPick(picked, current, countryDefault string, confirmed bool) string {
	if strings.ContainsAny(picked, "-_") {
		return picked
	}
	if confirmed && localeBase(current) == localeBase(picked) {
		return current
	}
	if countryDefault != "" && localeBase(countryDefault) == localeBase(picked) && localeSafeToPreset(countryDefault) {
		return countryDefault
	}
	if localeBase(current) == localeBase(picked) {
		return current
	}
	return picked
}

// resolveShopLocalePick gathers shopLocaleForPick's inputs. Read failures
// degrade to "store the pick as given".
func resolveShopLocalePick(ctx context.Context, d *common.Deps, picked, country string) string {
	confirmed := false
	if v, _, err := d.Settings.Get(ctx, common.KeyLocaleConfirmed); err != nil {
		logging.L().Warnf("locale pick: read %s: %v", common.KeyLocaleConfirmed, err)
	} else {
		confirmed = v == "true"
	}
	countryDefault := ""
	if country != "" {
		if cs, ok, err := data.NewCountrySettingsRepo(d.Db).Get(ctx, country); err != nil {
			logging.L().Warnf("locale pick: read country %s: %v", country, err)
		} else if ok {
			countryDefault = strings.TrimSpace(cs.DefaultLocale)
		}
	}
	return shopLocaleForPick(picked, strings.TrimSpace(d.CurrentState().Locale), countryDefault, confirmed)
}
