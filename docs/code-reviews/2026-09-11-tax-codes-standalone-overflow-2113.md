# 2026-09-11 — Tax codes: standalone page horizontal overflow at phone width (ut-docs#2113)

## What shipped

`GET /catalog/tax-codes` (the standalone tax-codes page, reached directly
outside the `/items` shell) overflowed horizontally at 360px width: the
Active/Edit/Deactivate columns forced the table wider than its container,
and the whole document scrolled sideways to 521px in a 360px viewport.

Root cause: ut-docs#2095 added a horizontal-scroll safety net for the
dialog-embedded copy of the tax-codes table only
(`#tax-codes-modal #tax-codes-table { overflow-x: auto; }`), scoped to the
`/items` shell's `#tax-codes-modal` dialog. The identical
`tax_codes_table.html` partial, rendered directly on the standalone page
(`.catalog-list #tax-codes-table`), never matched that selector, so it had
no overflow safety net at all — pre-existing, unrelated to any change since
#2095, and flagged as a follow-up (this card) by that PR's own independent
review rather than fixed inline there.

- `web/public/app.css`: widened the selector from
  `#tax-codes-modal #tax-codes-table` to unscoped `#tax-codes-table`.
  `#tax-codes-table` is the id on the single `<div class="card">` wrapper
  defined once in `web/ui/partials/tax_codes_table.html` — a unique id per
  page render regardless of whether that partial is reached via the dialog
  or the standalone page — so one unscoped rule now covers both call
  sites. Comment updated to record the ut-docs#2113 follow-up and its
  measurement.
- `e2e/tests/catalog-tax-codes-standalone-overflow-2113.spec.ts`: new
  Playwright spec, loads `/catalog/tax-codes` at 360×800 and asserts (a)
  `document.documentElement.scrollWidth <= clientWidth`, and (b) the
  `#tax-codes-table` wrapper's own rendered width does not exceed the
  360px viewport (a second, independent check that the table is actually
  scrolling *inside* its wrapper, not just coincidentally not blowing out
  the document via some other route). Uses the default e2e seed's
  "Standard VAT" tax code — no extra seeding needed.
- `web/help/img/{en,ar,fa,tr}/tax-codes.png` + `web/help/img/manifest.json`:
  regenerated via `make docs-shots`. Every other topic's screenshot from
  that same regen run was reverted as unrelated anti-aliasing/rendering
  noise (same precedent as `universal-till` PR #1090's test plan) — only
  the tax-codes images and the manifest's `surface_sha256` (which covers
  the whole `web/public/**` + `web/ui/**` surface, so any `app.css` edit
  bumps it) are kept.

## Independent review

**Verdict: safe to merge — no blocking issues found.**

### What I verified myself

1. **Diff matches the description.** Read `git diff HEAD^ HEAD` in full:
   exactly `web/public/app.css` (one selector + comment), the new e2e spec,
   and the four tax-codes PNGs + `manifest.json`. No surprises.

2. **TDD re-verified for real**, by hand-reverting just the CSS selector
   back to `#tax-codes-modal #tax-codes-table { overflow-x: auto; }` in
   this worktree (not trusting the PR's own claim):
   - **Before (reverted):** the new spec fails —
     `document is 161px wider than the viewport (scrollWidth=521,
     clientWidth=360)`. Matches the original bug report's numbers
     (521px document width in a 360px viewport) exactly.
   - **After (restored, `git diff` clean against the committed state):**
     same spec passes, plus the wrapper-width assertion.
   This is genuine red-before-green, not an asserted claim.

3. **No regression on the `/items`-shell dialog rendering of the same
   table.** Ran the fixed tree against:
   - `catalog-tax-codes-standalone-overflow-2113.spec.ts` — 1/1 pass
   - `items-shell-catalog-import-taxcodes-dialog-2095.spec.ts` — 6/6 pass
   - `catalog-toprow-icons-2092.spec.ts` — 4/4 pass
   - `osk-decimal-admin-fields-1275.spec.ts` — 11/11 pass (includes the
     tax_codes.html rate/takeawayRate OSK-typing cases)

   22/22 total, run together against one live server.

4. **Full Go gate:** `gofmt -l .` (no output), `go build ./...` (clean),
   `go vet ./...` (clean), `golangci-lint run ./...` (0 issues),
   `go test ./internal/...` (all packages pass — no Go source changed by
   this diff, so this is a pure sanity check, not expected to move).

5. **CI guards run directly:** `guard-docs-shots.sh` (green — fresh
   manifest, surface hash matches), `guard-i18n.sh` (green),
   `guard-e2e-fixtures-import.sh` (green), `guard-compliance-claims.sh`
   (green), `guard-help-topics.sh` (green), `guard-help-drift.sh` (green —
   every locale/topic mismatch it printed is a pre-existing baselined
   entry under ut-docs#1962/#1973, none newly introduced here).

6. **CSS blast radius:** grepped the whole repo for `#tax-codes-modal` and
   `#tax-codes-table`. `id="tax-codes-table"` is defined exactly once
   (`web/ui/partials/tax_codes_table.html`), so widening the selector's
   scope cannot collide with any other element. `#tax-codes-modal` is a
   separate, empty `<dialog>` (`web/ui/pages/catalog.html`) that only ever
   gets the tax-codes partial swapped into it as `innerHTML` inside the
   `/items` shell — it is not itself styled by the changed rule, and
   nothing else in CSS/JS/e2e specs targets either selector in a way this
   widening could shadow.

7. **Manual/help topic:** `web/help/en/tax-codes.md` describes the page's
   functional steps (create/edit/deactivate/reactivate a code) — nothing
   about layout or scroll behaviour. This is a pure visual bug fix (the
   table now scrolls instead of overflowing the document at 360px) with
   no new/changed/removed capability, so no manual text edit is needed;
   confirmed rather than assumed. The regenerated screenshots are taken at
   the reference kiosk viewport (1024×600, `e2e/tests-docs/docs-shots.spec.ts`),
   not 360px, so they don't visually depict this fix either way — their
   diff is the unavoidable `make docs-shots` re-render, not new content.

8. **No secrets, no real client/shop names.** The e2e seed data used
   throughout ("Standard VAT", "Reduced VAT", "Zero-rated") is existing
   demo fixture data, unchanged by this diff.

9. **Read the CSS chain, not just the test result, for other overflow
   sources on this page:** `.catalog-layout` is `grid-template-columns:
   minmax(0, 1fr)` (correctly allows the track to shrink below its
   content's min-content width — the classic grid-overflow trap is
   *omitting* `minmax(0, …)`); `.catalog-list` is a plain block `<div>`
   with no competing width/flex rules; `.card` has no `min-width`;
   `table.table { width: 100% }` sizes to its containing block. With
   `overflow-x: auto` on `#tax-codes-table` (the `.card` wrapper directly
   containing the `<table>`), an over-wide table is clipped/scrolled
   within that wrapper rather than pushing the document wider — the same
   mechanism already used for `.users-list .table` and
   `.settings-grid .card .table` elsewhere in `app.css`, just applied to
   the wrapping `<div>` instead of the `<table>` itself (functionally
   equivalent here since the div already wraps nothing but the table).
   The e2e spec's second assertion (wrapper width ≤ 360px, independent of
   the document-level scrollWidth check) exists for exactly the scenario
   this point is checking for, and it also passes.

### Non-blocking / deferred notes

- None found specific to this diff. (The manifest's own `_comment` string
  says the `surface_sha256` fileset is "web/ui/** + non-test
  internal/pages/**.go" while the actual hashing code
  (`e2e/tests-docs/lib.js`) also includes `web/public/**` — a pre-existing
  wording gap in that comment string, not introduced or worsened by this
  diff, not worth a follow-up card on its own.)

## Safe-to-merge verdict

Safe to merge. Single-purpose CSS fix, genuinely TDD-verified (red before
green, numbers matching the original report), no regression on the
sibling dialog-embedded rendering, full Go gate and every relevant CI
guard green, no documentation gap, no blast-radius collision.
