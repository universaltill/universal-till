# Code review — basket item-name column starved by fixed QTY/PRICE/TOTAL widths

- **Date:** 2026-08-30
- **Ticket:** ut-docs#1314 (4th attempt at this bug; three prior failed fixes
  are recorded in `web/public/app.css`'s `.basket table` comment)
- **Branch:** `fix/1314-basket-item-name-column-width`
- **Reviewed commit:** `9b66655` ("WIP: pre-review snapshot for ut-docs#1314")
- **Reviewer:** independent pass, different model from the implementer, run in
  an isolated worktree (per the `reviewer` skill's revert-then-restore rule,
  ut-docs#386).
- **Verdict: SAFE TO MERGE**, with one reviewer commit that is provably
  comment-only (see "What I changed").

## What shipped

CSS-only behaviour change in `web/public/app.css`, plus its test and manual:

- `.line-inputs` becomes `flex-direction: column` — the qty input now sits
  **above** the line-discount input instead of beside it. This is a deliberate
  reversal of the ut-docs#213-era side-by-side rule.
- The QTY table column drops from `8rem` to `4.3rem`; `.line-item`'s
  `max-width` rises from `9.875rem` to `10.4rem`. `table-layout: fixed` gives
  the one width-less column (ITEM) all the freed space.
- `.qty-input`/`.disc-input` gain `line-height: 1.2` and shave padding
  `.3rem` → `.25rem`. This is load-bearing, not cosmetic: without it the
  stacked pair makes rows 87.8px and ut-docs#213's ">=4 lines visible" AC
  breaks.
- `.line-name` gains `overflow-wrap: anywhere` for long German compound names.
- `.line-thumb { display: none }` at ≤480px so the phone-tier item column is
  not consumed entirely by the 2rem thumbnail.
- New spec `e2e/tests/basket-item-name-width-1314.spec.ts` (4 tests).
- Regenerated `web/help/img/*/sell.png` + one `web/help/en/sell.md` sentence.
- `e2e/playwright.config.ts` gains a guarded pre-installed-Chromium
  `executablePath` fallback.

No Go code is touched — confirmed, the diff contains zero `.go` files.

## TDD claim: independently re-verified

I reverted **only** `web/public/app.css` to `main`'s version, kept the new spec,
and re-ran it. Genuine red, with the claimed symptom and real numbers:

```
4 failed
Error: "Cheddar Cheese 400g" must not be vertically clamped away
       (scrollHeight 57 vs clientHeight 38, rendered width 47.15625px)
  Expected: <= 39   Received: 57

Error: "Doppelrahmfrischkäse 200g" must wrap (overflow-wrap), never clip
       mid-word horizontally (scrollWidth 169 vs clientWidth 47)
  Expected: <= 48   Received: 169
```

Restoring the CSS returns it to green: **4 passed (10.9s)**. The test fails for
the right reason (a 47px name box, exactly the starvation the ticket describes),
not for an incidental one. Tree was returned to a clean `9b66655` afterwards and
verified with `git diff HEAD` (empty).

## Independent measurement, not the Dev report's numbers

I booted real tills on spare ports in both modes (`UT_KIOSK=0` and `UT_KIOSK=1`),
rang up six lines, and measured the live DOM at `main` vs. this commit.

**Item name width — the bug being fixed (px, `main` → this commit):**

| viewport | non-kiosk | kiosk | clipped after? |
|---|---|---|---|
| 1280x800 | 47.2 → **113.2** | 47.2 → **113.2** | no |
| 1024x600 | 44.8 → **107.7** | 44.8 → **107.7** | no |
| 901x600  | 44.8 → **107.7** | 44.8 → **107.7** | no |
| 360x800  | 0 → 19.8 | 0 → 35.1 | still yes |

The fix works, and works by ~2.4x at every viewport from 901px up, in both
modes. The item column itself goes 102.5px → 168.6px at 1280x800.

**Fully-visible basket lines — the cost (`main` → this commit):**

| viewport | non-kiosk | kiosk |
|---|---|---|
| 1280x800 | 4 → **4** | 4 → **3** |
| 1024x600 | 1 → **1** | 2 → **1** |
| 901x600  | 1 → **1** | 2 → **1** |
| 360x800  | 1 → **1** | 1 → **1** |

**Finding (accepted, not blocking): the disclosed kiosk trade-off was
understated.** Dev reported "3 instead of 4 at 1280x800". True, but at the
*documented kiosk floor* 1024x600 — the 7" Pi hardware this product targets —
kiosk drops **2 → 1**, which was not reported. Non-kiosk has **no row-count
regression at any viewport**; the line-height/padding shave absorbs the
stacking exactly.

I accept it rather than block, because at that same 1024x600 kiosk
configuration the pre-change state was two rows of `"Chedd Che…"` at 44.8px —
two lines the operator cannot actually verify — versus one fully readable
107.7px name plus a scroll. Nothing is blocked either way: `.basket-scroll`
scrolls, `.totals` is pinned, and `.basket-count` carries the quantity. But the
number is now corrected in the CSS comment, because that comment is this bug's
institutional memory and a 5th attempt would otherwise be misled.

**Unreported improvement, verified:** at 360x800 this change also takes
`.basket-scroll`'s horizontal overflow from **32px → 0** (16px → 0 under
`body.kiosk`) — i.e. it repairs a live violation of ut-docs#391's own
no-horizontal-scroll invariant that existed on `main`.

## Adversarial checks

- **Does the column budget actually add up?** Yes. The comment's "`.35rem`*2
  cell padding" is right: `.basket td { padding: .4rem .35rem }` at line ~1050
  wins over the earlier `.55rem .6rem` (later in the cascade, equal
  specificity). `3.4 + 0.7 + 0.2 headroom = 4.3rem`. `.line-item`'s 10.4rem =
  `26.25 - 4.3 - 4 - 4 - 2.8 - 0.7 = 10.45`. With `* { box-sizing: border-box }`
  the 3.4rem input fits the 4.3rem column's content box.
- **`table-layout: fixed` interaction:** no subtlety missed. ITEM remains the
  single width-less column, which is the unambiguous case; attempt 2's
  equal-split trap needs *multiple* undeclared/`calc()`'d columns. Measured
  rendered column widths match declared ones.
- **`overflow-wrap: anywhere` regressing normal names:** no. It only breaks a
  word that does not otherwise fit. `"Coca-Cola Can 330ml"` renders unclipped
  at 113.2px and wraps at the space — visually confirmed in the regenerated
  `web/help/img/en/sell.png` ("Coca-Cola Can" / "330ml").
- **`.line-thumb { display: none }` fallout:** the only other references are
  `web/ui/partials/basket.html` and `e2e/tests/catalog-image-to-till.spec.ts`,
  which runs at 1280x720 (>480px) and is unaffected. The `.line-thumb-empty`
  placeholder shares the class so it hides consistently. The `<img>` carries
  `alt=""` (decorative), so nothing is lost for screen readers.
- **RTL:** `align-items: flex-end` on a column flex is writing-mode relative,
  so the pair hugs the inline-end edge in both directions. Confirmed visually
  in the regenerated `web/help/img/fa/sell.png`; `rtl.spec.ts` passes.
- **The two recurring pipeline bug classes** (a file-write handler missing
  `os.MkdirAll`; a cwd-relative path where `paths.Data(...)` belongs):
  genuinely N/A — verified, not assumed. The diff adds no Go code, no file
  writes and no disk paths.
- **i18n:** no template or locale file is touched; zero new `T "…"` keys were
  needed and none were smuggled in. `guard-i18n.sh` passes (1315 keys).
- **Seed/demo data & secrets:** test fixtures are generic products
  ("Cheddar Cheese 400g", "Doppelrahmfrischkäse 200g"). No real client or shop
  name, no secret-shaped literal.
- **Manual currency:** `web/help/en/sell.md`'s edited sentence ("a small
  discount box just below its quantity box") matches the regenerated
  screenshot truthfully. `fa`/`ar`/`tr` `sell.md` are 62 lines to `en`'s 122
  and do not contain this paragraph at all, so there is no stale translated
  prose — a pre-existing translation lag, out of scope here.

## UX gate

- **Kiosk touch targets are untouched**, verified by measurement rather than by
  reading the rule: `body.kiosk .qty-input, .disc-input { min-height: 2.1rem }`
  still wins, giving **37.5px at 1280x800 and 35.7px at 1024x600 — identical
  before and after**.
- **Non-kiosk inputs do shrink**, which the original comment did not say:
  34.7 → 29.1px at 1280x800, 33.1 → 27.8px at 1024x600. This matters because
  ut-docs#1021 confirmed a real deployed till running `window_mode=normal`
  with the kiosk service inactive — a touchscreen that never gets
  `body.kiosk` (the same finding that drove ut-docs#1170). Accepted, not
  blocked: these controls were **already** below this product's 44px minimum
  (33–35px), so the shave lowers a floor that was never met rather than
  breaking one that was, and it stays above WCAG 2.2 AA 2.5.5's 24px. Filed as
  a follow-up below.
- No new hardcoded colors/spacing tokens, no new modal blocker on the checkout
  path, logical properties throughout.

## What I changed

One commit, **comment-only in `web/public/app.css`**. I proved it carries no
behaviour change by stripping all comments from both versions and hashing the
remaining rules — identical (`7a1e76b8ff12415083f6437225010107`). Independently
corroborated by `make docs-shots`: **not one screenshot PNG changed**, only the
manifest's surface hash.

1. Corrected the kiosk trade-off comment with the re-measured numbers,
   including the 1024x600/901x600 kiosk floor going 2 → 1 that the first pass
   omitted, and the full before/after row-count table for both modes.
2. Amended the "touch height on kiosk hardware is untouched" note so it cannot
   be read as "no touch target moved anywhere" — it now records the non-kiosk
   shrink and the ut-docs#1021 reason that matters.
3. Recorded the previously unreported 360px horizontal-overflow repair
   (32px → 0) in the phone-tier comment, with an explicit warning not to "fix"
   the remaining ~20px name width by widening the item column here, which is
   exactly attempt 3's regression.
4. Regenerated `web/help/img/manifest.json` — `guard-docs-shots.sh` hashes
   `web/public/**`, so even a comment-only CSS edit invalidates it.

## Verification matrix

| Check | Result |
|---|---|
| `gofmt -l .` | clean (no output) |
| `go build ./...` | OK |
| `go vet ./...` | OK |
| `go test ./...` | all packages pass |
| new spec `basket-item-name-width-1314` | 4 passed |
| reverted-CSS run of the same spec | 4 failed, correct symptom |
| e2e regression matrix (7 specs) | **43 passed, 0 failed** |
| — incl. `sale-screen-213` ">=4 lines at 1280x800" | pass (the guard attempt 3 broke) |
| — incl. `basket-no-horizontal-scroll-391` full matrix | pass (caught a previous ui_scale-2 failure) |
| — incl. `ui-scale-basket`, `phone-width-layout-413`, `rtl` | pass |
| all 17 CI-blocking guards | pass |

`e2e/tests/catalog-image-to-till.spec.ts` fails in this sandbox — **verified
pre-existing**: it fails identically at `main` (`img.thumb` `complete=false` on
the catalog page, an image-decode issue unrelated to the basket). Not caused by
this diff.

## Deferred / follow-ups (not blocking)

1. **Phone tier still starved (360px).** After hiding the thumbnail the item
   column gets ~31.6px and the name ~19.8px — roughly two characters. Dev's
   scope call is correct and I agree with it: fixing it properly means
   re-budgeting PRICE/TOTAL/remove or giving the phone tier its own row
   layout, which is a different change from this card, and this diff makes the
   tier strictly better (name 0 → 19.8px, horizontal overflow 32px → 0) rather
   than worse. Worth its own Backlog card.
2. **Kiosk basket row count at the 1024x600 floor (2 → 1).** Accepted here for
   the reasons above, but recovering it without re-starving the name needs a
   kiosk/phone-tier row layout. Worth a card, and worth an e2e guard under
   `body.kiosk` — there is currently **none**, which I confirmed rather than
   assumed (no spec asserts basket row counts with the kiosk class applied).
3. **Non-kiosk basket qty/discount touch targets are 27.8–29.1px**, below the
   44px minimum this product holds elsewhere. Pre-existing, made ~5px worse
   here. Cannot be fixed with padding alone without re-breaking ut-docs#213's
   ">=4 rows" AC.
4. **`e2e/playwright.config.ts`'s `executablePath` fallback is unnecessary in
   this sandbox** — I verified the suite launches Chromium fine at `main`
   *without* it, because `PLAYWRIGHT_BROWSERS_PATH` already resolves
   `chromium-1194`. It is a guarded no-op and harmless, but it hardcodes an
   environment-specific absolute path into shared repo config and is unrelated
   to #1314. Left in place rather than removed, since I cannot test the other
   runners it may have been added for; flagged for the orchestrator to decide.

## Safe to merge

Yes. The fix is real, measured, and correct at every viewport from 901px up in
both modes; the TDD claim holds under independent revert; the regression guard
that attempt 3 broke passes with margin; no Go code, i18n, data-access or
compliance surface is touched; the manual and screenshots ship with it. The one
real finding was an understated trade-off in the durable CSS comment, which I
corrected in place without changing a single CSS rule.
