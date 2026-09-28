# Review: Settings grouped into icon categories (ut-docs#3090)

**Date:** 2026-09-28 · Built by Opus 5.5 (`lane:cloud-54`) · Reviewed by Fable (independent, detached worktree)

## What shipped

`/settings` now opens on a grid of large touch tiles, one per category, named the way a shop owner thinks: My shop, Selling, Payments, Receipts & printers, Staff & security, Tills & devices, Look & feel, Backup & updates, and Advanced (always last). Each tile has an icon and a one-line description.

- A tile (`#cat-<id>`) opens the existing two-pane view (#1960). The sidebar lists only that category's sections, under the category's name and a **Back to categories** link.
- **Advanced** (`#settings-advanced`) lists every section under category headings. The technical sections live only here: report an issue, diagnostics, barcode types, catalogue-import default, data, retention, telemetry and the raw settings list. Nothing is lost (AC 3).
- A deep link to a card or a nested id (`#registration`, `#android-update`) opens the card inside its own category, or in Advanced if it has none.
- The search box on the grid searches every setting.
- A category whose every section is gated out for this render gets no tile.
- It is still one server render with show/hide only (ADR-0008). A browser without script gets the old full page.

**Design.**
- `uislot.SettingsCategories` is a new registry. Every `CoreSettings` entry's `Group` is its category's label key: a category *is* a Group (AC 8). So a `layout` plugin's Group amendment moves a section to another category, and a plugin's own group becomes its own tile, `g-<id>`, shown just before Advanced.
- `settingsnav.Resolve` stable-sorts rows by category rank. A plugin's `order` amendment therefore reorders only within a category: the salon layout's Theme now leads Look & feel, and its "Hardware" group (Printer + Tills) is its own tile.
- `settingsnav.Categories` builds the tiles from the rows that survived `filterSettingsNavForRender`.
- Template order of the cards is unchanged.
- Two new built-in icons: lucide `store` and `credit-card`.

**Also changed:**
- New locale keys `settings.cat.*.label|desc` and `settings.home.label|back` in en/ar/fa/tr. The de/es pack PRs follow the core merge.
- `web/help/*/display.md` (the topic claiming `/settings`) gains a paragraph on the grid, and step 8's salon description is updated.
- The `display.png` screenshots are regenerated for every locale.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | At phone width, a stale `has-selection` made a category open on its *panel* after Back → another tile. | **Fixed.** `enterView()` and the no-hash branch clear it. The e2e phone test now does Back → tile and asserts the list shows. Mutation (both clears removed) fails the test. |
| 2 | major (a11y) | Focus was lost to `<body>` after Enter on a tile. | **Fixed.** Focus moves to the category title (`tabindex=-1`, focus-visible ring). New e2e keyboard test; it fails with the focus line removed. |
| 3 | minor | The grid search used `replaceState`, so Back left `/settings`. | **Fixed:** `pushState`. The e2e search test asserts Back returns to the grid. |
| 4 | minor | "← All settings" read like the Advanced tile's "Every setting", and the literal ← did not mirror in RTL. | **Fixed.** New key `settings.home.back` = "Back to categories", used in the help text too. The arrow is a CSS `::before` that flips under `[dir=rtl]`. |
| 5 | minor | `TestSettingsPage_NoTileForAnEmptyCategory` could not fail. Its premise (no Payments card on a fresh till) was false, and a reorder assertion could pass vacuously. | **Fixed.** The test turns every payment method off, asserts the premise, and asserts no tile. A mutation feeding the tiles unfiltered rows fails it. The vacuous loop now fails if no Look & feel row exists. |
| 6 | minor | The plugin group id sanitiser could merge two groups (case, dropped characters). | **Fixed.** It lowercases first and adds a numeric suffix on collision. New test `TestResolve_PluginGroupIDsNeverCollide`. |
| 7 | nit | Stale comments (the settingsnav package doc, the settings_page comment, the script header), an unreachable branch, and an unused `Category.Count`. | **Fixed:** comments updated, branch and field removed. |
| 8 | nit | Possible first-paint flash of the two-pane view on a slow kiosk. | **Fixed.** A one-line inline script right after the shell's opening tag swaps to the grid before the ~2,000 lines of cards are parsed. |

Accepted as designed: a plugin `order` amendment can no longer move a section across categories. Categories keep their fixed order, which is the point of the card. The salon plugin's manifest description ("puts Theme first") is now true only within Look & feel. The manifest is left untouched (a built-in signed plugin); `ut-docs/reference/plugin-manifest.md` documents the settings-slot semantics.

## Verified beyond unit tests

- `go build ./...`, `go test ./...` (all pass), `golangci-lint run ./...` (0 issues), `gofmt -l` clean.
- Every guard in `ci.yml`'s build job passes. The exception is `guard-shellcheck-version.sh`, which fails only because this container has no shellcheck; no shell script changed.
- Playwright: the new `settings-categories-3090.spec.ts` (8 tests) and the updated `settings-two-pane-1960.spec.ts`. Every spec that visits `/settings`, plus the auth and layout projects (the salon layout), ran green: 277 passed.
- **Screenshots looked at:**
  - The grid at 1024×600: 4 columns, touch-sized tiles. Advanced sits below the fold on the kiosk floor and scrolls.
  - The grid at 360 px: one column, no horizontal scroll.
  - fa (RTL): mirrored, and the sidebar sits on the right.
  - The Look & feel category view at 1024×600.

  Emulated in Chromium only, not on real touch hardware; nothing here uses a pointer/drag handler.
- German-length check: the de pack strings ("Personal & Sicherheit", "Backups & Updates") are no longer than the longest English tile ("Receipts & printers" wraps to two lines, which the tile handles).

## Language packs

The de/es strings got a second Fable review. It found the rename to `settings.home.back`, "Belegdrucker"/"Backups" to match each pack's own section names, the formal register in Spanish, and "fichas" as that pack's word for menu tiles. All are fixed in the pack PRs, and the German help paragraph now says "Backups & Updates".

## Deferred (follow-up cards)

- Per-category permissions beyond today's per-card gates (no such permission exists yet).
- Users and PINs inside Staff & security (they live in ☰ Menu today).
- Simplifying the contents of individual cards.

## Verdict

Safe to merge.
