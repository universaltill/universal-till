# Code review: translate plugin export-entry labels via T (ut-docs#1883)

**Branch:** `feat/1883-plugin-export-entry-label-i18n`
**Author:** Farshid Mirza (pipeline, `lane:cloud-24`)
**Reviewer:** independent Opus subagent (different model from the Sonnet
build pass, per the pipeline's review-model routing for `complexity:medium`)

## What changed

`web/ui/pages/settings.html`'s Data-export entry picker rendered a plugin's
`export`/`report`-type entry `.Label` raw — no `T` call at all — unlike
`internal/pages/plugin_page.go`'s pre-existing `page`-type entry handling,
which already treats `entry.Label` as a plugin-overlay-resolvable locale key
(`architecture/plugin-architecture.md` §7, ADR-0010). This left export-entry
labels permanently un-translatable even though the whole `locales/*.json`
plugin-overlay mechanism existed and worked for `page` entries.

Fix: wrap both the single-entry and multi-entry branches in `T`
(`{{ T .Label }}` / `{{ T (index .exportEntries 0).Label }}`). Backward
compatible by construction — `config.I18n.T` falls back to the literal key
unchanged when no overlay matches, so a plugin shipping a plain-English
label (as every export-type plugin does today, pre-this-card) renders
exactly as before.

Two new tests (`TestSettingsPage_ExportEntryLabel_SingleEntry_ResolvesPluginOverlay`,
`TestSettingsPage_ExportEntryLabel_MultiEntry_ResolvesPluginOverlayAndFallsBackOnMiss`)
drive the real `GET /settings` handler through the real mux against the real
migrated schema, seeding a plugin/plugin_entries row via a new
`seedTestExportPlugin` helper and injecting a `config.I18n` overlay to mimic
`plugins.Manager.syncLocales()`. Both were verified to actually fail without
the fix (reverted the two template lines locally, re-ran, confirmed
`--- FAIL`; restored, confirmed `--- PASS`) — not tests that would pass
regardless.

## Independent review

Full Opus review (background subagent) covering all three repos in this
card (`universal-till`, `ut-plugin-tax-de`, `ut-plugin-payment-sumup`).
Findings and their resolution:

- **F1 (blocker, in `ut-plugin-tax-de`, not this repo):** the sibling
  plugin repo's `scripts/package.sh` omitted `locales/` from the release
  archive, which would have shipped the new translated labels as raw,
  untranslated keys to the German pilot merchant. Fixed in that repo before
  merge — see its own review record.
- **F2 (high, in `ut-plugin-tax-de`):** no fallback-to-English degrade for
  an unresolvable key-shaped label (contrast ADR-0088 Decision G's explicit
  fallback requirement for layout-slot labels). Addressed by extending
  `ut-docs/scripts/templates/guard-plugin-i18n.sh` with a new check that a
  key-shaped `entries[].label` must resolve in `locales/en.json`, catching
  exactly the F1 class of bug at CI time even with no `locales/` directory
  at all.
- **F3/F4 (medium):** plugin `theme`- and `button`-type entry labels are
  the same raw-`.Label` gap on the same page/partial, not fixed here
  (no theme/button plugin ships a key-shaped label today — nothing to
  regress) but now named explicitly in `architecture/plugin-architecture.md`
  §7 and in `ut-plugin-tax-de`'s README so the gap doesn't go
  unacknowledged.
- **F5 (doc, mandatory per this repo's own CLAUDE.md "behaviour changes
  update the affected doc in the same session"):**
  `architecture/plugin-architecture.md` §7 updated with an explicit
  per-entry-type table of what resolves through `T` today, plus a
  packaging note recording the F1 lesson. See `ut-docs`' own review record
  for that diff.
- **F6 (nit):** the two new tests call `httpx.InitI18n` with an
  overlay-carrying translator and didn't restore the plain global
  afterward. Verified benign (no other test in this package/file depends
  on the mutated state, no `t.Parallel()` used anywhere in this package),
  but added `t.Cleanup(func() { initAuthTestI18n(t) })` to both anyway so
  the next reader doesn't have to re-derive that reasoning.

Also independently verified by the reviewer (not just re-stated from the
brief): the `T` fallback-to-literal-key behavior in `internal/config/i18n.go`;
that `settings.html`'s theme picker (line 264, untouched) is a *mixed* list
of built-in and plugin-supplied labels — a defensible scope boundary, not an
unrelated entity, hence F3 above; that the new tests exercise the real
production path (real mux, real migrated schema, real FK-satisfying seed
data) rather than a shortcut; and that no other `.Label` render path in
`web/ui/**` was missed for the `tax`/`export`/`payment` types this card
actually claims to address (payment method names and generic settings-field
labels were already correctly named as an explicit non-goal before review).

## Verification

- `go build ./...` — clean.
- `go test ./internal/pages/...` — full package green (183s), including the
  two new tests individually verified to fail-then-pass around the fix.
- `go vet ./internal/pages/...` — clean.
- `gofmt -l` — clean.
- `bash scripts/ci/guard-i18n.sh` — passes (1601 template keys resolve; the
  new dynamic `{{ T .Label }}` call doesn't trip the static key-literal
  scan, since it isn't a string literal key).

## Not in scope (see ut-docs#1883's tracking comment and the sibling repos'
own README notes for the full reasoning)

- Payment-method display names (`payment_methods.name`, copied verbatim
  from a plugin's manifest at install/sync time, rendered raw on the Pay
  tab) — cross-cutting, affects every payment plugin, tracked separately.
- Plugin `theme`/`button` entry labels (F3/F4 above) — no current plugin
  needs it; noted for whoever picks it up next.
- Generic plugin settings-field labels (`plugin_settings.html`'s
  `{{ .Key }}`) — never translated for any plugin, a separate, bigger
  design question (a display-name convention per setting key) than this
  card's scope.
