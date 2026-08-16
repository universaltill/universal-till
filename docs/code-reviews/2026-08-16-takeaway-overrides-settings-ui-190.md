# Code review — takeaway_rate_overrides settings UI (ut-docs#190)

- **Date:** 2026-08-16
- **Branch:** `feat/tax-de-takeaway-overrides-ui`
- **Card:** universaltill/ut-docs#190 (`complexity:medium`, `pilot:germany`)
- **Author model:** Sonnet (dev subagent) · **Independent review:** Opus
  (fresh-context subagent, isolated worktree) · per the pipeline's model
  routing.

## What shipped

A typed editor for the `takeaway_rate_overrides` plugin setting on core's
generic plugin-settings page (`/plugins/{id}/settings`), replacing the
raw-JSON text input for the §12 UStG dine-in/takeaway VAT switch
(`ut-plugin-tax-de`'s `tax.rate.ask` map):

- One row per active tax code (name + dine-in rate), a percent input per
  row; the catalog's `takeaway_rate_basis_points` (populated by the
  Loyverse/SumUp/BKP importers) shown as a placeholder suggestion.
- Orphaned override entries (tax code deleted since) render labelled and
  clearable.
- Stored in the exact JSON-string encoding `hostSettingsGet` unwraps, so
  the plugin's `map[string]int` parse is untouched; the existing
  `BumpGeneration` on save invalidates the plugin's cached answers
  (ut-docs#222 path unchanged).
- Key-name heuristic (`isTaxRateOverridesKey`, same family as
  `isSecretSettingKey`) — a settings-surface convention any tax plugin
  can adopt, not a tax-de special case in core. No ADR: follows the
  established pattern, no new cross-cutting mechanism.
- New `CatalogRepo.ListTaxCodes` (SQL stays in `internal/data`).
- 7 new `plugins.settings.takeaway.*` keys in all four core locales
  (en/ar/fa/tr), help topic `plugins.md` updated in all four locales;
  matching de/es translations land in the language-pack repos'
  `feat/takeaway-overrides-keys` branches immediately after this merges
  (lang-pack-drift is blocking on `main` pushes).

## Independent review findings (Opus, all verified & fixed same-branch)

1. **BLOCKER — non-finite/overflowing input silently persisted garbage on
   amd64.** `strconv.ParseFloat` accepts `NaN`/`Inf`/`1e300`;
   `int(math.Round(x))` on those is implementation-defined — arm64 (dev
   machines, CI) rounds to 0 and hides it, amd64 (production) yields
   `math.MinInt64`, persisted with a green "saved" tick, and a value the
   wasm32 plugin's `map[string]int` parse then chokes on. **Fixed:**
   validation moved onto the float (`IsNaN`/`IsInf`/`<0`/`>100`) before
   any int conversion; regression test runs the full rejection table and
   was additionally executed under `GOARCH=amd64` (Rosetta) to prove the
   fix on the affected architecture.
2. **MAJOR — full-map replace reintroduced the ut-docs#532 lost-update.**
   A stale form (rendered before an entry existed) silently deleted
   entries a concurrent writer — e.g. a catalog import's
   `MergeAdditiveJSONMapSetting` — had added. **Fixed:** the save now
   starts from the stored map; a field ABSENT from the form preserves its
   entry, present-but-blank is an explicit delete. Residual race window
   (read→write not transactional) is now limited to two writers editing
   the same tax code simultaneously — accepted, noted here.
3. **MAJOR — invalid rate aborted mid-write-loop**, leaving sibling
   settings (keys sorting before `takeaway…`) committed with no
   generation bump and no audit row. **Fixed:** typed submission is
   validated before ANY write; a 400 now leaves every setting untouched
   (regression test seeds an `api_endpoint` sibling and proves it).
4. **MAJOR — the localized 400 message never reached the user** (htmx
   doesn't swap 4xx; generic "server error" banner shown instead, the
   new `invalid_rate` strings dead). **Fixed:** page-local
   `htmx:responseError` handler (refund.html precedent) renders the
   server's message into `#ps-msg`; verified in a real browser — `0`
   passes client constraints, server rejects, "✗ Invalid takeaway rate"
   appears, and a subsequent valid save recovers.
5. **MINOR — sub-1-bp input (`0.004`) silently swallowed as "saved".**
   Fixed: any non-blank value rounding to ≤0 bp is a 400 (the plugin
   ignores bp≤0, so "saving" it would store nothing while reporting
   success).
6. **MINOR — "leave blank for no change" wording was false** (blanking a
   filled field deletes the override; the adjacent secret field trains
   the opposite reading). Fixed in all four core locales + help topics +
   the de/es pack branches: blank now reads "charge the dine-in rate".
7. **MINOR — unparseable stored value rendered as an all-blank editor and
   would be silently overwritten on save.** Fixed: parse failure falls
   back to the raw text input so the hand-edited value stays visible
   (same refuse-to-clobber stance as `MergeAdditiveJSONMapSetting`).
8. **MINOR — typed branch dropped the per-till marker; table lacked an
   overflow wrapper.** Both fixed (marker on the fieldset legend,
   `overflow-x:auto` wrapper).

Review also flagged two of the original tests as vacuous (passed against
pre-change code); the unknown-tax-code test was strengthened to carry a
known field so it now fails without the typed path. The no-tax-codes
fallback test is vacuous pre-change by construction but meaningful as a
post-change regression guard — accepted as-is.

TDD spot-check (by the reviewer, in its isolated worktree): implementation
reverted to base, new tests re-run — compile failure in `internal/data`,
6/7 page tests fail; restored, all pass.

## Verified beyond automated tests

- Real browser drive (Playwright/Chromium, 1024×600) against a throwaway
  till: save → exact stored bytes `{"tc-red":550,"tc-std":700}` (5.5% →
  550 bp) confirmed in SQLite; values round-trip into the form; orphan
  entry cleared; audit row written; error path and recovery (above).
- Visual check: en light + fa RTL screenshots read by a human-equivalent
  eye — labels above fields, table mirrors correctly in RTL, translated
  headers fit; doubled parentheses on the orphan label found this way and
  fixed. NOT visually inspected: dark theme, ar/tr layouts (strings
  checked textually only).
- Full e2e suite (118 Playwright specs, default project): green before
  and after the fix round.
- Full gate: `go build`, `go vet`, `go test ./...` (two failures
  pre-existing on `main` and identical there — see Deferred),
  guard-data-access / i18n / kiosk-engine / plugin-menu-read /
  help-topics / compliance-claims all pass; touched files gofmt-clean.

## Scoping note on the card's cross-repo AC

The AC asks for a regression test proving `handleTaxRateAsk` picks the
value up. The plugin's compiled wasm lives in `ut-plugin-tax-de` (no
checked-in wazero harness); core's test instead asserts the exact
`hostSettingsGet` round-trip + `map[string]int` parse the plugin performs,
plus the generation bump that invalidates its cache — the full in-repo
half of that contract. A checked-in cross-repo wazero harness remains
future work (noted on the card).

## Verdict

Safe to merge after fixes; the review's blocker and all majors are fixed
and regression-tested, minors fixed. Deferred (filed on the board rather
than fixed here): `main`'s two pre-existing red tests
(`TestInventoryReplicaBannerNeverLinksAcrossDevices`,
`TestPOSRepo_SalesByDay_BusinessDayBoundary_MergesTradingNight`) — not
this branch's regression.
