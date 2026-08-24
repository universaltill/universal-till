# Code review: Settings barcode symbology checklist (ut-docs#935)

- **Date**: 2026-08-24
- **Card**: universaltill/ut-docs#935
- **Design**: ADR-0059 Decision §2 (registry + scan-path wiring, Decisions §1/§3,
  were already built by #933/#934 — this card is the settings UI only)
- **Complexity**: medium (Dev at Sonnet inline, Review at Opus subagent per
  the scrum-master skill's model-routing rule)

## What shipped

A Settings page checklist (`web/ui/pages/settings.html`) listing every
`internal/barcode` registry entry (EAN-13, EAN-8, UPC-A, UPC-E, GTIN-14,
Code 128, Code 39, internal/PLU, and the two embedded-data scale-label
entries). Each checkbox toggles immediately — no Save button — through a
new `POST /api/settings/barcode-symbology` handler
(`internal/pages/settings_page.go`), manager/elevation-gated the same way
as every other mutating handler on this page.

Storage: `SettingsRepo.SetBarcodeSymbologyEnabled` (new,
`internal/data/barcode_settings.go`) does the read-modify-write inside one
DB transaction and refuses to leave the shop's enabled set empty. The
pre-existing `EnabledBarcodeSymbologies`/`SetEnabledBarcodeSymbologies`
accessors (built by #933/#934) are unchanged and still used by the scan
path, `AddBarcode`, and catalog import.

i18n: `barcode.symbology.*` (one key per registry entry, already named by
the registry's own `NameKey` field), `settings.barcode.*` (section
title/help), and `elevation.summary.barcode_symbology_on/off`, added to
all four locales (en/ar/fa/tr) with real translations. Manual:
`web/help/{en,ar,fa,tr}/catalog.md` gained a paragraph on the new setting,
and `display.md` (the topic that actually claims `/settings`)
cross-references it. Screenshots regenerated via `make docs-shots`.

## Independent review

Spawned an `Agent` (`general-purpose`, model `opus`, `isolation: "worktree"`)
against a WIP commit of the diff — a genuinely different model from the
Sonnet session that wrote the code, per the reviewer skill's routing rule
for `complexity:medium`. Full findings text: see the agent's report in this
cycle's session; summarized below with what was fixed.

### Blockers found and fixed

1. **`this` unbound in a `js:` hx-vals expression — the checkbox never
   actually posted anything in a real browser.** htmx evaluates a `js:`
   hx-vals as a bare `Function(...)()` call with no `this` binding, so
   `this` inside it was `window`, and `window.getAttribute` threw
   (uncaught) before the POST ever fired. Go-level handler tests couldn't
   catch this — they hit the endpoint directly, never the browser-side
   wiring. This is exactly the trap `ut-docs#865` review finding F3
   already fixed on the sibling `launch-on-startup` checkbox four lines
   above in the same file; the fix here follows that same precedent —
   `document.getElementById('bcsym-<ID>')` with a real, stable per-checkbox
   id, instead of `this`.
2. **`guard-docs-shots.sh` was red** — `make docs-shots` had been run
   against an intermediate edit of `settings.html`/`settings_page.go`,
   before the `set-row` → plain-`<label>` layout fix (see below) landed.
   Re-ran it as the actual last step before this commit.

### Major findings found and fixed

3. **A shop could untick every symbology and silently kill all scanning.**
   Nothing stopped disabling the last enabled entry; every scan and every
   untyped `AddBarcode` call would then fail to match anything, with no
   explanation anywhere. `SetBarcodeSymbologyEnabled` now refuses (400,
   `ErrEmptyBarcodeSymbologySet`) rather than let the set go empty.
4. **Non-atomic read-modify-write — two toggles issued close together
   could lose one another.** The original handler did a separate
   `EnabledBarcodeSymbologies` read then a separate
   `SetEnabledBarcodeSymbologies` full-list write; two concurrent toggles
   (plausible with ten independent checkboxes and no `hx-sync` between
   them) could both read the same starting list and the second write would
   silently discard the first. `SetBarcodeSymbologyEnabled` now does the
   whole thing inside one transaction. Covered by
   `TestSetBarcodeSymbologyEnabled_ConcurrentTogglesDoNotLoseEachOther`
   (two goroutines toggling different ids concurrently; asserts both land).

### Minor findings folded in

5. The elevation summary now uses the looked-up `Symbology.NameKey`
   instead of rebuilding `"barcode.symbology."+id` by hand (only worked
   before because every current entry's `NameKey` happens to follow that
   convention).
6. Added `TestBarcodeSymbologyNameKeysResolveInEveryLocale` — the
   checklist's `{{ T .NameKey }}` is a *dynamic* template call, invisible
   to `guard-i18n.sh`'s literal-key scan; this test is the guard the
   static scanner can't be.
7. Cross-referenced the new setting from `display.md` (all 4 locales) —
   the topic that actually claims the `/settings` route — so a shop owner
   reading Settings end-to-end finds it; the card's own `?` link still
   points at `catalog` (the more topically relevant place for the prose),
   permitted by the "section inside an already-claimed page gets an
   explicit helpLink" rule.

### Not changed (reviewed, accepted as-is)

- **NIT**: `package data` used, then locally shadowed by `data :=
  map[string]any{...}` later in the same GET handler. Pre-existing
  convention in this exact function (other `data.New*Repo(...)` calls
  already precede the shadow) — left as-is rather than diverging from the
  surrounding code's own style.
- **MINOR**: a cancelled elevation prompt leaves a checkbox showing an
  unsaved state until reload. Pre-existing behaviour on the sibling
  `launch-on-startup` checkbox this pattern was copied from — not a
  regression, not fixed here.

## Verified beyond automated tests

- **Real browser click-through** (Chromium via Playwright, driven
  manually against a live `go run .` instance): clicking a checkbox fires
  the POST, gets a 204, and the checked state survives a full page reload
  — confirms the BLOCKER 1 fix actually works outside the Go test harness.
- **Empty-set rejection in the browser**: unticking all 8 default-on
  entries one at a time — the first 7 each return 204, the 8th (the last
  remaining) returns 400, the checkbox visibly reverts to checked, and the
  `#settings-save-error` banner becomes visible.
- **Visual check, both LTR and RTL**: rendered screenshots in en, fa, ar,
  tr. Confirmed no left/right-literal RTL defects, and specifically
  confirmed the checkbox-detaches-from-wrapping-label bug this diff itself
  found and fixed (`class="set-row"`, a flex row, was stranding the
  checkbox on its own line above a long wrapping translation — switched to
  a plain `<label style="display:block">`, matching the
  `settings.printer.auto`/`settings.telemetry.enable` precedent already in
  this file). Did not separately screenshot the dark theme — the new
  markup introduces no new colors (reuses `.card`/`.muted` only), so risk
  there is judged negligible, not verified pixel-for-pixel.

## Gate

`gofmt -l .` clean; `go build ./...` clean; `go vet ./...` clean;
`go test ./...` full suite green (all packages); `-race` run clean on
`internal/pages`/`internal/barcode` (the packages this change touches —
`internal/data`'s own full suite under `-race` times out at the default
10-minute test timeout in this sandbox regardless of this change, a
pre-existing environment characteristic, not a regression: the same
package passes cleanly without `-race` in 72s). Guards:
`guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
`guard-compliance-claims.sh`, `guard-docs-shots.sh` all pass.

## Verdict

Safe to merge. No client/shop name used anywhere as demo/seed/test data;
no secret-shaped literal introduced.
