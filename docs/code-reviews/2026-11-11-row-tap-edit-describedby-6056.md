# Review: row-tap name buttons describe themselves as "Edit" (ut-docs#6056)

Date: 2026-11-11 · Lane: `lane:cloud-41` · Branch: `feat/6056-row-tap-edit-describedby`
Author: Sonnet (Dev) · Reviewer: Opus 5.5, independent fresh context (worktree)

## What shipped

- `web/ui/pages/{categories,kitchen_stations,locations,promotions,country_settings,registers}.html`:
  one page-level
  `<span id="<page>-row-edit-hint" class="visually-hidden" hidden>{{ T "common.edit" }}</span>`
  and `aria-describedby="<page>-row-edit-hint"` on every row-tap name button
  (`.record-row-open` / `.category-row-open`, `data-record-edit`). The
  accessible name stays the record name (Voice Control "click <name>",
  WCAG 2.5.3); "Edit" is the description, as the old pencil said.
  - Country settings: the hint sits before `#country-settings-view`, outside
    the chip filter's `hx-swap="outerHTML"`, so a swap neither drops nor
    duplicates it.
  - Registers: the hint is in `content`, not `register_row`, so active and
    inactive rows share it.
  - Categories: the reorder hint stays on the `<table>`; the button keeps
    `aria-keyshortcuts`. The two are not merged.
  - `hidden` (UX's suggestion) keeps a stray "Edit" out of browse mode;
    `aria-describedby` still reads hidden text.
- `fiscal_register.html` is untouched: its button is a read-only View.
- Existing key `common.edit` reused; no new key, CSS or JS.
- Tests: `internal/pages/row_tap_edit_6046_test.go` gains a per-page
  `hintID`, `assertEditHint6056` (exactly one hint, text "Edit", outside the
  table, every name button points at it) and `TestRowTapEdit_Categories_6056`;
  `promotions_page_test.go`'s regex tolerates the new attribute.
- `web/help/img/manifest.json`: docs-shots surface hash refreshed; no PNG
  changed (`Docs-Shots-Unchanged: true`).

## TDD

Dev wrote the tests first and saw them fail (hint id found 0 times). The
reviewer re-verified this independently: with `aria-describedby` removed from
`locations.html`, `TestRowTapEdit_6046/locations` fails. With the file
restored it passes. The Tester also mutation-tested it: removing the
attribute from `categories.html`, or `hidden` from the registers hint, each
fails a test.

## Tester (real driven run)

The till binary was seeded with `test-6056-*` records. Chromium's
accessibility tree, read over CDP, gave every `[data-record-edit]` button on
all six pages the record name as its name and "Edit" as its description:
categories 10, kitchen stations 2, locations 5, promotions 2, country
settings 16, registers 1 active + 1 inactive.
- Each page renders exactly one hint.
- After the country chip swaps (mine → all → mine) the description is still
  there.
- In `fa` the page is right-to-left and the description reads "ویرایش".
- The fiscal register View button is still named "View".

Visual attestation: screenshots with and without the change are
pixel-identical on all six pages at 1024x600 and 360px (12/12). Locations
(en and fa) and registers at 360px were also checked by eye.

Not checked: real screen-reader audio.

## Findings

| # | Sev | Where | Finding | Resolution |
|---|---|---|---|---|
| L1 | Low | `row_tap_edit_6046_test.go` `assertEditHint6056` | Slices `body[i:]` before checking `i < 0`, so a missing table panics instead of failing. | **Fixed:** checks before slicing. |
| L2 | Low | e2e | No committed browser spec asserts the accessible description. | **Accepted.** The Go test pins the markup and the Tester verified the accessibility tree by hand. This container has no e2e packages installed. |
| L3 | Low | four templates | The new template comment was indented 4 spaces instead of 2. | **Fixed;** surface hash refreshed again. |
| L4 | Low | tests | The tests match an exact attribute order, which is brittle. | **Accepted;** the #6046 precedent uses the same style. |

The reviewer also checked these and found nothing: id collisions;
empty-list branches; HTMX partials that render rows without the hint (none
exist, and `register_row` is only used by `registers.html`); `hidden` with
`aria-describedby`; `os.MkdirAll` / `paths.Data` (no production Go code
changed).

Gate: `go build ./...` · `go test ./internal/pages/...` · full
`go test ./...`. `internal/plugins` hit the Dev agent's 600s timeout while
running under load; run alone it passed in 857s. Guards passed: i18n,
i18n-meta, no-showmodal, rtl-logical-css, help-topics/drift/breadcrumbs/terms,
fixed-dates, compliance-claims, competitor-naming and docs-shots.

Pre-flight: `web/help/` n/a (no visible change, no new behaviour to
describe) · `make docs-shots` n/a (surface hash only, no pixels) ·
`reference/list-and-dialog-pattern.md` updated on its open PR ut-docs#6047
(template and checklist now show the hint) · `README.md` n/a · `en.json` /
meta n/a (reused `common.edit`) · follow-ups none.

Verdict: **approve.**
