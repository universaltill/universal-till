# ut-docs#1231 — sale screen: PRODUCTS gets the dominant vertical share

**Branch:** `fix/1231-sale-screen-products-dominant-space` · **Date:** 2026-08-28
**Repo:** `universal-till` · **Commit under review:** `b273e8f` (one commit on
top of `main`)
**Reviewer:** independent pass — did not write this code, verified rather than
trusted every claim in the commit message and the inline comments.

## What shipped

- `web/public/app.css` — `.pos-container`'s `grid-template-rows` goes from
  `minmax(8rem, 1fr) minmax(0, auto)` to `minmax(8rem, 2fr) minmax(0, 1fr)`,
  plus a ~60-line rewrite of the comment above it.
- `web/public/osk.js` — the document-level `focusin` handler's `hide()` is
  deferred one tick (`setTimeout(hide, 0)`) instead of running synchronously;
  the guard is inverted into an early `return`.
- `web/help/img/{en,fa,ar,tr}/sell.png`, `web/help/img/fa/users.png`,
  `web/help/img/manifest.json` — `make docs-shots` output. Confirmed these are
  image bytes plus the manifest's surface/topic hashes, nothing else.
  `web/public/**` *is* inside the docs-shots surface fileset
  (`scripts/ci/guard-docs-shots.sh` header, `e2e/tests-docs/lib.js`), so the
  regeneration was required, not gratuitous.

No Go, no templates, no locales, no migrations. Nothing here touches money,
tax, persistence, auth, plugin verification or the offline path — so no
blocker-class category applies by construction; the only blocker candidate
would be a functional regression, examined at length below.

## What I verified independently (and how)

Everything below was measured by me, in a real Chromium against the real
served till (`e2e/run-till.sh`, port 8091) or in a standalone reduction.

**1. The grid mechanism, in a standalone reduction.** A 604.8px-tall grid
whose second row holds 330px of content:

| `grid-template-rows` | row 1 (products) | row 2 (tender) |
|---|---|---|
| `minmax(8rem, 1fr) minmax(0, auto)` | 260.0 | 332.0 |
| `minmax(8rem, 2fr) minmax(0, 1fr)`  | 394.7 | 197.3 |

The claimed mechanism is real: the `auto` track is maximized to its
max-content size before the `fr` track sees any leftover, and the new pair
distributes at an exact 2:1. The `fr` ratio resolves as a ratio (rather than
degenerating into a content ratio) because `.pos-container`'s block size is
definite — `body.sale-screen { height: 100dvh }` → `main.container`
`flex: 1 1 auto; min-height: 0` → `.pos-container` `flex: 1 1 auto`
(app.css:88-92), and `body.kiosk .pos-container { height: calc(100dvh -
6.6rem) }` in kiosk mode. Checked, not assumed.

**2. The same numbers on the real sale screen at 1280x800** (demo catalog,
root font-size 17.858px, default UI scale):

| | products | tender | `.tab-panel` client |
|---|---|---|---|
| before (`1fr`/`auto`) | 181.7 | 410.3 | 197 |
| after (`2fr`/`1fr`)   | 394.6 | 197.3 | 107 |

Matches the commit message to 0.1px. The headline claim — products becomes
the dominant row — holds. Same story at 1920x800 (168.3 → 391.6) and
1024x600 (136.0 → 267.8).

**3. Products' 8rem floor is not defeated by the `2fr`,** and the change is a
no-op under real vertical pressure. Reduction, old vs new track list at
squeezed container heights, and the live app with `body.osk-padded` and/or
`--ui-scale: 2`:

| condition | old (products/tender) | new (products/tender) |
|---|---|---|
| reduction h=400 | 128.0 / 259.2 | 258.1 / 129.1 |
| reduction h=200 | 128.0 / 59.2 | **identical** 128.0 / 59.2 |
| reduction h=140 | 128.0 / 2.0 | **identical** 128.0 / 2.0 |
| live 1024x600 + osk-padded | 136.0 / 25.8 | **identical** 136.0 / 25.8 |
| live 1024x600 @ ui-scale 2 | 272.0 / 49.6 | **identical** |
| live 1920x800 @ ui-scale 2 (+osk) | 292.0 / 53.1 | **identical** |

So the comment's central defensive claim — "this rule only changes how much
of the LEFTOVER space goes to tender, not what happens when there isn't
enough to go around" — is **correct**, and I confirmed it rather than taking
it on faith. It also means the reverted 8rem-tender-floor experiment
described in the commit message was correctly abandoned.

**4. `.pos-container` is not shared with any other screen.** Only
`web/ui/pages/index.html:101` uses it; `web/ui/partials/receipt.html:35-37`
overrides it inside a print block. The `@media (max-width: 900px)` and
`(max-width: 480px)` blocks both redefine `grid-template-rows` wholesale, so
the tablet and phone tiers are untouched — `phone-width-layout-413.spec.ts`
passing (10 tests) confirms it.

**5. The osk.js race is real, and the fix genuinely closes it — reproduced
red→green by me, without editing the repo.** I served a pre-fix `osk.js`
(synchronous `hide()` restored) through a Playwright route interception and
drove the reported interaction (OSK mode `on`; tap the scan field, type a
barcode on the OSK, tap the scan row's "Add"):

- pre-fix `osk.js`: **no `/api/pos/scan` request fired at all**, the item
  never reached the basket — the tap is silently swallowed, exactly as
  claimed.
- committed `osk.js`: request fires, `Coca-Cola 330ml` lands in the basket,
  the OSK closes and `body.osk-padded` is removed.

I also confirmed the OSK toggle still behaves under the deferral: with the
keyboard closed, tapping `[data-osk-toggle]` opens it and it is **still open
300ms later**; tapping again closes it.

**6. Gates.** `gofmt -l .` clean; `go build ./...` clean;
`scripts/ci/guard-i18n.sh` and `scripts/ci/guard-docs-shots.sh` both pass
(the latter confirming the regenerated manifest matches the tree).

**7. e2e, including a real `main` baseline.** The three named specs
(`sale-screen-osk-scan-submit-1177`, `tender-panel-reachable`,
`sale-screen-213`): **13/13 pass**. Full `--project=default` suite on the
branch: **199 passed / 2 failed**. Then I checked out `main`'s `web/public`,
rebuilt and re-ran the same full suite: **198 passed / 3 failed**. The two
branch failures (`catalog-image-to-till`, `split-tender-i18n-925`'s fa case)
both reproduce on `main` — the second with the server logging `tender
rejected: payments (50) do not cover total (120)`, i.e. shared-basket state,
not layout. The baseline additionally flaked `settings-pos-notice-918`, which
the branch run passed. So the commit's "matches main's own baseline" claim
holds; the branch is not worse than `main`, and this environment is mildly
flaky in both directions.

*Sandbox note:* Playwright 1.61.1 wants headless-shell 1228 and this box only
has chromium-1194, so I temporarily added `use.launchOptions.executablePath`
to `e2e/playwright.config.ts` to run anything at all. **Reverted** — the only
uncommitted file left in the tree is this review record.

## Findings

### F1 — non-blocking, needs a product-owner decision: the squeeze is now inverted onto the payment buttons

The one thing neither the commit message nor the comment says. The commit
cites, as evidence of the old imbalance, that "the full payment button stack
(Cash/Card/Gift Card/Hold Sale/New Customer) rendered unclipped below it" —
and does not say what happens to that stack afterwards. Measured on the real
till, demo catalog, default UI scale, no OSK:

**1280x800** — the reference resolution the bug was reported from; the tender
panel's visible box ends at y=765:

| button | before | after |
|---|---|---|
| Cash / Card | 491..566, fully visible | 704..779, bottom clipped |
| Gift Card | 575..650, fully visible | 788..863, outside the panel, `elementFromPoint` miss |
| Hold Sale / New Customer | 698..752, fully visible | 821..875, outside the panel, `elementFromPoint` miss |

**1024x600** — an AC resolution, the one `tender-panel-reachable.spec.ts` and
the manual screenshots use; tender ends at y=567, viewport 600:

| button | before | after |
|---|---|---|
| Cash / Card | 431..502, visible and hit-testable | 563..634, **not hit-testable without scrolling** |
| Gift Card, Hold Sale, New Customer | already off-panel | still off-panel |

The regenerated manual screenshot is the plainest evidence:
`web/help/img/en/sell.png` (captured at 1024x600) previously showed **Cash**
and **Card** as two large green buttons; the new one shows the scan row, the
Pay/Split tabs and a 5px green sliver — **the manual's own picture of the
Sell screen no longer contains a single payment button**, while
`web/help/en/sell.md` step 3 still reads "…then choose Pay."

Why this is *not* a blocker: `.tender` keeps `overflow-y: auto` and
`.tab-panel` its 6rem floor, so every button stays reachable by scrolling
inside the panel; `tender-panel-reachable.spec.ts` (which deliberately calls
`scrollIntoViewIfNeeded()` before hit-testing) passes both cases, and I
re-ran them myself. Nothing is unreachable and nothing is silently
zero-height. ut-docs#161's precedent explicitly accepts "scroll to reach" as
the fallback.

Why it still needs a decision before merge: this converts the till's
most-used money action from "always on screen" to "scroll first" at both
common till resolutions, and it is the mirror image of the symptom
ut-docs#161 was filed for. ut-docs#1231 asked for products not to be a
sliver; it did not ask for the pay grid to go below the fold. That trade-off
is a product call, not a CSS one — the 2:1 ratio, or a layout that keeps at
least the first pay row on screen (cap products rather than uncap tender, a
gentler split, or move Hold Sale/New Customer out of the vertical stack),
should be signed off explicitly. Whatever is chosen, the `sell` topic's
screenshot ought to show payment buttons again, and if scrolling the tender
panel becomes expected operator behaviour, `sell.md` should say so (repo
rule: the manual ships with the change, ut-docs#324).

### F2 — non-blocking, should-fix: the deferred `hide()` does not mirror `focusout`'s guard, and can undo a legitimate re-open

The new comment says the deferral "mirrors the same pattern focusout already
uses below". It mirrors the `setTimeout` but **not the guard**. `focusout`
re-checks where focus actually landed before hiding:

```js
setTimeout(function () {
  var a = document.activeElement;
  if (!wantsOSK(a) && (!osk || !osk.contains(a))) hide();
}, 50);
```

the new `focusin` path fires `setTimeout(hide, 0)` unconditionally. Any
`show()` that happens after the `focusin` but before the timer fires is
therefore silently undone. Demonstrated on the real app, one task:

```js
scanRowSubmitButton.focus();  // focusin on a non-OSK-able target -> schedules hide
codeInput.focus();            // focus really lands back on an OSK-able field
codeInput.click();            // -> show(): the OSK opens
```

→ committed build: the OSK is **closed** 200ms later. Pre-fix build: it stays
open. The committed build's end state is the wrong one — focus sits on an
OSK-able field with no keyboard.

The only interactive route into that ordering today is the OSK toggle
(`pointerup` → `target.focus(); show(target)`), and it is protected *solely*
by the `pointerdown` `preventDefault()` suppressing the button's own focus.
That holds in Chromium (verified — item 5 above). It is also precisely the
assumption ut-docs#1219 found WebKitGTK breaking for the sibling behaviour (a
canceled `pointerdown` there does not produce the follow-up `click`). On any
engine where a canceled `pointerdown` still focuses the button, the toggle
would now schedule a hide, `pointerup` would `show()`, and the pending hide
would close the just-opened keyboard one tick later — the toggle racing
itself again, in the opposite direction from the race the comment at
`osk.js:409-415` still describes. That comment is stale for the same reason:
"focusin -> hide() BEFORE the toggle's own activation handler runs" is no
longer the ordering.

Fix is one line — reuse `focusout`'s `document.activeElement` re-check inside
the new timer, which is what the comment already claims the code does. Not
applied here: this is a review, not an implementation pass.

Looked at and found *not* to be problems: nothing else reads `current` or
`osk-open` within a tick of the focusin (`press()` needs a later key event;
`show()`'s `current === el` short-circuit is only reachable from a later
task); `body.osk-padded` living one extra tick has no other consumer in the
repo (`grep osk-padded` → `app.css:292` and `sale-screen-213.spec.ts`, which
sets it itself); the OSK's own keys and the toggle both `preventDefault()`
their `pointerdown`, so neither enters this path; `press('↵')`'s synchronous
`hide(); blur(); requestSubmit()` sequence is untouched.

### F3 — non-blocking, comment accuracy

- **"leaving products pinned at its bare 8rem floor"** (twice in the CSS
  comment, once in the commit message) is not true at the resolution it is
  cited for. Root font-size at 1280x800 is 17.858px, so 8rem = 142.9px, while
  products actually rendered at **181.7px** pre-fix — it was getting the
  leftover after tender's max-content, not sitting on its floor. (It *is*
  exactly at the floor at 1024x600: 136.0px. The mechanism is right; the
  "pinned at the floor" wording is wrong for the reference till.)
- **"tender … already has one [a floor] two layers down (.tab-panel's own
  6rem floor)"** mischaracterizes what that floor does. `.tab-panel`'s
  `min-height: 6rem` floors the scroll *content inside* `.tender` (which is
  `overflow-y: auto`); it does not floor the tender grid **row**, which
  renders at 25.8px at 1024x600 with the OSK open. The conclusion the comment
  draws from it is nonetheless correct — the row collapses identically with
  the old track list (measured, item 3), so nothing regresses; only the
  explanation is loose.

This repo treats comment accuracy as load-bearing (cf. the stale-comment
finding fixed in-branch in `2026-08-28-osk-touch-pointerup-activation-1219.md`),
which is why these are written down rather than waved through.

### F4 — nit: comment shape

`web/public/app.css:468` is 95 columns against the ~78 the rest of the file
keeps, and reads as a sentence spliced into an existing line. More generally,
~60 lines of comment for one declaration, braiding ut-docs#213, #1231 and the
osk.js story together, buries the rule it documents — and the osk.js half is
already told, better, at its own call site in `osk.js`.

### F5 — nit / informational, not this diff's fault

- `web/help/img/fa/users.png` changed by 2 bytes with nothing about `/users`
  changing — regeneration noise, harmless.
- The products panel is *still* clipped mid-tile with no visible scroll
  affordance at 1024x600 (measured `scrollHeight` 527 vs `clientHeight` 266).
  The ticket's wording ("first product tile rendered clipped … with no
  visible scroll affordance") covers that half too; this change makes the
  panel bigger, not self-evidently scrollable. Worth a follow-up ticket
  rather than scope creep here.
- `web/help/img/manifest.json`'s own `_comment` (and `e2e/tests-docs/lib.js`
  line 194) describe the surface fileset as "web/ui/** + non-test
  internal/pages/**.go", omitting `web/public/**` which is genuinely part of
  it. Pre-existing, noticed while checking whether this diff needed the
  screenshot regen at all.

### Checked and found NOT to be a problem

- **Is the osk.js fix too narrow?** I grepped every `pointerdown` /
  `mousedown` / `touchstart` listener in `web/public/*.js` and `web/ui/**`.
  The only others are the bug-report panel's drag head and the table
  designer's drag start — both *intentionally* move things during a pointer
  interaction, and both already `preventDefault()` their `pointerdown` — plus
  three idle-timer resets. There is no other `focus`/`focusin`/`blur`
  listener anywhere in the app, and `app.js`'s only `.focus()` call
  (`amountInput.focus()`) runs inside a click handler, i.e. after mouseup.
  osk.js's focusin really was the only unintentional mousedown-time layout
  mutation, and since the handler is document-level the fix covers every page
  with an OSK, not just the sale screen. Correctly scoped.
- **CSS validity / unit consistency.** `minmax(<fixed>, <flex>)` is valid (a
  flex value is only forbidden as the *min*); `align-items: stretch`, the
  `.8rem` gap and the panels' own `min-height: 0; overflow-y: auto` behave
  identically before and after — verified by measurement, not by reading.
- **Other `.pos-container` consumers / other screens.** None (item 4).
- No secrets, no client names, no compliance-claim wording, no new
  user-facing string (so no `web/locales/**` work), no ADR contradicted, no
  Go/file-I/O so this pipeline's two recurring bug classes (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`) are n/a.

## Verdict

**Approve with fixes.** No blocker-class issue: money/tax, data loss and
security are all out of scope by construction, and there is no
unreachable-control or zero-height regression — the specs that exist for that
failure class pass, on a suite run I did myself and compared against a real
`main` baseline rather than trusting the claim. The CSS change is correct,
its reasoning survives independent measurement, and the osk.js race it
exposed is real (reproduced red→green here) and genuinely fixed.

Before merge I would want:

1. **F1 put to the product owner.** It is a real change to the money path's
   ergonomics at both reference resolutions, it is absent from the commit
   message, and the manual's own Sell screenshot no longer shows a payment
   button. Either sign the trade-off off explicitly (and update `sell.md`),
   or retune the ratio.
2. **F2 fixed** — one line, `focusout`'s `activeElement` re-check inside the
   new timer, so the code actually does what its comment says.
3. **F3/F4 corrected** in the comment text, while it is being touched anyway.

## Fixes applied (post-review, same branch, before merge)

All four findings addressed, verified same way as the original review
(measurement + the full e2e gate re-run), not just asserted fixed:

- **F1 — retuned, not escalated.** This is a continuous UX-ergonomics
  parameter (a CSS ratio), not a business/legal/pricing call, so per the
  architect skill's "more than one legitimate approach → ship the best
  default, don't escalate" rule it was retuned rather than parked for the
  product owner. `3fr:2fr` (the reviewer's own measured example) still
  scrolled Cash off-panel at 1024x600 by ~7px (`elementFromPoint` miss,
  measured directly). `4fr:3fr` is the loosest ratio that keeps Cash/Card —
  every shop's always-present fallback payment methods — hit-testable with
  **zero scrolling** at both reference resolutions (measured: hit-test true
  at 1280x800 and 1024x600), while products still nearly doubles versus
  pre-fix (181.7px → ~280-340px depending on basket state, vs. 394.6px
  under the original, too-aggressive `2fr:1fr`). `sell.md`'s "then choose
  Pay" step needed no prose change — Cash/Card are visible again in the
  regenerated `sell.png` at the manual's own 1024x600 capture size (a
  2-item basket pushes them to the bottom edge but they remain legible and
  hit-testable, matching the direct measurement).
- **F2 — fixed as suggested.** `focusin`'s deferred callback now re-checks
  `document.activeElement` before hiding, identical guard shape to
  `focusout`'s existing sibling. Verified with the reviewer's own repro
  (`btn.focus(); input.focus(); input.click()` while OSK is open) — the OSK
  now correctly stays open, both in a standalone check and re-run inside
  the full `sale-screen-osk-scan-submit-1177.spec.ts` suite (13/13 still
  green, 3 consecutive repeats, zero flakes).
- **F3 — comment corrected.** "Pinned at its bare 8rem floor" (inaccurate
  at 1280x800, per the review's own measurement) is now "squeezed near its
  bare 8rem floor"; the `.tab-panel` floor claim now says explicitly what
  it floors (the Pay tab's scroll content) rather than implying it floors
  the tender row itself.
- **F4 — comment shortened.** ~67 lines down to ~35 for the one
  declaration; the 95-column line is gone (checked: nothing in the
  rewritten comment exceeds ~78 columns). The osk.js half of the story
  stays only as a pointer, not restated — the full explanation lives at
  its own call site in `osk.js`, per the finding.
- **F5 — not this diff's to fix**, per the review's own read (informational
  / pre-existing / out of scope), left as-is; the products-still-clipped-
  at-1024x600 half is real and worth its own follow-up card, not scope
  creep here.

Re-ran the full gate after every fix, not just the case each finding
named: `gofmt -l .`, `go build ./...`, all `build`-job CI guards
(`guard-i18n.sh`, `guard-docs-shots.sh` included), and the full
`--project=default` e2e suite twice more (213/215 both times — the same 2
pre-existing, `main`-reproduced flakes as the original review found,
nothing new). `web/help/img/**` + `manifest.json` regenerated once more
for the final `4fr:3fr` ratio.
