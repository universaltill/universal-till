# Fix `/fiscal-register` per-location address form overflow at 360px (ut-docs#2420)

**Date:** 2026-09-18
**Card:** ut-docs#2420
**Complexity:** easy — Sonnet dev (inline), fresh-context Sonnet review
**Lane:** `lane:cloud-24`

## What shipped

- `web/public/app.css`: `.fiscal-register-group .users-inline { flex-wrap: wrap; }`
  scoped under `@media (max-width: 480px)`. The per-location address-edit
  form (`web/ui/pages/fiscal_register.html`'s `.users-inline` — Street/
  Postcode/City inputs + a submit button) has three fixed-width (`7.5rem`)
  inputs with no ability to shrink or wrap, so once a location's group also
  renders its 11-column table with at least one row, the whole document
  overflows horizontally by ~184px at the 360px kiosk-floor viewport
  (`universal-till/CLAUDE.md`'s non-negotiable). An empty group (heading +
  address form, no table rows) never reaches this.
- Deliberately **scoped to this page's own group**, not the shared
  `.users-inline` class: a bare `flex-wrap: wrap` on `.users-inline` itself
  was already tried and reverted (ut-docs#898) because it makes `/users`'
  unrelated role-select + "Change role" row wrap even at full desktop
  width. `.fiscal-register-group` exists nowhere outside this page, so the
  new selector can't reach `/users`.
- `e2e/tests/fiscal-register-address-form-overflow-2420.spec.ts` (new):
  real-browser Playwright regression test reproducing the issue's exact
  repro (create location → register on that location → fiscal entry →
  360x800 viewport), asserting both `document.documentElement.scrollWidth`
  and the address form's own `getBoundingClientRect().width` stay within
  the viewport. A second test pins `/users`' inline forms stay
  `flex-wrap: nowrap` at desktop width (the ut-docs#898 regression guard).
- `web/help/img/manifest.json`: `surface_sha256` field only, via
  `scripts/ci/update-docs-shots-surface-hash.sh` — the docs-shots harness
  captures at a fixed 1024x600 viewport (`e2e/playwright.docs.config.ts`),
  well above the new rule's `max-width: 480px` gate, so no screenshotted
  topic's rendered pixels change. `Docs-Shots-Unchanged: true` trailer on
  the commit per that script's own convention.

## Evidence the test is a real regression guard, not a tautology

Ran the new spec with the CSS fix applied (2 pass), then temporarily
reverted only `web/public/app.css` (`git stash`) and re-ran: the first
test failed with `document is 184px wider than the viewport
(scrollWidth=544, clientWidth=360)` — matching the issue's own measured
184px exactly. Restored the fix afterward; both tests pass again. Also
ran the full pre-existing `fiscal-register-record-dialog-2186.spec.ts`
suite (8 tests, including its own 360px viewport case) — all still pass,
no regression.

## Independent review (fresh-context Sonnet)

No must-fix findings. Checked and confirmed:

- CSS robustness: `.users-inline` is `inline-flex` with three `<label>`
  elements (short i18n text + a 120px input) plus one button as flex
  items — `flex-wrap: wrap` wraps per flex item, not per input, and every
  locale's `fiscalregister.address.*` strings (en/ar/fa/tr) are short
  enough that no single line risks exceeding 360px minus the `.card`
  padding. `getBoundingClientRect()` on the wrapped flex container is a
  real, non-coincidental measurement.
- Test realism: goes through the real create-location/register/entry
  flow via this page's own existing helpers (matching the sibling
  #2186 spec's shape), asserts the row/form are visible before measuring,
  and checks both document- and element-level width so a false pass via
  ancestor clipping is ruled out.
- Cascade/selector conflicts: none — grepped every `.users-inline`
  occurrence in `app.css`; only `input`/`input.rename-input` width rules
  exist elsewhere, no other rule sets `flex-wrap`, and no fiscal-register
  address input carries `.rename-input`.
- Scoping approach: correct given the ut-docs#898 constraint.
- Manifest escape-hatch usage: correct — confirmed the 1024x600 viewport
  hardcode independently, confirmed only `surface_sha256` changed.

## Gate

`go build ./...` (no Go files touched), `gofmt -l .`, `go vet ./...`,
`bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-docs-shots.sh` —
all green. New spec + full `fiscal-register-record-dialog-2186.spec.ts`
suite run against a real Chromium/Go server — all pass.

## Verdict

Safe to merge. Nothing deferred.
