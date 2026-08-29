# Code review — admin-screen decimal fields corrupted by the on-screen keyboard (ut-docs#1275)

- **Date:** 2026-08-29
- **Branch:** `fix/1275-osk-decimal-fields-admin-screens`
- **Reviewer:** independent reviewer (fresh-context, different model — Opus,
  this pipeline's `complexity:medium` review tier), isolated worktree.
- **Verdict: SAFE TO MERGE.** No blockers found. Two should-fix findings,
  both fixed in this same cycle; three nice-to-have findings, two fixed,
  one accepted as-is with reasoning below.

## What shipped

Follow-up from independent review of ut-docs#1272's fix, out of that
card's scope: the identical `type="number"` decimal-corruption root cause
(`osk.js`'s `insert()` does naive `value += text` for number-type fields,
and a number input silently resets `.value` to `""` on any momentarily-
invalid decimal string while typing) was still live on ~15 admin-screen
fields not covered by ut-docs#1249/#1272's fixes. Unlike those two, none
of these fields (except `#stock-cost`) copy into a separate hidden field
via `onchange` — they submit `type="number"` directly via `name=` — so
the "#1249-class always-empty" bug doesn't apply; only the decimal-
corruption bug does, and it's arguably worse (silently wrong, not empty).

**Fields converted** (`type="number"` → `type="text" inputmode="decimal"`
+ a numeric `pattern`, no `oninput` handler needed since nothing copies
into a separate field except `#stock-cost`, whose existing submit-time
sync already picks up whatever was last typed):

- `web/ui/pages/promotions.html`: `value_amount`, `value_percent` (both
  forms — the edit-row form and the new-promotion form)
- `web/ui/pages/settings.html`: Payments fee `percent`, `fixed`
- `web/ui/pages/inventory.html`: `quantity`, `#stock-cost`, `qty_before`,
  `qty_requested`
- `web/ui/pages/tax_codes.html`: `rate`, `takeawayRate`
- `web/ui/pages/country_settings.html`: `tax_rate_pct` (both the per-row
  edit form and the new-country form) — **not** `archive_min_days`
  (`step="1"`, integer-only, never accepts a decimal, correctly left
  alone)

**Pattern choice, following ut-docs#1249's own precedent:**
- Money fields (`value_amount`, fee `fixed`, `#stock-cost`) branch on
  `currency.Decimals` (the global template function `ActiveCurrency`,
  `internal/httpx/httpx.go:507`), matching `#pfand-amount`'s established
  convention.
- Percentage fields (`value_percent`, fee `percent`, `rate`/
  `takeawayRate`, `tax_rate_pct`) and quantity fields (`quantity`,
  `qty_before`, `qty_requested`) are **not** currency amounts, so they use
  a flat 2-decimal pattern instead — a currency-decimals branch would be
  wrong for these (a 0-decimal-currency shop still wants fractional tax
  rates and quantities).
- `quantity`/`qty_before`/`qty_requested` additionally allow a leading
  `-`: none had a `min=` attribute originally, and
  `internal/pages/inventory_api.go`'s `Quantity` field is documented
  "positive for receive, +/- for adjust" — narrowing the pattern would
  have silently regressed real adjustment/override entry. Every other
  converted field had an explicit `min="0"`/`min="0.01"` and correctly
  drops native min/max validation (accepted regression, same as
  ut-docs#1249's own F2).

New e2e spec `e2e/tests/osk-decimal-admin-fields-1275.spec.ts`: drives
`osk.js`'s real on-screen keys (`button[data-k]`, not `.fill()`/`.type()`)
into one representative field per file (9 cases), plus a dedicated
`#stock-cost` → `#stock-cost-minor` end-to-end submit test, plus a
regression guard that a real/external keyboard can still type a leading
`-` into `quantity` (osk.js's numeric layer has no `-` key at all —
ut-docs#1276, a separate open card — so that path isn't reachable via the
OSK today).

## Verification performed

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` | empty / pass |
| `go test ./...` (full suite) | pass, every package `ok` |
| All 29 CI-blocking guards in `ci.yml`'s `build` job | 29/29 pass (data-access, migration-version-collision, kiosk-engine, plugin-menu-read, i18n + i18n_toast, compliance-claims, docs-shots + its own `_test`/cross-check, help-topics, webkit-version, kiosk-launch-flags, android-status-address, android-i18n, emoji-font, htmx-loaded, autofill-suppression, osk-loaded, check-brand-assets, makefile-version) |
| `make docs-shots` (surface hash changed — these are routed, manual-linked pages) | regenerated (twice — once for the type/pattern fix, again after the S2 `size` adjustment below), `guard-docs-shots.sh` passes |
| New e2e spec (11 cases) | 11/11 pass |
| OSK/autofill/shifts/tips/deposit sibling subset (54 specs) | 54/54 pass |
| Full e2e `default` project (independent reviewer run) | 216 passed, 1 failed (`split-tender-i18n-925.spec.ts`, fa/RTL) — re-run in isolation: passes. Same pre-existing ordering flake `docs/code-reviews/2026-08-29-shifts-tips-osk-money-fields-1272.md` already documents; touches none of the five changed files |

### TDD claim, verified twice (Dev, then independently by Reviewer)

All 9 OSK-driven corruption-check cases fail against the unfixed
(`type="number"`) markup with exactly the diagnosed corruption pattern
(e.g. typing `"8.25"` produces a final value of `"25"`, `"7.50"` produces
`"50"` — the leading digit(s) before the decimal point are lost, not the
whole value). Both the implementer and the independent reviewer reverted
only the five production `.html` files, killed the till server first
(templates are `go:embed`ed, so a reused server would false-pass),
re-ran, confirmed red, restored, re-ran, confirmed green.

## Findings and disposition

1. **S1 (should-fix, fixed) — `web/public/app.css`'s `.fee-row
   input[type="number"]` rule silently stopped matching the two fee
   fields once they became `type="text"`, regressing a 2026-08-01
   independent review's own fix** (the wider input track + reduced
   padding that keeps "100.00%" from clipping at any UI scale). Measured
   live: the safety margin dropped from 14.5px to 5.6px at `ui_scale 1`
   (still not clipping today, but a wider locale font would reinstate the
   original bug). **Fixed**: selector now also matches
   `input[type="text"]`.
2. **S2 (should-fix, fixed) — `country_settings.html`'s `tax_rate_pct`
   row input carried a `size="5"` that was always present but inert
   under `type="number"` (`size` only applies to text-like inputs).**
   Now live, it narrowed the table column enough to wrap the "tax
   included in price" checkbox label onto 3 lines. Measured the actual
   trade-off before picking a fix: a fully single-line label needs
   `size>=12` (~165px), which re-widens the column enough to re-clip the
   Archive Retention column off the card's edge — the exact clipping this
   diff's narrower column incidentally fixed. **Fixed**: bumped to
   `size="7"` (down to a 2-line wrap, not 1, but keeping the column
   narrower than before) — a deliberate middle ground, documented inline
   in the template, not a full resolution of the tension between the two
   goals. A follow-up UX pass could resolve this properly (e.g. give the
   checkbox label its own table cell, or a fixed-width column via CSS
   instead of relying on `size`) — not filed as a separate card since it's
   cosmetic and the current state is a net improvement over both the
   pre-fix (3-line-wrap-free but column-clipping) and the naive-fix
   (3-line-wrap) states.
3. **N1 (nice-to-have, fixed) — spec comment inaccuracy.** The file
   header said "none of these fields copy into a separate hidden field
   via onchange" — true for the *bug this card fixes* (the #1249-class
   always-empty path), but `#stock-cost` does have a hidden companion
   (`#stock-cost-minor`), just synced on the form's `submit` event, not
   `onchange`. Reworded to state the mechanism precisely rather than
   imply no hidden field exists at all.
4. **N2 (nice-to-have, fixed) — the `#stock-cost` case only asserted the
   visible field's value, not `#stock-cost-minor`.** Added a dedicated
   end-to-end test that types via the OSK, submits the real stock-receipt
   form, and asserts the hidden minor-units field syncs correctly —
   matching how ut-docs#1249/#1272's own specs asserted the POSTed field.
5. **N3 (informational, accepted, not fixed here) — the card title's
   "Remaining" undersells what's still out there.** A repo-wide grep
   after this fix finds more decimal-accepting `type="number"` fields
   outside these five files, same root cause: `index.html`'s split-tender
   `amount`/`change` (the **sale screen** — the highest-OSK-exposure
   surface in the product), `basket.html`'s `qty-input`, and
   `catalog.html`/`catalog_variants.html`'s price/cost/modifier fields
   (some of which also feed a hidden minor-units field, so may be
   #1249-class too, not just decimal-corruption). None were tracked by
   any open issue. **Filed as ut-docs#1284**, split so the two
   sale-screen fields can get `p1` treatment given their exposure — out
   of this card's own explicitly-scoped five files.
6. No missed fields within the five files' own scope — a repo-wide grep
   confirms every decimal-accepting `type="number"` field in
   `promotions.html`/`settings.html`/`inventory.html`/`tax_codes.html`/
   `country_settings.html` was converted, and `archive_min_days`
   (integer-only) was correctly left alone.

## Checked and found clean

- Server-side is a genuine no-op for every field: all five handlers read
  the raw posted string via `strconv.ParseFloat`
  (`promotions_page.go:83,89`, `settings_page.go:482-486`,
  `inventory_api.go:68-71,243-247`, `tax_codes_page.go:105,141,145`,
  `country_settings_page.go:183,255`) and were never coupled to the input
  type; every server-rendered prefill is already dot-decimal
  (`fmt.Sprintf("%.2f", …)`/`taxrate.FormatPercent`), so it round-trips
  through the new `pattern` unchanged.
- `osk.js`/`autofill.js` compatibility confirmed by reading the source,
  not assumed: `wantsOSK()` accepts `type="text"`; `isNumeric()` matches
  `inputmode="decimal"` via the saved `oskPrevInputmode`, so the numeric
  keyboard layer still opens; `insert()` now takes the cursor-aware
  `setRangeText` path it explicitly skips for `number`/`email`.
  `autofill.js`'s `TEXTY_TYPES` already lists `text` and `number`
  identically.
- Dropped `min`/`max` degrades to a *visible* server error on every one
  of these five pages (promotions' `{{ if .errKey }}`, tax_codes'
  `htmx:responseError` handler, settings' `✗ range` message) — not a
  silent swallow like the unrelated ut-docs#1273 gap.
- The `×100`-hardcoded money conversions this diff's fields feed
  (`inventory.html`'s `stock-cost` JS sync, `promotions_page.go`,
  `settings_page.go`'s fee handler) are a real, separate currency-
  awareness gap — already filed as **ut-docs#1282**, not introduced or
  worsened by this diff (this diff only changes what can be *typed*, not
  what happens to the value afterward).
- No SQL, no `internal/` repository-pattern concern, no self-order/kiosk
  `Engine` touch, no new i18n keys (new strings are HTML comments,
  exempt), no compliance wording, no RTL/logical-property concern (no new
  CSS layout beyond the `.fee-row` selector fix, which changes nothing
  positionally), no real shop/client names, no secret-shaped literals.
- No `web/help/` manual prose describes these fields' input mechanics, so
  nothing went stale; `guard-help-topics.sh` green. Screenshots
  regenerated for the two routed pages this diff visibly changes
  (`country-settings.png`, all locales — real content change, explained
  by the S2 `size` fix); other pages' tiny byte-level screenshot diffs
  are established harness noise, not a content change.
- The spec restores OSK mode in `afterEach` per `helpers.ts`'s documented
  convention, so it can't leak `osk=on` into later specs on a failure.

## Follow-up cards filed

- ut-docs#1282 — `promotions`/`settings`/`inventory` money conversions
  hardcode `×100`, wrong on 0-decimal currencies (found alongside this
  card, out of scope, same class as ut-docs#1274).
- ut-docs#1283 — the main e2e suite (`playwright.config.ts`) has no
  pre-installed-Chromium fallback for cloud sessions, unlike
  `playwright.docs.config.ts` (infra gap found while verifying this
  card's e2e coverage in a cloud pipeline session).
- ut-docs#1284 — the identical OSK decimal-corruption root cause is still
  live on the sale screen (`index.html`'s split-tender fields,
  `basket.html`'s quantity input) and catalog/variant price fields —
  found by the independent reviewer's own repo-wide grep, outside this
  card's five-file scope.
