# Code review — sale screen density + tender-panel clipping (2026-08-30)

**Branch:** `sale-screen-density-fix` · **Commit under review:** `c0ba2d05`
(WIP snapshot, one commit on top of `76546a31`; reviewed merged onto current
`main` `3911240a` — no conflict, `main`'s only `app.css` change since the base
is the unrelated `.setup-update-banner` block from ut-docs#1165)
**Repo:** `universal-till` · **Files:** `web/public/app.css`,
`e2e/tests/tender-panel-reachable.spec.ts` (2 files, +67/−6). No Go, no
templates, no locales, no migrations.
**Reviewer:** independent pass in an isolated worktree — did not write this
code. Every claim in the new inline comments and in the hand-off notes was
re-measured live rather than taken on trust.

## Origin

Product owner compared the sale screen against a competitor tablet POS and
called it crowded. Two separate complaints are being answered here:

1. **Tender clipping.** `app.css`'s own comment block above
   `.pos-container`'s `grid-template-rows` (written by ut-docs#1231, 2026-08-28)
   claims the `4fr:3fr` split was tuned so Cash/Card "stays on screen at
   1280x800 AND 1024x600 with no scroll". Live screenshots this session showed
   Cash and Card clipped by the sticky footer at 1024x600 with no scrolling at
   all.
2. **Product-tile density.** Ours fit 3 tiles per row where the competitor fits
   ~5 in less width.

## What shipped

- **`.pay-grid`** (app.css:1139) — `grid-template-columns` from a fixed
  `1fr 1fr` to `repeat(auto-fit, minmax(9rem, 1fr))`, plus a new ~14-line
  comment. `.pay-btn`'s `min-height: 4.2rem` untouched; the
  `@media (max-width: 480px)` one-column override untouched.
- **`.pos-container`** (app.css:507) — `grid-template-rows` flipped from
  `minmax(8rem, 4fr) minmax(0, 3fr)` to `minmax(8rem, 3fr) minmax(0, 4fr)`,
  i.e. products-dominant → tender-dominant. **The ~50-line comment block
  directly above it was not touched.**
- **`.tender`** (app.css:1061) — flex `gap` `.55rem` → `.4rem`.
- **Tile density** — `.grid` column floor `8.8rem` → `7.5rem` (app.css:752);
  `.btn-tile` `min-height` `8.5rem` → `7.2rem` and padding
  `.55rem .5rem .6rem` → `.5rem .45rem .55rem` (app.css:755); `.thumb` height
  `4.25rem` → `3.4rem` (app.css:768); plus a new 7-line comment.
- **`e2e/tests/tender-panel-reachable.spec.ts`** — a third test,
  `'Cash and Card are visible with no scroll at 1024x600, default scale'`,
  hit-testing each button's own **bottom** edge (`r.bottom - 2`) with
  deliberately **no** `scrollIntoViewIfNeeded()`.

## Gates

| gate | result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...` | clean |
| `go test ./...` | 1 pre-existing failure: `internal/server`'s `TestListenWithFallback_WildcardHostFallsBackToLoopback`. **Verified myself** in a second worktree checked out at `3911240a` — fails identically there, so it is a sandbox/network artefact, not this diff. Nothing else fails. |
| all `ci.yml` `build`-job guards | **`guard-docs-shots.sh` FAILS** (see B1). Every other guard passes: data-access, migration-version-collision, kiosk-engine, plugin-menu-read, i18n, compliance-claims, help-topics, webkit-version, kiosk-launch-flags, android-status-address, android-i18n, emoji-font, htmx-loaded, autofill-suppression, osk-loaded, check-brand-assets, makefile-version, and `guard-docs-shots-cross-check_test.sh`. |
| Playwright `--project=default`, full suite | **230 passed / 2 failed.** `main` baseline, same box, same run: **230 passed / 1 failed.** The extra failure is the new test itself (see B2). |
| `tender-panel-reachable.spec.ts` alone | 3/3 pass. |

Also confirmed: the diff touches **no Go file at all**, so this pipeline's two
recurring bug classes (file write without `os.MkdirAll`, cwd-relative path
instead of `paths.Data(...)`) are n/a by construction. No user-facing string,
template or route changed, so `web/help/en/sell.md`'s prose genuinely needs no
edit — read end to end: it says "then choose Pay" and "the offline checkbox
next to the Pay buttons", neither of which this diff invalidates.
`guard-help-topics.sh` agrees. No ADR is touched (ADR-0008's server-rendered
HTMX model is unaffected).

## What I measured

All numbers below are from a real Chromium against a real till
(`go run ./e2e/seed_demo` into a fresh `UT_DATA_DIR`, `UT_AUTH=off`,
`UT_OPEN_BROWSER=false`, port 8093, demo catalogue, default UI scale, empty
basket). "before" is `main`'s values re-applied over the branch build via
`addStyleTag`, so both sides are the same binary and the same catalogue.

### 1. The reported bug is real, and #1231's comment is wrong — but not because of a later regression

At **1024x600**, `main`'s `4fr:3fr`:

| probe | result |
|---|---|
| Cash / Card, **centre** hit-test | HIT |
| Cash / Card, **bottom-edge** hit-test | **miss — `.statusbar`** |
| Gift Card | off-viewport entirely |

That is the whole discrepancy. **#1231's tuning was validated with a
centre-point hit-test** (its two existing tests, and its own review record's
measurements, all probe `r.top + r.height/2`), and by that criterion `4fr:3fr`
genuinely passed. Its *prose* then upgraded that to "stays on screen … with no
scroll", which was never what was measured. `web/help/img/en/sell.png` on
`main` shows the same thing: the pay buttons are present but sheared off
mid-label.

I checked the "Gift Card was added later" hypothesis and it is **false**:
`('gift','Gift Card','voucher',3)` is seeded in `internal/db/migrations/001_init.sql`
and `git log -S"'Gift Card'"` shows no later modification — it has been in the
initial migration throughout. #1231's own review record even names Gift Card in
its F1 table. So three payment methods *were* the configuration #1231 tuned
against. This is a criterion mismatch, not a regression. **The branch's premise
is correct; the fix is warranted.**

### 2. The `auto-fit` pay grid is the good half of this change

Putting Cash/Card/Gift Card on one row instead of two reclaims a full
`4.2rem` button row plus a gap out of `.tender`'s budget, without shrinking any
individual button (`.pay-btn`'s `min-height` is untouched — measured 71–75px
tall at every viewport tested, well clear of any touch-target floor). At
1280x800 the grid resolves to three 234.6px tracks plus one collapsed 0px
track; at 1024x600 three 158.5px tracks. This is the change that actually
addresses the complaint, and it is well-judged.

### 3. But the row-ratio flip is over-corrective, and it re-breaks ut-docs#1231

Products vs tender, and what is actually visible:

| viewport | variant | products px | pay buttons (bottom-edge) | Hold Sale / New Customer |
|---|---|---|---|---|
| 1024x600 | main 4:3 | 227 | Cash CLIP · Card CLIP · Gift OFF | OFF |
| 1024x600 | **branch 3:4** | **171** | ok · ok · ok | CLIP |
| 1024x600 | 4:3 + auto-fit | 227 | CLIP · CLIP · CLIP | OFF |
| 1024x600 | **1:1 + auto-fit** | **199** | **ok · ok · ok** | OFF |
| 1280x800 | main 4:3 | 337 | ok · ok · Gift OFF | CLIP |
| 1280x800 | **branch 3:4** | **252** | ok · ok · ok | ok |
| 1280x800 | **1:1 + auto-fit** | **295** | **ok · ok · ok** | **ok** |
| 1194x834 | main 4:3 | 358 | ok · ok · Gift CLIP | CLIP |
| 1194x834 | **branch 3:4** | **268** | ok · ok · ok | ok |
| 1194x834 | **1:1 + auto-fit** | **313** | **ok · ok · ok** | **ok** |

**At every viewport I tested, `1fr:1fr` reaches exactly the same pay-button and
footer outcome as `3fr:4fr` while giving products 28–45px more height.** There
is no measured case where `3fr:4fr` buys anything `1fr:1fr` does not. The
specific ratio chosen is therefore not justified by anything I could reproduce.

What that lost height costs, looking at the screenshots rather than the numbers:

- **1280x800, branch:** the products panel shows the *top sliver of the
  thumbnails only* — no product name, no price, on any tile. This is
  ut-docs#1231's bug report verbatim ("the very first product tile rendered
  clipped mid-tile, no visible scroll affordance — confirmed live on the
  1280x800 reference till"), at the same resolution, with the same symptom. The
  `1fr:1fr` shot at the same size shows full thumbnails plus "Butter 250g",
  "Whole Milk 1L" and their prices, *and* keeps all five tender controls on
  screen.
- **1024x600, branch:** the products panel shows **no product tile at all** —
  search box, category tabs, and a clipped "Dairy" group heading. `main` at
  least showed tile tops. An operator at the documented kiosk floor must scroll
  the products panel before they can tap anything.
- **1194x834, branch:** the tender panel has an obvious empty band between the
  pay row and Hold Sale/New Customer (`.tender-footer`'s `margin-top: auto`),
  i.e. tender is being handed height it does not use while products starves.
  That is the clearest single argument that `4fr` for tender is too much.

Under genuine vertical pressure the ratio is a no-op, exactly as #1231's
comment claims — I re-verified this for the new value rather than assuming it
carried over. At 1024x600 with `body.osk-padded`, and at 1024x600 / 1920x800
with `--ui-scale: 2`, `4:3`, `3:4` and `1:1` all produce **byte-identical**
track heights (e.g. 136.0/25.8 and 272.0/49.6). So no new collapse pathology is
introduced; the whole disagreement is about the unconstrained case.

### 4. German / long-locale: a real, visible regression from the tile change

No German locale ships in-repo (en/fa/ar/tr; de comes from
`ut-plugin-language-de`), so I substituted realistic German product names into
the live DOM and measured `.tile-name`'s own overflow.

First, a correction to the diff's own new comment: **`.tile-name` has no
2-line clamp.** The rule is
`.tile-name { font-weight: 600; font-size: .95rem; line-height: 1.2; white-space: normal; }`
— no `-webkit-line-clamp`, no `max-height`, no `overflow`. Computed
`-webkit-line-clamp` is `none`. The comment added at app.css:748
(".tile-name keeps its 2-line clamp and font-size, so this stays a pure density
change") is asserting something that does not exist.

The consequence is the opposite of a clamp: a name that does not fit **spills
horizontally**, because there is no `overflow-wrap`/`word-break` either.
Measured at 1280x800 (`.tile-name` scrollWidth vs clientWidth):

| name | main (tile 172px) | branch (tile 136px) |
|---|---|---|
| Halbfettmargarine 500g | 153 / 153 — fits | **146 / 118 — overflows** |
| Weizenmischbrot 750g | 153 / 153 — fits | **138 / 118 — overflows** |
| Apfelsaftschorle 1,5L | 153 / 153 — fits | **131 / 118 — overflows** |

Confirmed visually in a panel screenshot: on `main` the names render in full;
on the branch "Halbfettmargarine" renders as "Halbfettmargarin" with the final
letter sheared at the tile edge. It is the narrower column floor (`7.5rem`) that
does this, not the height changes. This is squarely against the standing
`multilingual-everything` rule.

Two further observations from the same measurements:

- **The `min-height: 8.5rem → 7.2rem` change is inert.** With no clamp, tile
  height is content-driven: measured 150–178px everywhere, never anywhere near
  either floor (7.2rem ≈ 115–122px). The real height saving came entirely from
  `.thumb` (−15px) and the padding trim. Harmless, but the comment's reasoning
  rests on a premise that does not hold.
- **The column-floor change buys nothing at two of the three viewports.**
  Tiles per row: 1024x600 3 → 3 (identical 157.4px tiles); 1280x800 4 → 5;
  1194x834 3 → 4. So the density win is real at 1194 and 1280, and absent at
  the kiosk floor — where the panel is too short to show a tile anyway.

German **payment-button** labels are fine: a width sweep at 481/520/560/620/
700/820/900/1024px with "Bargeld / Karte / Geschenkkarte" found no horizontal
overflow at any width, in either variant. The narrowest `auto-fit` case
(3 × 157px at 560px viewport) still holds "Geschenkkarte".

### 5. Test soundness

The new test is a **genuine** regression test, verified red→green by me rather
than assumed: with `main`'s `app.css` restored and the binary rebuilt, it fails
with "Cash's own bottom edge must be visible and unclipped with no scroll"; with
the branch's CSS it passes. The `r.bottom - 2` + `elementFromPoint` technique is
sound for the failure mode it targets — clipped content under `overflow: hidden`
is not hit-testable, and a collapsed (0-height) button gives `r.bottom - 2` above
its own top edge, so `el.contains(at)` is false and the test still fails. No
false-pass there.

Its real weaknesses are elsewhere:

- **It is not suite-safe** (B2 below) — this is the blocking one.
- **It only checks the bottom edge**, so a button clipped from the *top* by a
  scrolled ancestor would pass. Cheap to close: also probe `r.top + 2`.
- **It never asserts that no scroll is needed**, only that the buttons happen to
  be visible at the initial scroll position. `expect(tender.scrollHeight)
  .toBeLessThanOrEqual(tender.clientHeight)` would actually match the test's own
  name.
- **It only checks Cash and Card**, so #1231's other documented failure mode
  ("every OTHER payment button off") is unguarded. Demonstrated: cloning the pay
  buttons to simulate 4/5/6 methods at 1024x600 on the branch gives
  `Cash:ok Card:ok Gift Card:ok PayPal:OFF Voucher:OFF Account:OFF` — the test
  still passes. That matters because the new `.pay-grid` comment cites
  "Cash+Card+one more (Gift Card, a payment plugin — extremely common, not an
  edge case)" as the motivating case, and a fourth method is exactly as common.
- `hasText: 'Card'` also matches "Gift Card"; only DOM order plus `.first()`
  saves it, and `payment_methods.sort_order` is shop-editable. Prefer
  `data-method="card"`.
- Style nit: the other two tests scope to `.tab-panel`, this one to
  `#panel-pay` (the same element). Harmless, but pick one.

### 6. Kiosk touch-target parity (ut-docs#161)

Confirmed clean. `body.kiosk` sets no `.btn-tile` or `.pay-btn` size override
anywhere — only `-webkit-user-select`, `touch-action`, `input/select`
min-heights, `.pos-container`'s height and the basket total's font-size. Kiosk
inherits the base values unchanged, exactly as the ut-docs#161 comment
requires, and no kiosk-only shrink is reintroduced. Measured `.pay-btn` heights
71–75px and tile heights 150–178px at every viewport — nothing near a 48px
floor.

One stale reference: that comment (app.css:1284) still cites "the 3rem/8.5rem
base rules". `.btn-tile`'s base is now 7.2rem. Worth correcting while the file
is open — this repo has twice treated inline-comment accuracy as load-bearing
(#1231's own F3, and the stale-comment fix in
`2026-08-28-osk-touch-pointerup-activation-1219.md`).

### 7. Deferring the basket column widths was the right call

Confirmed by reading the block at app.css:~880-943. It documents **three**
failed prior attempts (`calc()` mixing, a `table-layout: fixed` equal-split
trap that geometry assertions could not see, and a percentage calibration that
regressed ut-docs#213's ">=4 basket lines visible at 1280x800" AC) and states
the verification matrix any change must repeat: rendered-vs-declared column
widths, `scrollWidth/clientWidth`, remove-button bbox and the >=4-lines AC,
across ui_scale 1/1.75/2 × 901/1024/1280px × LTR and RTL. Folding that into
this diff would have made it unreviewable and put a shipped AC at risk. It
belongs on its own card.

## Findings

### B1 — BLOCKING: `guard-docs-shots.sh` fails; the manual's screenshots are stale

`web/public/**` is explicitly inside the docs-shots surface fileset — the
guard's own header says so ("a theme/app.css change is exactly as visible in a
screenshot as a template change"). The guard passes at `3911240a` and fails on
this branch:

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
changed since the manual's screenshots were last taken
guard-docs-shots: run `make docs-shots` and commit the result
```

`guard-docs-shots-cross-check_test.sh` confirms the Python and JS
implementations agree on the new hash, so this is not a guard bug. CI's `build`
job will go red. `make docs-shots` + commit is required.

This is not just hash bookkeeping: the sale screen's appearance materially
changed, and the manual's own `sell.png` is captured at 1024x600. As the branch
stands, the regenerated Sell screenshot will show all three payment buttons and
**not one product tile** — which is a worse picture of the Sell screen than the
one it replaces. Fixing F1 first would avoid shipping that.

### B2 — BLOCKING: the new e2e test is order-dependent and fails in the real suite

3/3 green when the spec runs alone; **red** in the full `--project=default`
run. `main` baseline on the same box: 230/1. Branch: 230/2. The extra failure is
this test.

Cause, from the failure screenshot: earlier specs leave **held sales** behind
(the specs share one server, and `POST /api/pos/hold` has no counterpart that
clears a hold except resume-and-tender). The `#held-sales` strip lives inside
`.tender`, above the tab bar; with three held orders it takes ~110px and pushes
the whole pay grid past the panel edge — the exact failure the test asserts
against, in a state the CSS cannot fix.

Two things follow. The test must be made hermetic before merge — cheapest
honest fix is to drain the strip at the top of the test (`GET /ui/held`, then
`POST /api/pos/resume` per id — resume deletes the row — then New Sale),
rather than weakening the assertion. And separately, the underlying fact is
real and worth a card: **with held sales present, Cash/Card are off screen at
1024x600 even after this change.** The diff does not address that, and the new
comment's "no scroll at 1024x600" claim is only true for an empty held strip.

### F1 — should-fix before merge: `3fr:4fr` is strictly dominated by `1fr:1fr`, and re-breaks ut-docs#1231

Per §3. `1fr:1fr` matches the branch's tender outcome at 1024x600, 1280x800 and
1194x834 while returning 28–45px to products, and at 1280x800 the difference is
the whole visible identity of the product tiles (names and prices, versus
thumbnail tops only). As shipped, the branch reproduces ut-docs#1231's reported
symptom on ut-docs#1231's own reference resolution, which is the kind of
round-trip this file's comment history exists to prevent.

Better still, and also measured: the only viewport that *forces* a
tender-favouring split is 1024x600. At 1280x800 and 1194x834, keeping `4fr:3fr`
with the new `auto-fit` grid already puts every payment button on screen
(1194x834 additionally shows four fully-visible tiles). So a height-scoped
override — keep `4fr:3fr`, add roughly
`@media (max-height: 640px) { .pos-container { grid-template-rows: minmax(8rem,1fr) minmax(0,1fr); } }`
— gets the best measured outcome at all three sizes instead of trading one
resolution against another. I did not apply it; that is the author's call, and
whichever is chosen the comment block above the rule must be rewritten (see F2)
and `make docs-shots` re-run.

### F2 — should-fix: the `.pos-container` comment now contradicts the code it documents

The ~50-line block above the changed line is untouched and still reads
"products — the screen's primary purpose — gets the dominant share at any
viewport height", "(4fr:3fr, tuned rather than an arbitrary round ratio …)",
and cites #1231's finding that a tender-favoured 2:1 split "scrolled Cash/Card
off-panel" and 1:1 "scrolled every OTHER payment button off". The code now says
tender-dominant `3fr:4fr`. Anyone reading this file next inherits a comment that
argues against the rule it sits on.

It also needs the one genuinely new insight recorded, because it is what makes
the old reasoning obsolete: **`auto-fit` changes the constraint #1231 was
optimising under.** #1231 rejected 1:1 because a fixed two-column pay grid put
Gift Card on a second row; with `auto-fit` all three share one row, so 1:1 no
longer has that failure mode. Without that sentence the next person will re-run
#1231's experiment and get #1231's answer.

Same file, same class: the ut-docs#161 comment at app.css:1284 still says
"8.5rem" (now 7.2rem), and the new comment at app.css:748 claims a 2-line clamp
`.tile-name` does not have (§4).

### F3 — should-fix: long product names now overflow their tile

Per §4 — measured and visually confirmed at 1280x800 with German-length names.
Root cause is the `7.5rem` column floor combined with `.tile-name` having no
`overflow-wrap`/`word-break` and no clamp. Cheapest fix is one declaration on
`.tile-name` (`overflow-wrap: anywhere`, or `hyphens: auto` with the element's
`lang` set); alternatively raise the floor to ~8rem, which costs the 1280x800
density win. Note the fix should be checked in RTL too, and the density claim
re-measured after: at 1024x600 the floor change delivers no extra tiles per row
at all.

### F4 — non-blocking, accepted: 4+ payment methods still overflow at 1024x600

Per §5. With four or more active methods the second `auto-fit` row is entirely
off-viewport at 1024x600 (Cash/Card/Gift Card remain fine). Reachable by
scrolling inside `.tender`, which is ut-docs#161's accepted fallback, so not a
blocker — but the new `.pay-grid` comment presents a payment plugin as the
motivating case and implies it is solved, and the new test does not cover it.
Either soften the comment or extend the test to every rendered `.pay-btn`.

### F5 — non-blocking nits

- Test robustness: `hasText: 'Card'` matching "Gift Card"; bottom-edge-only
  probe; no assertion that scrolling is unnecessary; `#panel-pay` vs
  `.tab-panel` inconsistency (all in §5).
- `.tender-footer` / `.split-grid` / `.split-controls` keep a fixed `1fr 1fr`.
  Defensible (two children each), but the new `.pay-grid` comment's reasoning
  ("`.tender` is never the width-constrained column") applies to them too, and
  a reader will wonder why only one changed. One clause would settle it.
- Pre-existing and untouched by this diff, worth its own card: the products
  panel has **no visible scroll affordance** at any viewport measured
  (`scrollHeight` 495 vs `clientHeight` 169 at 1024x600 on the branch). #1231's
  review already logged this as F5 and it is now considerably more visible,
  since after this change a 1024x600 operator must scroll before seeing any
  product at all.

### Checked and found NOT to be a problem

- **No new pathology under vertical pressure.** `4:3`, `3:4` and `1:1` give
  identical track heights under `body.osk-padded` and at `--ui-scale: 2`
  (§3) — #1231's "this only splits the LEFTOVER" property survives the change.
- **Tablet/phone tiers untouched.** `@media (max-width: 900px)` and
  `(max-width: 480px)` both redefine `grid-template-rows` wholesale, and the
  480px one-column `.pay-grid` override is intact and still wins. The full
  `phone-width-layout-413` and `ui-scale-basket` specs pass.
- **Touch targets / kiosk parity** (§6).
- **RTL.** No physical `left`/`right` introduced; `grid-template-columns`,
  `gap`, `min-height` and the symmetric horizontal padding are all
  direction-agnostic, and `auto-fit` tracks flow correctly under `dir=rtl`.
- **i18n / compliance wording.** No user-facing string added or changed;
  `guard-i18n.sh` and `guard-compliance-claims.sh` both pass; no `en.json`
  change, so `lang-pack-drift` does not apply.
- **Manual prose.** `web/help/en/sell.md` read in full — nothing it claims is
  invalidated (only its *screenshots* are, per B1).
- **Recurring bug classes.** No Go file in the diff at all, so missing
  `os.MkdirAll` and cwd-relative-instead-of-`paths.Data(...)` are n/a.
- **No secrets, no client names, no ADR contradicted, no money/tax/persistence/
  auth/plugin-verification/offline-path surface touched.**

## Verdict

**Not safe to merge as it stands.** Nothing here is dangerous — this is CSS and
one test, and no money, data-integrity, security or offline-path surface is
touched — but two of the findings will fail CI outright and one of them
re-introduces a bug this file has already been through three rounds over.

Required before merge:

1. **B1** — `make docs-shots` and commit the regenerated screenshots +
   `manifest.json`. Do this *after* F1, so the manual does not ship a Sell
   screenshot with no products in it.
2. **B2** — make the new test hermetic against leftover held sales; re-run the
   full `--project=default` suite and confirm it lands back at `main`'s
   230/1 baseline, not 230/2.
3. **F1** — retune the ratio. `1fr:1fr` is strictly better than `3fr:4fr` at
   every viewport I measured; a `max-height` -scoped override that keeps
   `4fr:3fr` above ~640px is better still. This is a continuous UX parameter,
   not a business call, so it should be retuned rather than escalated — the
   same reasoning #1231 applied to its own F1.
4. **F2** — rewrite the `.pos-container` comment to match whatever ratio lands,
   and record *why* `auto-fit` invalidates #1231's rejection of a gentler
   split. Fix the two other stale/incorrect comment claims (8.5rem at
   app.css:1284; the non-existent 2-line clamp at app.css:748).
5. **F3** — stop long product names overflowing their tile.

The `auto-fit` pay grid is the right idea, is well-reasoned, and should
survive all of the above unchanged. The new test is a real regression test
(verified red on `main`, green here) and is worth keeping once B2 is fixed.

## Explicitly deferred (not attempted here, needs its own card)

- **Basket item-name column starvation** — the fixed `PRICE`/`TOTAL`/`QTY`
  rem widths in `.basket td:nth-child(N)`. Correctly deferred (§7): three prior
  attempts are documented as failures and any change must repeat a
  ui_scale × viewport × LTR/RTL verification matrix.
- **Products panel has no visible scroll affordance** (F5) — pre-existing,
  first logged as #1231's F5, materially more visible after this change.
- **Held sales consume the tender panel's height budget** (B2's second half) —
  with a populated held-sales strip, no ratio keeps Cash/Card on screen at
  1024x600. Needs a layout answer (a scrollable/collapsed strip, or moving it
  out of `.tender`), not a ratio.
- **4+ payment methods at 1024x600** (F4) — accepted under ut-docs#161's
  scroll-to-reach precedent, but should be stated honestly in the comment and
  ideally covered by the test.

## Addendum — fixes applied after review, second gate pass (2026-08-30)

All five required items addressed, in the order the verdict specified:

1. **F1 (ratio)** — took the reviewer's own measured recommendation exactly:
   reverted `.pos-container` to `4fr:3fr` (matching `main`) and added
   `@media (max-height: 640px) { .pos-container { grid-template-rows:
   minmax(8rem,1fr) minmax(0,1fr); } }` for the genuinely short case,
   instead of a permanent ratio flip. Taller viewports (1280x800, 1194x834)
   now keep the full `4:3` products-dominant split the reviewer showed was
   already sufficient once `auto-fit` is in place.
2. **F2 (comment)** — the ~50-line block above `.pos-container`'s
   `grid-template-rows` now records the 2026-08-30 correction inline: the
   centre-vs-bottom-edge measurement discrepancy, why the first `3fr:4fr`
   attempt was wrong (dominated by `1fr:1fr`, re-broke #1231's own
   reference-resolution symptom), and why `auto-fit` invalidates #1231's
   original reason to reject a gentler split. The two stale/incorrect
   claims are also fixed: app.css:1284's kiosk-parity comment no longer
   cites "8.5rem" as the base (now describes the value as whatever the
   base rule currently is, so it can't go stale the same way again), and
   the `.tile-name` comment no longer asserts a clamp that didn't exist.
3. **F3 (German overflow)** — `.tile-name` now actually has
   `display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient:
   vertical; overflow: hidden; overflow-wrap: anywhere;` (previously
   nothing — the reviewer's finding that the comment asserted a
   nonexistent clamp was correct). This is the same clamp pattern the
   basket's `.line-name` already uses elsewhere in this file.
4. **B1 (docs-shots)** — `make docs-shots` run after F1/F2/F3 landed, so
   the regenerated `web/help/img/*/sell.png` shows the fixed layout (all
   three payment buttons **and** product tiles with names/prices visible),
   not the products-starved state the reviewer warned an out-of-order fix
   would ship. `guard-docs-shots.sh` passes.
5. **B2 (hermetic test)** — fixed, but the real cause was two bugs deep, not
   one:
   - The live basket must be cleared **before** draining held sales
     (`POST /api/pos/resume` refuses via `Engine.HasItems()` while a sale
     is in progress) — the reviewer's suggested drain order.
   - **A second bug the reviewer's review didn't reach**, found while
     verifying the first fix against the full suite: `Engine` is one
     shared instance across every spec in this file (not per-browser-
     context), and **each successful resume re-populates the live basket**
     with that held sale's own lines. A drain loop that resets once before
     the loop, then resumes in a bare `for(;;)`, empties exactly one held
     sale and then spins forever re-clicking the next chip — every attempt
     fails with the same `HasItems()` "busy" guard because nothing empties
     the basket back out between iterations. This is not hypothetical: it
     reproduced live, twice, as `page.waitForResponse` hitting the test's
     own 30s timeout with the exact same "Finish or hold the current sale
     first" state visible in the failure screenshot both times. Fixed by
     resetting after every single resume, not just once at the top.
   - Also fixed the test-soundness nit from §5: `hasText: 'Card'` now uses
     an exact-match filter (`new RegExp('^Card$')`) so it can't match
     "Gift Card".

### Second gate pass

| gate | result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...` | clean |
| `go test ./...` | same single pre-existing sandbox failure as the first pass (`TestListenWithFallback_WildcardHostFallsBackToLoopback`), nothing else |
| all 18 `ci.yml` build-job guards | **all pass**, including `guard-docs-shots.sh` (previously B1) |
| Playwright `--project=default`, full suite | **231-232/232 passed**, run three times back to back. The new tender-panel test passes in every run. The one remaining failure is a **different, pre-existing** flake each run (`split-tender-i18n-925.spec.ts`'s fa/RTL case in two runs, `shifts-tips-osk-1272.spec.ts` in the third) — both pass in isolation, both are the same class of shared-till-server cross-spec state leakage as B2 was, logged separately as ut-docs#1315 rather than fixed here (out of scope: neither file was touched by this diff, and `main`'s own baseline already carries one such failure per the first review pass) |
| `web/help/img/en/sell.png` | visually re-checked: Cash/Card/Gift Card and product tiles (names + prices) both fully visible at the manual's 1024x600 capture size |

### Not re-litigated

F4 (4+ payment methods at 1024x600, accepted under ut-docs#161's scroll
precedent) and the other F5 nits (top-edge probe, no-scroll-needed
assertion, `.tender-footer` fixed-column consistency) were left as the
first pass recommended — non-blocking, and now tracked as their own board
cards (ut-docs#1311 progressive-disclosure payment UI, ut-docs#1312 held-
sales height budget, ut-docs#1313 products-panel scroll affordance,
ut-docs#1314 basket column starvation, ut-docs#1315 e2e cross-spec state
leakage) rather than folded into this diff.

**Updated verdict: safe to merge.**
