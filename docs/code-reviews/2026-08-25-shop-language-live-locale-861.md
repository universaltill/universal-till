# Review: shop-level default locale, live-switchable (ut-docs#861)

## What shipped

A shop's default locale — used for notification email and any other
background job with no request to resolve a per-browser preference from —
was previously only settable via `UT_DEFAULT_LOCALE` at install time
(ut-docs#658). This adds a live-switchable version:

- `internal/httpx.SetDefaultLocale(locale string)`: atomic swap of the
  till's default-locale fallback, no translator reload needed
  (`config.I18n` already loads every shipped locale's strings at boot).
- `internal/pages/common.RuntimeState.Locale` (+ `KeyLocale = "store.locale"`,
  the same key `internal/settings/runtime.go`'s `LoadRuntimeConfig`/
  `SaveRuntimeConfig` already read/wrote at boot), wired into `LoadState`/
  `SaveState` the same way Currency/Country already are.
- `internal/pages/init.go`: boot now seeds `httpx.InitI18n`/
  `config.NewI18nFS` from `state.Locale`, not `cfg.Locales.Locale` directly
  — mirrors `httpx.InitCurrency(state.Currency)`'s existing pattern.
- New "Language" card in Settings (`POST /api/settings/save`, same
  handler/elevation gate the Currency card already uses): validates against
  `httpx.AvailableLocales()`, persists, live-applies via
  `httpx.SetDefaultLocale`.
- 3 new i18n keys (`settings.language.title/help/apply`) in all 4 base
  locales; manual (`web/help/*/display.md`, the topic already claiming
  `/settings`) updated with a new step; screenshots regenerated.

## Independent review

Opus, fresh-context, isolated worktree (`git worktree add --detach`, since
this session type doesn't support the built-in `isolation: "worktree"` —
documented fallback used instead). Full record of what it checked and
re-verified is in the session transcript; summary here.

**Verdict: safe to merge, no blocking findings.**

**Findings, triaged:**

- **F2 (fixed in this branch)** — real regression: `SaveState` now
  unconditionally re-persists `KeyLocale` on every save (this card's own
  change), but the raw `/api/settings/upsert` editor's reflect-into-state
  switch had no case for it. An operator editing `store.locale` via
  Settings' All-settings table saw their edit silently reverted by the next
  `/api/settings/save` from *any other* card — the exact ut-docs#178 class
  of bug. Fixed: added the `KeyLocale` case (validated against
  `AvailableLocales()`, matching the shipped card's own validation — this
  key's blast radius if wrong is worse than Currency/Country's unvalidated
  freedom, since an invalid locale breaks `T()` rendering sitewide
  immediately) plus the matching live-apply call. New regression test,
  TDD-verified (reverted the fix, confirmed the exact failure the review
  predicted, restored, confirmed green):
  `TestUpsertLocale_ReflectsIntoStateAndSurvivesLaterSave`.
- **F3 (fixed)** — the manual's new numbered step duplicated an existing
  item number in all 4 locales (goldmark auto-renumbers so nothing was
  visibly broken, but the source read `1,2,3,3,4…`). Renumbered.
- **F4 (fixed)** — the new Language card's status `<span>` had an `id`
  that nothing ever writes to (the shared handler's elevation prompt
  hardcodes `#settings-save-msg`, the Currency card's span, as its target
  regardless of which form triggered it — pre-existing shape of this
  shared endpoint, not introduced here). Removed the misleading `id`;
  left a comment explaining why, rather than widening this card's scope
  into re-targeting the shared elevation flow per-form.
- **F5 (fixed)** — `web/help/en/display.md`'s new step said "the shop's
  own default language"; the i18n key and the ar/fa/tr manual pages all
  correctly say "till's" (accurate — it's a per-till SQLite setting).
  Fixed the English manual to match.
- **F1 (not fixed here — filed as ut-docs#995 follow-up)** — a real,
  broader gap the review found: `config.I18n.T`'s fallback chain
  (`locale → baseLang(locale) → i.fallback → baseLang(i.fallback)`)
  has no guaranteed-English terminal entry. Once `store.locale` holds a
  non-English value, English stops being the last-resort fallback for a
  key missing from an *externally-installed* language-pack plugin (shipped
  locales are safe — `guard-i18n.sh` enforces key parity with `en.json`,
  but overlay-provided packs like `ut-plugin-language-de` aren't covered
  by that guard). Not new-from-nothing (a `UT_DEFAULT_LOCALE=de-DE` install
  already had this exposure) but this card makes it reachable from the UI
  on installs that never had it. Real fix is a terminal `"en"` entry in
  `I18n.T`'s own chain (`internal/config/i18n.go`), which is a change to
  shared i18n-resolution behavior affecting every locale/plugin path, not
  scoped to this card — filed separately rather than folded in here.

**Other review observations, no action needed:** the double
`LoadRuntimeConfig`/`common.LoadState` read of the same `store.locale` key
(one in `app.go` before `pages.Init`, one inside it) is genuinely harmless
— both converge on the same value, not a conflict, traced end to end.
`init.go`'s own comment slightly overstates its effect in production
(`cfg.Locales.Locale` is already overwritten by `LoadRuntimeConfig` by the
time `pages.Init` runs) — left as is, since it's still correct for any
caller that bypasses `app.go`. The setup wizard's language step still only
sets the per-browser cookie, never the shop default — plausible future
follow-up, same class as this card, not filed separately yet.

## Verified beyond automated tests

- **Real driven run** (`go run .`, `UT_STORE=sqlite`, `UT_AUTH=off`,
  Chromium via Playwright): confirmed the Language card renders correctly
  in English (light theme) and Arabic (RTL) — label above field, button
  aligned with the dropdown, no overlap/wrap, logical layout in RTL.
  **Found a real bug this way** (not caught by any unit test): on a fresh
  till, `httpx.DefaultLocale()` returns the full `UT_DEFAULT_LOCALE`
  BCP-47 tag ("en-US"), but the picker's own options
  (`AvailableLocales()`) are bare shipped-locale codes — an unnormalized
  comparison in the `defaultlocale` template func left the dropdown
  showing no selection at all on a never-configured till. Fixed
  (prefix-normalize, same rule `IsRTL` already applies) and confirmed via
  a real HTTP round-trip (`curl`) showing `selected` on the correct
  option, plus a new regression test
  (`TestDefaultLocaleTemplateFuncNormalizesToLanguagePrefix`).
- Confirmed end-to-end via `curl`: `POST /api/settings/save` with
  `locale=fa` persists and is reflected in the very next page render with
  no restart; an unrecognized value (`xx-not-real`) is silently rejected,
  leaving the prior value in place.
- Dark theme: attempted via a client-side `data-theme` attribute toggle in
  the driven-run script; no visible change was observed, likely because
  this app's theme mechanism isn't a simple attribute swap and the script
  didn't trigger it correctly — **not independently confirmed in dark
  theme**. Low risk: the new card uses only shared `.card`/`.btn`/
  `<select>` classes, identical to the already-shipped Currency card
  immediately above it, with no new CSS.
- i18n: `guard-i18n.sh` clean; ar/fa/tr translations for the 3 new keys
  read correctly against sibling `settings.currency.*` keys' tone/register
  (reviewer's independent spot-check agreed). Translated directly by this
  pipeline — the NAS Ollama translation endpoint (`reference/translation.md`)
  is unreachable from this cloud session; standard re-verify follow-up
  filed as ut-docs#996, same pattern as #915/#941/#601's own follow-ups.

## Safe-to-merge verdict

Yes. Full gate green: `gofmt`, `go build`, `go vet`, `go test ./...`
(including a full `-race` run), `guard-data-access.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh`, `guard-compliance-claims.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh` all clean on the final
diff, gated a second time after the review-fix commits (F2-F5) landed.

## Explicitly deferred

- ut-docs#995 — `I18n.T`'s missing guaranteed-English fallback terminal
  entry (review finding F1).
- ut-docs#996 — re-verify the 3 new i18n keys against the NAS Ollama
  translation pipeline from a session with real LAN reachability.
- The setup wizard's own language step still doesn't write the shop
  default (only the per-browser cookie) — noted, not filed as its own
  card yet; low urgency since Settings' new card covers the same need
  post-setup.
