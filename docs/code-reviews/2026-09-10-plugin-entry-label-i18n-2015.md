# Review: plugin entry-label i18n (ut-docs#2015)

**Card:** universaltill/ut-docs#2015 — payment/theme/button plugin manifest
entry labels never resolved through `T`, so they rendered raw regardless of
the till's locale. `page`/`export`/`report` entries already resolved
correctly; this was a gap in the other three types, blocking every
payment/theme/button plugin from ever being translatable.

**Build:** Sonnet (complexity:medium). **Review:** Opus, independent
subagent, isolated worktree.

## What shipped

Wrapped each raw `{{ .Label }}`/`{{ .Name }}` render with `{{ T ... }}` at
its render site, matching the existing `page`/`export`/`report` convention
exactly — no new mechanism:

- Pay tab quick-pay button and payment grid (`web/ui/pages/index.html`)
- Self-order kiosk checkout picker (`web/ui/partials/self_order_payment_picker.html`)
- Settings theme picker, payment-default select, and fee-row name
  (`web/ui/pages/settings.html`)
- `/ui/plugin-buttons` (`web/ui/partials/plugin_buttons.html`)
- The cloud's Design theme picker (`internal/pages/cloudsync_wire.go` via a
  new `cloudThemeOptions` helper in `internal/pages/themes.go`) — found by
  this review, see below; not a template render site, so the original grep
  sweep for `.Label`/`.Name` template usages missed it.

A built-in's plain-text value (`"Cash"`, titleCase theme name, …) passes
through `T` unchanged — verified against `T`'s actual implementation
(`internal/httpx/httpx.go` → `internal/config/i18n.go`), not assumed: it's a
plain map lookup across shop/base/overlay layers, falling back to returning
the key verbatim, no error/panic path.

Four regression tests, one per render surface that needed one:
`TestPayTab_PaymentEntryLabelResolvesPluginLocaleOverlay`,
`TestThemePicker_LabelResolvesPluginLocaleOverlay`,
`TestPluginButtons_LabelResolvesPluginLocaleOverlay`,
`TestCloudThemeOptions_ResolvesPluginLabelThroughTranslator` — each installs
a plugin shipping ONLY a locale overlay (no base-locale string), so the
assertion can only pass if the render path genuinely resolves the key
through the plugin's own overlay, not by coincidence. All four independently
confirmed red pre-fix (revert-then-restore, actual command output below) and
green post-fix.

## Independent review findings

### 1. Cloud Design picker sent the raw translator key — should-fix, FIXED
`cloudsync_wire.go`'s `DeviceExtra` built the cloud portal's theme list from
`opt.Label` raw. The diff's own new `ThemeOption.Label` doc-comment declares
that field a translator key, so this consumer directly contradicted it: a
reseller portal would have listed `theme.midnight.label` while the till's
own picker showed the translated name. This was the one render site the
original sweep missed because it isn't a Go template. Fixed with a new
`cloudThemeOptions()` helper resolving through `httpx.T(httpx.DefaultLocale(), …)`
— the established pattern this repo already uses for non-request-bound
surfaces (`print_api.go`, `kitchen_print.go`, `alerts.go`, `sync_api.go`).
The entry `key` deliberately stays raw (the cloud sends it back as a
`set_setting theme` directive) — pinned by the new test too.

### 2. Doc-comment citations pointed at the wrong section — should-fix, FIXED
All five new doc comments (plus the test-file header) originally cited
`architecture/plugin-architecture.md §7` — that section is "Distribution"
(repo naming/release), unrelated. The actual contract already existed at
`reference/plugin-manifest.md`'s entries table, `label` row: *"Display name,
rendered through the POS translator. Plain text passes through unchanged;
to localize it, use a key … and ship `locales/<locale>.json` overlay
files"* — written generically for every entry type. **The doc was already
correct; the code was the bug.** No `ut-docs` architecture-doc change was
owed for this specific claim (the separate `ut-docs` PR for this card
documents the behavior change itself, which is a distinct, legitimate
doc-first update — see that PR). All citations repointed to ADR-0010 +
`reference/plugin-manifest.md`.

### 3. Test helper leaked process-global i18n state — should-fix, FIXED
`httpx.InitI18n` is process-global with no getter. The new
`newI18nForEntryOverlayTest` helper installed a hermetic overlay-only
translator and never restored it, so it stayed installed for every test the
Go test runner scheduled afterward in the same binary — invisible at the
leaking test's own call site, and dependent on file ordering (adding the new
`cloud_theme_options_test.go` — which sorts before
`country_settings_page_test.go` — reproduced it live:
`TestCountrySettingsPageUnknownShopCountry_ShowsAllWithExplanation` failed
with an unrelated "missing explanation text" symptom purely from this
leak). Fixed with a `restoreRealI18n(t)` helper (`t.Cleanup` re-wiring the
real `web/locales` bundle), wired into both the original helper and the new
test. Full suite back to green regardless of file/test ordering. The same
latent hazard exists in the pre-existing `newHermeticEnOnlyI18n` — left as
is, out of scope for this card; worth a follow-up Backlog card.

### 4. `web/help/img/ar/sell.png` churn was screenshot-generator flake — nit, FIXED (net-zero)
The build's commit changed this PNG's bytes even though nothing about the
sell screen's UI changed. Regenerating via `make docs-shots` in the review
worktree produced a file byte-identical to `main`'s (`sha256 a36aeb69…` both
before this branch and after re-running the generator), while the committed
version was `828be602…` — pure nondeterministic rendering churn, not a real
diff. Reverted to `main`'s bytes; `guard-docs-shots.sh` passes clean.

### 5. Built-in payment names stay untranslated — nit, deliberately deferred
Seeded `payment_methods.name` values are literal `"Cash"`/`"Card"`, so
routing them through `T` is currently a no-op for built-ins (confirmed live:
the regenerated Arabic screenshot still shows `⚡ Cash` next to
fully-Arabic UI). Out of scope for #2015 (which is about plugin entry
labels specifically) — this diff makes translating built-ins possible (seed
them as keys like `tender.cash` instead), but doing so is a separate card.
Noted in the `PaymentMethod.Name` doc comment so it doesn't read as though
the built-in case is already handled.

### 6. Reports/refund show raw payment method IDs — nit, out of scope, correctly untouched
`web/ui/partials/reports_tab_payments.html` and `web/ui/pages/refund.html`
render `payments.method_id`, not the entry label — a different, pre-existing
gap `tenderLabel` already solves for the sale journal elsewhere. Not part of
this card's scope; correctly left alone.

### 7. Coverage gap on 4 of the 8 render sites — nit, not fixed
Automated resolution assertions exist for one site per entry type (4 of the
8 total `{{ T ... }}` sites this diff touches). The other four (Pay-tab
quick-pay button, Settings payment-default select, Settings fee-row, kiosk
checkout picker) all render under existing tests with seeded built-in
methods, so a template error there would already fail — but the *translation
resolution* specifically isn't independently pinned at those four sites.
Low risk given the shared code path (`T` is the same call everywhere); left
as a coverage gap rather than blocking on it.

### Checked clean
- **Coverage sweep**: grepped every `.Label`/`.Name` template reference and
  every caller of `ListActivePaymentMethods`/`ListActiveNonCashPaymentMethods`/
  `availableThemes`/`ListButtonEntries` — all render sites covered (finding 1
  aside). The plugin-action event-bus payload (`"label": b.Label` raw) is
  correctly untouched — that's a machine payload, not a UI surface.
- **No hand-invented translations shipped** — zero changes under
  `web/locales/`; the three German test fixtures live only in `_test.go`
  files, inert by construction.
- No `os.MkdirAll`/`paths.Data(...)` issue — no new production file writes;
  the test helper's fixture writer already uses `paths.Plugins(...)` under
  an isolated `t.TempDir()`.
- No real client/shop names, no secret-shaped literals — test plugin IDs
  mirror the documented sample-plugin naming convention.
- UX checklist (`reference/ux-guidelines.md`): no token/spacing/RTL/modal
  changes — this is a minimal template wrapper around existing render
  sites, not new UI. One item flagged for awareness, not fixed: a
  translated label can run longer than the manifest original; the Pay-tab
  button grid uses `min-height` (not fixed height) in a wrapping grid, so
  low risk.
- Help/manual (`web/help/`): nothing describes or contradicts the old
  raw-rendering behavior; no manual update owed.
- Scope: the `web/help/img/**` diff is required by `guard-docs-shots.sh`,
  not scope creep; nothing unrelated touched.

## TDD re-verification (actual commands, actual output)

Reverted `internal/pages/cloudsync_wire.go`/`themes.go`'s translation call
back to raw (`opt.Label` instead of `httpx.T(locale, opt.Label)`), keeping
the new test:

```
=== RUN   TestCloudThemeOptions_ResolvesPluginLabelThroughTranslator
    cloud_theme_options_test.go:56: cloud Design picker gets the raw
    translator key as the theme's display label ...;
    got map[key:midnight label:theme.midnight.label]
--- FAIL: TestCloudThemeOptions_ResolvesPluginLabelThroughTranslator (0.12s)
FAIL
```

Restored the fix, re-ran: `--- PASS (0.12s)`.

The other three tests (`PayTab_PaymentEntryLabelResolvesPluginLocaleOverlay`,
`ThemePicker_LabelResolvesPluginLocaleOverlay`,
`PluginButtons_LabelResolvesPluginLocaleOverlay`) were independently
revert-then-restore verified during the build phase and again during review;
both passes confirmed the same fail-then-pass shape.

## Gate (run independently, by the reviewer and again by the orchestrator before commit)

`go build ./...`, `go vet ./...`, `gofmt -l .` (clean), `go test ./...`
(full suite, exit 0), `golangci-lint run ./...` (0 issues),
`guard-i18n.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-page-http-error.sh`, `guard-plugin-menu-read.sh`,
`guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`,
`guard-e2e-fixtures-import.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh` — all
exit 0. `guard-deadcode-baseline.sh` and the Android/`guard-gobind-skip`
guards not run: this sandbox lacks the `libgtk-3-dev`/`libwebkit2gtk-4.1-dev`
cgo headers `cmd/unitill-desktop` needs (pre-existing environment gap,
already noted in this repo's own CLAUDE.md, ut-docs#1581), and this diff
touches no `android/**`/`mobile/**` code.

## Verdict

**Safe to merge.** No blockers. Findings 1–4 fixed in this branch; findings
5–7 are genuine but out of scope or low-risk, noted for follow-up rather
than blocking:
- Backlog: seed built-in payment method names as translator keys (finding 5)
- Backlog: `newHermeticEnOnlyI18n` has the same process-global leak hazard
  `restoreRealI18n` fixes here (finding 3's note)
- Optional: add resolution-specific assertions for the 4 uncovered render
  sites (finding 7) — low priority given shared code path
