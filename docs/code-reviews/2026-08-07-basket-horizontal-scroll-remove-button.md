# Code review — basket horizontal scroll clips the remove button (3rd recurrence)

- **Date:** 2026-08-07
- **Task:** ut-docs#391 (basket still scrolls horizontally, remove button
  clipped in half on real till hardware — third reported recurrence of
  this defect class)
- **Branch:** `fix/basket-horizontal-scroll-391`
- **Author:** pipeline Dev step (Sonnet, inline)
- **Independent reviewer:** general-purpose subagent on **Opus** (different
  model, per standing practice — this card is `complexity:medium`)

## What shipped

- **Root cause**: `.basket-scroll` set `overflow-y: auto` without an
  explicit `overflow-x`. Per the CSS overflow computed-value coupling
  rule, the unset `visible` axis silently became `auto` too, so once the
  basket table's natural width exceeded the panel, a horizontal
  scrollbar appeared defaulting to scroll-position 0 — clipping the
  remove button (the right-most column) in half.
- `web/ui/partials/basket.html`: `<td class="line-item">` →
  `<td><div class="line-item">…</div></td>`. A `<td>` with `display:
  flex` specified directly on it fails CSS table fixup — the browser
  wraps it in an anonymous table-cell box instead, which is what
  actually participates in `table-layout: fixed`'s column-sizing
  algorithm. Without this, a width rule aimed at the original `<td>`
  never reaches the real column box.
- `web/public/app.css`: `.basket table` switches to `table-layout:
  fixed`. Reserves explicit REM widths for the fixed-content columns
  (qty+discount inputs 8rem, price 4rem, total 4rem, remove 2.8rem —
  every one sized to its real content, invariant to `--ui-scale` since
  the content is rem-sized too) and leaves the ITEM column with no
  declared width at all, so the algorithm gives it 100% of whatever's
  left — unambiguous with exactly one such column. `.basket-scroll`'s
  `overflow-x` is now explicit (`auto`, documented as a fallback, not
  the fix) instead of relying on the implicit coupling.
- `e2e/tests/basket-no-horizontal-scroll-391.spec.ts` (new): basket-scroll
  has no horizontal overflow at the kiosk floor (1024x600); the remove
  button's bounding box is fully inside the basket panel in both LTR and
  RTL (fa); the qty/discount inputs' own values never scroll out of view
  inside the input itself; and a `ui_scale` × viewport matrix (every
  scale Settings actually offers, 1 through 2, at both the 1024x600
  kiosk floor and the 901px narrowest desktop-grid width) checking the
  remove button sits inside its own column, not just the panel overall.
- Manual screenshots regenerated (`make docs-shots`):
  `web/help/img/**/sell.png` reflect the fixed layout.
  `alerts.png`/`designer.png` also picked up unrelated non-deterministic
  dynamic content (timestamps, a live receipt number/barcode) — a known
  gap tracked separately (ut-docs#360, #370), not caused by this diff.

## The independent review, and three iterations to get it right

This is the part worth reading in full — the review did its job.

**First pass (commit `d102162`)** used a straight PERCENTAGE column
budget (33/31/14/14/8) under `table-layout: fixed`. Deterministic against
`table-layout: auto`'s redistribution problem, but wrong differently: the
Opus review measured it **failing at `ui_scale` 2** — a real, offered,
guarded-as-supported Settings option (`ui-scale-basket.spec.ts` already
guards it) — with negative margins up to **-42px** and the remove button
**clipped in half again**, reproducing the original field report exactly.
It passed the one viewport the first e2e spec covered by only **0.48px**,
with the button already 5.48px wider than its own column, surviving only
because cell padding absorbed it. Root cause: the fixed-content controls
are REM-sized (track `--ui-scale` exactly), but a PERCENTAGE column is a
share of the panel's *rendered px width*, which stops moving in lockstep
with rem content once the panel hits its own 22rem/26.25rem clamp at a
viewport/scale combination where the two diverge. Review also
independently re-verified the original TDD claim (reverted the CSS/HTML
in the working tree, confirmed the regression test failed with the exact
claimed symptom, restored, confirmed green) and confirmed CI
guards/screenshots looked right otherwise.

**Second pass**: reserved the fixed-content columns as explicit REM
(scale-invariant) and split the rest via `calc((100% - reserved) *
ratio)` on item/price/total. The REM columns came out exactly right — but
`table-layout: fixed`'s column-sizing algorithm does not evaluate
`calc()` the way normal box sizing does: instead of applying the intended
55:22.5:22.5 ratio, the browser silently fell back to splitting the
remainder **equally** across the three `calc()`'d columns, squeezing item
to a third of its intended share. The table's own `scrollWidth` still
matched `clientWidth` (no horizontal overflow — the specific thing this
bug is about), so the e2e spec's own geometry assertions never caught it;
only a targeted live DOM probe (declared vs. rendered column width) did,
self-caught before re-review.

**Third pass**: same REM reservations, but item/price/total as plain
PERCENTAGES (not `calc()`) calibrated for the worst case — the panel's
22rem floor, where the REM columns' share of the table is largest.
Correctly scale-invariant this time (verified: 10/10 configs across the
review's exact failing matrix, plus RTL) — but a fixed percentage
calibrated for the worst case can't also be generous at the panel's
roomy 26.25rem default width. Self-caught by re-running the *full* suite
(not just the new spec) before considering this closed: regressed
ut-docs#213's own e2e guard (`>=4 basket lines visible without scrolling
at 1280x800`) — a narrower item column meant more names hit their
existing 2-line clamp, taller rows, fewer fit.

**Final design** (what actually shipped): item is the sole column with
no declared width; `table-layout: fixed` gives it 100% of whatever's left
after the four explicit-REM columns are honoured — unambiguous with
exactly one such column, avoiding pass two's equal-split trap entirely.
Price/total widened from an initial 3rem to 4rem after discovering even
"PRICE"/"TOTAL" header labels (not just data amounts) needed more room
than 3rem gives at normal desktop width. Re-verified: full e2e suite
(82/83, only the pre-existing unrelated `catalog-image-to-till.spec.ts`
failure), the review's original failing matrix (10/10), the AC-213
regression (now passing), and a targeted visual check at the exact worst
case the review found broken (901x600 @ `ui_scale` 2) — remove button
fully visible with margin, qty/price/total legible, headers no longer
collide (added `overflow-wrap: anywhere` to the header cells too, for the
same reason it was already on the data cells).

## Independently re-verified TDD claims

- Reviewer reverted the app.css/basket.html changes in the working tree
  (regression spec kept), re-ran the spec, confirmed 3/4 failed with the
  exact claimed symptom (LTR button right edge 13.3px outside the panel,
  RTL left edge 11.7px outside, scrollWidth overflow), matching the field
  report's "remove button clipped in half" verbatim in the failure
  screenshot. Restored, confirmed 4/4 green.
- This session re-confirmed the two prior failed-attempt regressions the
  same way: `calc()`'s equal-split via a live DOM probe (declared 55%
  item column measured 110px vs. price/total's declared 22.5% *also*
  measuring 110px — proof of equal-split, not the intended ratio); the
  AC-213 regression via the pre-existing `sale-screen-213.spec.ts` itself
  going from pass → fail → pass again across the second and third
  design iterations.
- Confirmed the one known residual (see below) predates this ticket
  entirely: checked out unmodified `main`'s `app.css`/`basket.html` at
  the same narrow width and found the remove button clipping **far**
  worse there (-151px margin vs. -33px with this fix) before any of this
  work landed.

## Verified beyond automated tests

- `go build ./...` — clean (no Go files touched by this diff at all).
- `go test ./...` — clean except the pre-existing, unrelated
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` (root-
  sandbox permission quirk, documented in prior review records,
  reconfirmed failing identically on unmodified `main`).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` — all green. The last one
  specifically enforces that this `app.css`/`basket.html` change is
  matched by regenerated `web/help/img/**` + `manifest.json`.
- Full e2e suite (`e2e/tests/*.spec.ts`, default project): 82 passed, 1
  pre-existing unrelated failure (`catalog-image-to-till.spec.ts`,
  confirmed failing identically on unmodified `main` — a sandbox image-
  loading artifact, not a real regression).
- Manual screenshots (`web/help/img/en|fa|ar|tr/sell.png`) read back and
  visually confirmed: remove button fully visible, item names legible,
  qty/price/total single-line and legible, RTL correctly mirrored — both
  at the documented kiosk floor and (separately, ad hoc) at the worst
  case the review found broken.

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | **blocking** | First-pass percentage budget failed at `ui_scale` 2 (button clipped in half again) and passed the tested viewport by only 0.48px | **Fixed** — redesigned to explicit-REM-reserved + auto-remainder-item columns (see history above); reverified across the full failing matrix |
| 2 | **blocking** | Remove button was 5.48px wider than its own declared column at the one tested viewport, surviving only via cell-padding absorption | **Fixed** — remove column now 2.8rem, explicitly sized to `.btn-x` + its padding + cell padding with headroom for documented "wider font metrics" hazard |
| 3 | **blocking (AC violation)** | `.basket table`'s pre-existing `overflow: hidden` made any future column overflow a *permanent* hard clip, exactly what the AC rules out | **Fixed** — removed; confirmed cosmetically safe (table background matches the panel's own background, so square vs. rounded table corners are visually indistinguishable) |
| 4 | should-fix | The `3.4rem → 3.3rem` input narrowing in the first pass silently clipped a real qty value at some viewport/scale combos | **Fixed** — reverted to 3.4rem; also self-caught and fixed an *additional* instance of this same class in a later iteration (7.8rem qty+discount column budget was too tight by 1px at some non-integer scales; widened to 8rem) |
| 5 | should-fix | The comment's "verified... at the 22rem floor" claim was unverified and, per the review's own measurement, untrue (panel sits at 26.25rem at every default-scale viewport tested) | **Fixed** — comment rewritten with the actual verification methodology and honest history of what was tried and why it didn't hold, rather than a single unverified claim |
| 6 | should-fix (test gap) | The new spec only covered one viewport/scale; recommended parameterizing over `ui_scale` and adding a narrower viewport, plus asserting the button's box against its own `<td>` not just the panel | **Fixed** — spec now sweeps every `ui_scale` Settings offers at two viewports, and checks the button fits its own column specifically (the exact check that would have caught pass two's `calc()` failure) |
| 7 | real-but-minor | `overflow-wrap: anywhere` on price/total fired on ordinary amounts ("£1.20" splitting mid-number) under the first-pass budget | **Fixed** — no longer fires at any normal supported configuration under the final column budget; kept as a safety net for genuinely pathological amounts only, extended to the header cells too after self-discovering they had the identical problem at the extreme corner |
| 8 | nitpick | `.line-item`'s `max-width: 9.875rem` was flagged as now-inert dead code under the (at-the-time) percentage design | **Resolved by the final design itself** — the item column is now the flexible/unspecified one, so this max-width is genuinely load-bearing again (caps how far it grows at the panel's default width); comment updated to say so explicitly |
| 9 | nitpick | Comment wording ("declared 34%" vs. actual 33%; "narrowest→widest" then listed widest→narrowest) | **Moot** — the whole column-width design was rewritten across three more iterations after this was flagged; the current comment was written fresh against the final, verified numbers |
| 10 | nitpick | `.basket th/td:nth-child(N)` width rules also apply to the empty-state `colspan="5"` row | **Accepted as-is**, per the reviewer's own read — harmless today (colspan sizing takes the combined width regardless), a latent oddity not worth extra selector complexity for |

## A known, out-of-scope residual

At viewports well below the documented 1024x600 kiosk floor (tested down
to ~350px — a browser window resized far narrower than any real till
configuration, not a "supported till resolution" per the AC), the remove
button can still clip slightly. Confirmed this predates ut-docs#391
entirely: unmodified `main` clips it far worse at that same width
(-151px margin) than this fix does (-33px) — a real improvement, just not
a complete one at a width nobody asked this ticket to support. Not
chased further; flagged here rather than silently dropped.

## Verdict

**Safe to merge.** The reported bug — the basket scrolling horizontally
and the remove button clipping in half on real till hardware — is fixed
and independently re-verified at the documented kiosk floor (1024x600),
every `ui_scale` Settings actually offers, and both LTR and RTL, without
regressing the pre-existing `>=4 lines visible` AC-213 guard. The
independent review's three blocking findings on the first design are
fixed via a genuinely different (and this time independently
re-verifiable) mechanism, not a patched-over version of the same broken
approach — full history kept in `app.css`'s own comment and this record
so a fourth recurrence, if it ever happens, doesn't re-walk the same two
dead ends.
