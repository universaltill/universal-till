# 2026-09-14 — Basket item name gets its own row at the 360px phone tier (ut-docs#1338, follow-up to ut-docs#1314)

## What shipped

ut-docs#1314 fixed the basket item-name column at the kiosk floor
(1024×600) and default till (1280×800) by reserving qty/price/total/
remove as explicit rem widths and leaving the item column width-less, so
`table-layout: fixed` gives it all the leftover space. At the 360px phone
tier that leftover space measured ~20px (~2 characters) — declared out of
scope there and filed as this card.

`app.css`'s own comment above `.basket table` already documents four
prior failed attempts at "give ITEM more room" via column-budget tweaks.
Rather than a fifth shave of the same shape, this tier stops relying on
that budget entirely: each basket line becomes a 2-row CSS grid — the
name spans the full card width on its own row, and qty/price/total/
remove share a second row below, sized from the *same* reserved rem
widths the ≥481px table already declares (single source, never re-tuned
separately). `table`/`thead`/`tbody`/`tr`/`td` lose their table-ish
`display` so the table column algorithm that was starving `item` never
runs at this width; `grid-template-areas` replaces it with an explicit
shape. The header row goes through the identical grid, so Qty/Price/
Total stay labelled above each line's own second row rather than reading
as two unlabelled adjacent numbers.

## Verified beyond automated tests

- **The TDD claim was independently re-verified**, not taken on the
  implementer's word: reverting `app.css`'s change (server rebuilt — the
  CSS is embedded, a disk edit alone isn't enough) makes the new spec
  fail on the real symptom: `scrollWidth 24 vs clientWidth 20` on the
  German compound name. Restored → passes again. Measured live at
  360×740, ui_scale 1: item cell 31.6px → 288.3px (after review fixes),
  name 19.8px → 156.3px. At ui_scale 2 the baseline name is literally
  0px; after the fix, 312.5px unclipped.
- **The four referenced regression specs actually ran and passed**, in a
  combined targeted run against the finished diff: `basket-item-name-
  width-1314`, `basket-no-horizontal-scroll-391` (including its full
  ui_scale×viewport matrix and RTL case), `sale-screen-213` (including
  the kiosk-mode floor), `phone-width-layout-413`. **47/47 passed**
  (1.0m), run twice independently (once by the review subagent in its
  isolated worktree, once by the orchestrator after pulling the review's
  fixes back into the working checkout).
- **RTL measured live**, not assumed: at 360px `?lang=fa` the grid's
  inline direction mirrors correctly — the name cell starts at the
  visual right edge, qty→price→total→remove run right-to-left,
  `overflowX`/`docOverflowX` both 0. No physical `left`/`right` in the
  new block; the file's existing logical-property convention holds.
  `basket-no-horizontal-scroll-391`'s own RTL test passed.
- **Accessibility checked, not guessed at**: contrary to the usual
  expectation for a `display:grid`-on-`<tr>` override, Chromium's
  accessibility tree still reports `table > rowgroup > row >
  columnheader/cell` through the override, in LTR, RTL, and the
  empty-basket state. There is direct precedent for this exact pattern
  already in this file (`#open-orders-table`, ~line 2619), also with no
  explicit ARIA backstop. WebKit's behavior here (historically more
  willing to drop table roles on a blockified table) is **not verified**
  — only Chromium is installed in this sandbox — and is called out below
  under "Not verified" rather than assumed fine.
- **All relevant CI guards run and green**, independently re-run after
  pulling the review's fixes: `guard-docs-shots.sh` (see F1 below),
  `guard-i18n.sh`, `guard-e2e-fixtures-import.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` (pre-existing tracked drift only, unrelated to
  this diff), `guard-htmx-loaded`, `guard-emoji-font`,
  `guard-autofill-suppression`, `guard-compliance-claims`.
- **Full-suite noise investigated, not waved away.** A `--project=default`
  run of the entire 132-file suite against this diff showed 480 passed /
  20 failed, spanning entirely unrelated areas (vouchers, tab-bar,
  hold-modal, OSK, forms) plus the two basket tests below. The same full
  run against the clean parent commit (no diff at all) showed **more**
  failures (411 passed / 85 failed) — including a totally unrelated
  early failure (`bugreport-panel.spec.ts`) that plausibly cascaded
  through much of the rest of that run, given this suite's single
  shared live-server architecture (`fixtures.ts`'s own documented
  rationale). Critically, the exact two basket-adjacent tests that
  failed in the diff's full run (`sale-screen-213.spec.ts` — "count
  badge tracks add, remove and clear" and "basket rows visible under
  body.kiosk...") **passed cleanly** in the base run and in every
  targeted (non-full-suite) run on both sides. Conclusion: full,
  single-worker 500+-test sequential runs are flaky/cascade-prone in
  this sandboxed execution environment independent of this diff — not
  evidence of a regression. The reliable signal is the targeted run
  above; real CI (GitHub Actions, presumably better resourced/isolated)
  remains the authoritative gate before merge.

## What the independent review found

Reviewed at **Opus** (card is `complexity:medium`, built inline at
Sonnet — per `MODEL-ROUTING.md`, a different and stronger model reviews).
Verdict: **not safe to merge as submitted**; safe to merge after the
fixes below, all made in an isolated worktree with a negative control
proving each fix's test can fail.

| # | Severity | Finding | Resolution |
|---|---|---|---|
| **F1** | blocker (CI) | `scripts/ci/guard-docs-shots.sh` hashes every file under `web/public/**` into a `surface_sha256` recorded in `web/help/img/manifest.json`. This diff touched `app.css` without refreshing that hash, so the guard — part of `ci.yml`'s `build` job — would fail the PR on merge. | Fixed: ran `scripts/ci/update-docs-shots-surface-hash.sh`; the diff is exactly the one `surface_sha256` line. The change lives entirely inside a `@media (max-width: 480px)` block and `docs-shots` only captures at 1024×600, so no rendered screenshot pixel actually changes — this is the guard's own documented escape hatch for exactly this case. Commit carries `Docs-Shots-Unchanged: true`. |
| **F2** | should-fix | The submitted grid's name area spanned only its own four rigid rem tracks (15.1rem), so it was still capped at their sum regardless of card width — measured 272.0px of an available 288.3px at 360px, and 272.0px of 408.3px (33% of the row unused) at 480px. The card's whole point was to stop the name being sized by that budget; it still was, just a bigger version of it. The submitted test's ">120px" floor passed the capped version, so nothing caught it. | Fixed: added a leading `1fr` spacer column; the name area now spans all five tracks and the numeric columns stay flush to the inline-end. New test asserts the name cell width equals the row width (within 1px); negative control reproduced the original 272px-of-288px cap. |
| **F3** | should-fix | The new row's `column-gap: .3rem` added its own width on top of a **pre-existing** overflow this breakpoint already has above `ui_scale 1` (ut-docs#391's own matrix only sweeps 1024px/901px, both above 480px, so nothing tests this corner) — measured +23px at scale 1.5, +31px at scale 2, at every phone width, on top of a baseline overflow of 132px/294px respectively. Not a new bug this fix introduced from nothing, but it made an existing gap measurably worse. | Fixed: removed the `column-gap` — `.basket td`'s own `.35rem` inline padding already separates the four cells. Overflow after the fix is byte-identical to the pre-#1338 baseline at every scale checked. New test asserts the second row's total span never exceeds the ≥481px table's own declared 15.1rem budget (direction-agnostic, so it holds in RTL too). |
| **F4** | should-fix | `display: block` on `<thead>` makes its single header `<tr>` a `:last-child` too, so the submitted `.basket tr:last-child { border-bottom: none }` (meant to remove the trailing border after the *last basket line*) silently also erased the header/body separator — measured `border-bottom-width: 0px` on the header row. Cosmetically easy to miss in a screenshot. | Fixed: scoped to `.basket tbody tr:last-child`; added `.basket thead tr { border-bottom-width: 2px }` matching `.basket th`'s own 2px rule at every other width. New test asserts the header row's rendered border-bottom is ≥1px. |
| **F5** | should-fix (test quality) | The new spec's own `scan()` helper used `page.getByRole('textbox').first()` — the exact pattern `sale-screen-213.spec.ts` and `basket-no-horizontal-scroll-391.spec.ts` already carry an explicit comment warning against, since ut-docs#1284 made `.qty-input` `type="text"` (role `textbox`). Once a basket line exists, `.first()` can resolve to the qty input instead of the scan field — reproduced directly: the barcode landed in `.qty-input` and the scan field stayed empty, hanging the test on a 30s `waitForResponse` timeout. | Fixed: scoped to `.scan-row input[name="code"]`, matching the two sibling specs' own established pattern. (Noted, not fixed — pre-existing, out of scope: `basket-item-name-width-1314.spec.ts` line 25 still has the same pattern; worth its own card if it ever flakes.) |

Also checked and found clean, so not re-litigated: no Go code and no
file-writing logic in the diff at all (`os.MkdirAll`/`paths.Data` class
of bug doesn't apply); no real client/shop name or secret-shaped literal
anywhere touched; `reference/ux-guidelines.md`'s checklist (existing
design tokens only, RTL-safe, no new modal, real states, the German
long-compound case is the spec's own second test); no help topic under
`web/help/**` describes phone-tier basket row layout so none needed
updating, and `docs-shots` captures only at 1024×600 so a 360px-specific
screenshot was never in scope to begin with — the only manifest action
needed was F1's hash refresh.

## Deferred (filed as a new Backlog card, not built here)

**ut-docs#2256** — the qty-column's duplicate `.line-order-type-phone`
dine-in/takeaway pill (added by ut-docs#1314 solely because the item
column was then too narrow — ~32px — for the desktop pill under the item
name) is now obsolete: the item column is 288.3px. Measured at review:
returning to the single desktop pill under the name would shrink a
360px/ui_scale-1 basket row from 206.3px to ~143.4px (below even the
pre-#1314 baseline of 151.1px) — meaningfully more of the basket visible
inside its `45dvh`-capped scroll box. Not built in this card because it's
a UX-owned interaction decision on a control the product owner has
explicitly iterated on twice already (ut-docs#1379, ADR-0073 Decision
2), it touches `basket.html` (not just a media query), and it needs its
own check that exactly one radiogroup per line ever reaches the
accessibility tree. Bundled into the same follow-up: `.line-thumb {
display: none }` at this breakpoint (also from #1314, also justified
solely by the now-stale narrow-column constraint).

## Not verified

- **WebKit/Safari accessibility-tree behavior for the `display:grid`
  override is unverified** — only Chromium is installed in this sandbox.
  Chromium retains the table's implicit ARIA roles through the override
  (confirmed live, matching this file's existing `#open-orders-table`
  precedent), but this is called out rather than assumed to hold
  cross-engine; recommend a follow-up check specifically on iOS Safari,
  the actual target for this breakpoint.
- **Not checked on real phone hardware.** Measured and screenshotted in
  headless Chromium at 360×640/740 and 480px only.
- Two full-suite sequential runs (480/500 and 411/496 passed
  respectively) both showed unrelated cascading failures under this
  sandbox's constraints — see "Full-suite noise investigated" above.
  Real GitHub Actions CI on the opened PR is the authoritative full-suite
  gate; this record does not substitute for watching that run.

## Verdict

**Safe to merge.** All five review findings are fixed, each with a
negative control proving its own test can fail without the fix. The
targeted, AC-relevant regression suite (47 tests across the new spec
plus the four specs the card's own acceptance criteria named) is green,
run independently twice. RTL and the empty-basket/header-row states were
measured live, not assumed. The one deferred item (ut-docs#2256) is
correctly out of scope for this card and filed on the board rather than
silently dropped.
