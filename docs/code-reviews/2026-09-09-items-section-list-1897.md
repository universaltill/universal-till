# Code review: Items section-list page (ut-docs#1897)

**Date:** 2026-09-09
**Card:** ut-docs#1897 — "Items area has no section structure — SumUp's
Library/Categories/Inventory/Modifiers/Option sets vs our one flat Catalog
page"
**Branch:** `feat/1897-items-section-list`
**Diff:** `internal/pages/items_page.go` (new), `internal/pages/items_page_test.go`
(new), `web/ui/pages/items.html` (new), `internal/pages/init.go`,
`internal/pages/menu_page.go`, `web/help/{en,fa,ar,tr}/catalog.md`,
`web/locales/{en,fa,ar,tr}.json`
**Reviewer:** independent (Opus, fresh context, own isolated worktree, did
not write the code)

## What shipped

The flat top-level "Catalog" nav tile and the separate "Inventory" tile are
replaced by one "Items" tile opening `/items` — a section-list landing page
mirroring SumUp's Items area (ut-docs#1830, captured on the pilot tablet):
each entry is a bold name plus a one-line subtitle. Five sections: Library
(→ `/catalog`, unchanged route/handler), Categories (disabled "coming
soon" — its real screen is ut-docs#1898, deliberately not built here),
Inventory (→ `/inventory`, unchanged), Modifiers (disabled "coming soon" —
ut-docs#1899, not built here), Option sets (→ `/catalog`, since the
per-item variants panel already lives there and the reusable-set
generator, ut-docs#1900, hasn't shipped). `nav.catalog`/`nav.inventory`
keys are untouched — still used directly by `catalog.html`/`inventory.html`/
`import.html`/`tax_codes.html`. The shared nav rail's own separate
`/inventory` one-tap shortcut (`nav.html`, ut-docs#1332/#1349) is a
different UI surface, left alone. New `nav.items`/`items.*` keys were
translated directly into all four core-shipped locales (en/fa/ar/tr) —
confirmed by review as this repo's actual convention for these four files
(`git log` shows ordinary feature commits editing them directly); the
external `ut-plugin-language-{de,es}` NAS mechanism only ever covers those
two packs, not these.

## Independent review — one CI blocker and two UX issues found and fixed

**Blocker: `guard-help-topics.sh` failed.** `/items` was a new registered
route claimed by no manual topic's `routes:` front matter. Fixed by adding
`/items` to `catalog.md`'s existing `routes: [/catalog, /import]` (now
`[/catalog, /import, /items]`) in all four locales — that topic's content
(products, variants, barcodes) is what the Library section leads to, and
this follows the same multi-route-per-topic pattern `catalog.md` already
used for `/import`. `make docs-shots` re-run after the front-matter change
(screenshot count unchanged at 26 topics — `/items` piggybacks on the
existing `catalog` topic rather than becoming a 27th).

**Two different English names for the same destination.** The Library
section was named "Library" while `/catalog`'s own `<h1>` (unchanged, per
scope) still reads "Catalog" — an operator tapping Library would land on a
screen that names itself something else, the exact disorientation this
card exists to fix. Fixed by having the Library row reuse the `nav.catalog`
key directly instead of a separate `items.library.name` key (removed from
all four locales, now unused).

**"Option sets" implied a dedicated screen that doesn't exist.** Tapping it
opens `/catalog` — the flat product list, with no visible "option sets"
anything, unlike the honest "Coming soon" rows for Categories/Modifiers.
Fixed by rewording the subtitle from "Quickly create item variations" to
"Open an item to edit its variations" (translated equivalently in fa/ar/tr)
— honest about what the link actually shows today, while keeping it live
rather than disabling a feature that IS real (the per-item variants panel).

### Other findings addressed

- **Layout: `.settings-grid`'s multi-column reflow** made the five rows
  wrap into two columns at till width instead of SumUp's single vertical
  stack the card asked for — row order became non-obvious. Replaced with a
  plain single-column flex stack.
- **Accessibility/contrast:** `opacity:.6` on a disabled card's whole
  content stacked a second contrast reduction on top of `.muted`'s already-
  reduced subtitle text — a real WCAG AA risk on a daylight till. Removed;
  the "Coming soon" `.tag` (real text, announced normally to assistive
  tech) is what actually carries the disabled meaning — `aria-disabled` on
  a plain `<div>` with no widget role was inert markup anyway, not the
  actual signal.
- **Cosmetic:** added the `.page-head` wrapper around the `<h1>` that every
  sibling page (`menu.html`, `catalog.html`, `inventory.html`,
  `reports.html`) already uses.

### Findings reviewed and not changed (with reasoning)

- **`"title": "Items"` hardcoded English** for the browser tab, instead of
  resolving `items.title`. Confirmed as this repo's existing pattern
  (`designer_page.go` hardcodes `"Quick Buttons"` the same way) — consistent
  with, not a deviation from, how every other page in this file already
  does it. Left as-is.
- **`lang-pack-drift` will flag the 13 new locale keys** as missing from
  `ut-plugin-language-{de,es}` — advisory on this PR, non-blocking (only
  blocking on push to `main`, per this ecosystem's standing rule). Tracked
  as a follow-up rather than blocking this PR on it.

## Verified beyond automated tests

`go build`/`go vet`/`gofmt -l` clean. Full `go test ./...` green (every
package, not just `internal/pages`). `guard-i18n.sh`, `guard-data-access.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh` all clean after the fixes.
Independent review mutation-tested the new tests (reverting `baseMenu`,
giving Categories a live `Href`) and confirmed each fails for the right
reason. A real driven run (`go run .` against a scratch SQLite DB, `curl`
against the live server) confirmed: `/items` renders all five sections with
the corrected "Catalog" name and honest Option-sets subtitle, exactly two
disabled rows with no opacity on their text, the `page-head` wrapper
present, and `/catalog`'s own `<h1>` reading "Catalog" — matching the
Library section's name.
