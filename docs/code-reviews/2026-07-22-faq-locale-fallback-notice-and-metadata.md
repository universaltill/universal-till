# 2026-07-22 — Content bundle locale-fallback notice + version/last-updated metadata

## Context
Spec audit gap (`ut-docs/QUEUE.md`, ut-plugin-faq spec 001-multilingual-faq-page):
`loadContentBundle()` silently fell back to whatever locale file was
available with no signal reaching the template (spec FR-004 / US2
acceptance scenario 2: "presents a clear fallback notice without breaking
layout direction"), and the bundle's `version`/per-entry `last_updated`
fields existed in the JSON schema but were never parsed or shown, so staff
had no way to confirm content recency without developer tools (FR-007).

## Changes
- `contentBundle` gained `Version` (bundle-level) and each entry gained
  `LastUpdated` — both already present in real `ut-plugin-faq` content,
  just never parsed.
- `loadContentBundle`'s signature changed to also return `usedFallback
  bool`: true only when the requested locale needed a true language
  fallback (no shipped file matches its language at all), false for an
  exact match, a regional-prefix match (`en` → `en-US`), or — new — a
  same-base-language match (`en-GB` requested, only `en-US` shipped: still
  the requester's language, just a different region, so NOT a fallback).
- `bundleView` now also returns `Version` and the max of all entries'
  `LastUpdated` (plain string comparison — safe since the dates are
  fixed-width ISO-8601 `YYYY-MM-DD`, which sorts lexicographically in
  chronological order).
- `plugin_content.html`: a fallback notice paragraph when
  `.bundle.FallbackNotice`, and a version/last-updated line when both are
  present. New i18n keys `plugin.content.fallback_notice` /
  `plugin.content.meta` in all 4 core locales (en/ar/fa/tr) — the `meta`
  string is itself a `%s %s` format string consumed via Go template's
  `printf`, the same pattern already used by
  `web/ui/partials/receipt.html`'s `receipt.legal.plugin_label`.
- Tests: unavailable locale gets the notice + correct (max) version/date;
  matched locale doesn't; same-base-language regional variant doesn't
  either.

## Independent review
One review pass (line-by-line + cross-file trace of the only caller;
test-coverage gap check; i18n/escaping correctness). Findings and
disposition:

**Fixed:**
- **Real false-positive**: the prefix-match tier is one-directional (only
  checks "does a shipped file start with `locale-`", e.g. `locale=en` →
  `en-US.json`) — it never checked the reverse: a regional locale request
  (`en-GB`) against a differently-regioned shipped file of the *same*
  language (`en-US.json`). Without a fix, an English speaker whose region
  isn't shipped would incorrectly see "not available in your language yet"
  even though it plainly is their language. Added a same-base-language tier
  (`baseLang()` — the subtag before the first `-`) between the prefix-match
  and the final `en-US`/first-available fallback tier. New test:
  `TestPluginPage_SameBaseLanguageDifferentRegionHasNoFallbackNotice`.
- **Translation phrasing**: reviewer flagged the Turkish, Persian, and
  Arabic `plugin.content.meta` strings as awkward/calque-like versus the
  more fluent `fallback_notice` strings in the same files. Fixed: Turkish
  `sürüm` spelled out (was a bare "s" glued to the placeholder); Persian
  and Arabic reordered to the natural "version of the content" possessive
  construction instead of a literal English word-order copy.

**Considered, deliberately not changed:**
- *`contentBundle.FallbackLocale` (`fallback_locale`) is parsed but never
  consulted anywhere in the tier-selection logic* — pre-existing (predates
  this change, the doc comment already described it before this diff), and
  fixing it well requires clarifying what the field is actually meant to
  mean (a locale's own declared preference vs. a global default) — not
  something to guess at under this fix's scope. Corrected the function's
  doc comment to stop implying it's consulted, so the next person reading
  it isn't misled the way this review's reviewer initially was. Left as a
  known gap, not filed as a new backlog item since it's cosmetic (the
  fallback chain already lands on `en-US` either way).

## Verification
`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all green, including the 7
`TestPluginPage_*` cases.
