# Code review — `sale.order_type_prompt_stage` (ut-docs#2282)

- **Date:** 2026-09-16
- **Branch:** `feat/2282-order-type-prompt-stage` (base commit under review: `8e4ac17`)
- **Card:** universaltill/ut-docs#2282 — "Sell screen: setting to choose WHERE
  dine-in/takeaway is asked", `complexity:medium`, `lane:cloud-24`
- **Reviewer:** independent pass (Opus), worktree-isolated, on a diff it did
  not write.
- **Verdict:** **Safe to merge with the review fixes applied.** The diff as
  originally committed was **not** safe to merge: two of its three modes were
  broken in ways no Go test, linter or CI guard could see.

---

## What shipped

A new till setting `sale.order_type_prompt_stage` with three values choosing
*where* the cashier is asked for the sale-level dine-in/takeaway order type:

| value | behaviour |
|---|---|
| `cart_top` (default) | unchanged — the always-visible basket-top toggle |
| `before_sale` | a modal on the first add to an empty basket; toggle still shown |
| `at_pay` | the basket-top toggle is hidden; the modal opens on Pay, and the tender overlay opens only once answered |

- `internal/data/order_type_prompt_stage.go` — the key, the three constants
  and `NormalizeOrderTypePromptStage` (unknown/unset/garbled → `cart_top`).
- `internal/pages/settings_page.go` — `POST /api/settings/sale-order-type-prompt`,
  elevation-gated + audited, mirroring `order-no-scheme` field for field.
- `internal/pages/index_page.go` + `web/ui/layouts/base.html` — the resolved
  stage reaches the client as `body[data-order-type-prompt-stage]`.
- `web/ui/pages/index.html` — `#order-type-modal` (reusing `.modifier-modal`
  /`.modifier-actions`/`.btn primary|secondary`, posting to the existing
  `/api/pos/order-type`) plus the client-side gating script.
- `web/public/app.css` — `at_pay` hides `.order-type-toggle-group` and
  `.order-type-mixed` (deliberately **not** the whole `.order-type-row`, so
  `#table-picker` stays reachable).
- `internal/uislot/slot.go` + `settingsnav_test.go` golden — new settings
  section at order 850, between Order numbers and Barcode types.
- New keys in all four in-repo locales; `web/help/{en,de,fa,ar,tr}/sell.md`
  updated; `e2e/tests/order-type-prompt-stage-2282.spec.ts`.

No server-side behaviour reads the setting outside rendering: `SetOrderType`
and tax resolution are untouched, per the Architect note.

---

## Findings

### 1. HIGH — `at_pay`'s CSS rule was dead: the comment above it closed with `-->` instead of `*/` — **FIXED**

`web/public/app.css`: the explanatory block above the new rules ended with
the HTML terminator `-->`, so the CSS comment never closed. It ran on to the
next `*/` (the end of the ut-docs#1379 block), swallowing both new
selectors. `at_pay` mode therefore still showed the basket-top dine-in/
takeaway toggle it is defined to hide.

Nothing in the repo could see this: `gofmt`/`go build`/`go vet`/
`golangci-lint` do not read CSS, no guard parses it, and a plain
`strings.Contains(appCSS, …)` test would have **passed**, because the rule
text really was in the file — inside a comment.

**Fixed** (one character), plus a regression pin:
`internal/pages/order_type_prompt_stage_css_test.go` asserts the selectors
against the **comment-stripped** stylesheet, and a sibling test fails on any
unterminated comment block anywhere in `app.css`.

Verified by revert-then-restore: with `-->` back in place the new test fails
with *"selector … is not live — it is missing, or it is inside a CSS comment"*,
and the card's own e2e test 4 (`at_pay: no basket-top toggle`) fails on
`expect(locator).toBeHidden()`. Both pass again with the fix.

### 2. HIGH — `before_sale` never actually added the item — **FIXED**

The mode's whole promise ("answering the prompt resumes the intercepted
add") did not work. Reproduced live, then root-caused:

- htmx serializes **and HTML5-validates** the triggering form when the
  request is finally *issued*, not when it was triggered.
- `web/public/app.js`'s ut-docs#1177 handler blanks the scan row's `code`
  input on the native `submit` event, on a `setTimeout(…, 0)` whose own
  comment states it runs *"after the value has already been read and sent"*.
  That is true of an ordinary submit and false the moment `htmx:confirm`
  pauses the request in between.
- Result: the resumed request serialized an **empty**, `required`-failing
  form and htmx halted it silently. Instrumented run:
  `EV htmx:validation:halted path=/api/pos/scan code=""`, basket count `0`.

So on `before_sale` the cashier scanned, answered the prompt, and nothing
landed in the basket — every time, for every entry point that submits a form
(the scan row and the item-customization modal).

**Fixed** in `web/ui/pages/index.html`: the intercept now snapshots the
triggering form's field values when it pauses the request, writes them back
immediately before `issueRequest()`, and restores the cleared state straight
after — so the row ends exactly as an uninterrupted submit leaves it, with
ut-docs#1177's own behaviour intact. Entry points carrying their values in
`hx-vals` (product tiles, suggestions) snapshot to `null` and are unaffected.

Verified by revert-then-restore against fresh e2e servers: with the fix
removed, e2e test 2 fails (`#basket` never contains `Coca-Cola`); restored,
it passes. Two assertions were added to that spec to pin both halves —
the item lands, **and** the scan field ends blank (so ut-docs#1177 cannot
silently regress through this path).

### 3. HIGH (process) — the Dev-side gate was reported green but `guard-docs-shots.sh` was red — **FIXED**

Re-running the gate independently, `scripts/ci/guard-docs-shots.sh` exited
**1** on `8e4ac17` (surface changed; `en/fa/ar/tr` `sell` topic markdown
changed). This is a CI-blocking guard in `ci.yml`'s `build` job, so the
branch as committed would have gone red. The escape hatch
(`update-docs-shots-surface-hash.sh`) did not apply — the settings screen
really does change (new card, new nav row).

**Fixed** by running the real harness (`e2e/scripts/docs-shots.sh`). Only
four PNGs changed — `web/help/img/{en,fa,ar,tr}/display.png`, the `/settings`
screenshot — confirming the reused Chromium is byte-identical to the one that
produced the committed baseline despite the harness's version-pin warning.
The regenerated shot correctly shows the new **Ask dine-in or takeaway** row
in the settings sidebar, at the right position, and mirrors correctly in the
RTL (fa/ar) locales.

### 4. MEDIUM — `at_pay` does not gate the one-tap quick-pay button — **NOT FIXED (out of scope, needs a Backlog card)**

`.quick-pay-btn` (`hx-post="/api/pos/tender"`, ut-docs#1336) charges the
preferred method immediately without opening `#payment-overlay`, so the
`at_pay` intercept — which hooks the `.payment-trigger` click — never runs.
With finding 1 fixed, the basket-top toggle really is hidden in `at_pay`, so
a sale rung through quick-pay completes with the sale-level order type never
asked and silently defaulting to dine-in. In Germany that is the §12 UStG
19%/7% split.

Mitigation today: the per-line dine-in/takeaway controls in `basket.html` are
untouched by this card and still give the cashier a working path, and tax is
computed per line. Dev flagged the exclusion deliberately and the Architect
note scoped the change to the Payment button, so overriding that in review
would be scope-widening — left as a finding.

**This must be closed before ut-docs#2309** (the removal of the per-line
control), which would otherwise remove the only remaining mitigation.

### 5. LOW — `before_sale` prompts on a non-item scan; broader than flagged — **NOT FIXED (accepted, worth a Backlog card)**

Dev flagged that a customer/loyalty code scanned on an empty basket wrongly
opens the modal. Confirmed real by reading `internal/pages/pos_api.go`, and
it is broader than reported: `looksLikeCustomerCode`, `looksLikeVoucherCode`
(`GS-`) and the promo-code branch all take the same `/api/pos/scan` path, so
a voucher or promo scan does the same.

Judged **shippable with a known gap**, not a merge blocker: the intercepted
request still lands after the answer, the order type chosen is a valid,
reversible sale-level choice, the basket-top toggle is still on screen in
this mode, and nothing money-, tax- or data-loss-shaped happens. The only
sharp edge is that *cancelling* drops the customer/voucher link silently.
The clean fix is a design change (prompt *after* an add takes the basket
0→1, rather than intercepting before it), which belongs on its own card.
The code comment was extended to record the broader scope.

### 6. LOW — locale keys were inserted out of sort order — **FIXED**

All four `web/locales/*.json` are strictly key-sorted (2412 keys each); this
diff introduced exactly one out-of-order pair, `elevation.summary.order_type_prompt_stage`
placed before `elevation.summary.order_no_scheme`. No guard enforces this, but
it is a real convention and matters for the language packs' own drift diffs.
Swapped in all four files; each re-verified as valid JSON and fully sorted.

### 7. LOW — stray double blank line in all five `sell.md` files — **FIXED**

Collapsed (exactly one line removed per file; no pre-existing blank-line
runs elsewhere were touched).

### 8. LOW — `at_pay` re-asks on a resumed held/parked order, with no current value shown — **NOT FIXED (accepted)**

The card's must-not-regress note (#1903/#1919, "`before_sale` must not
re-ask on resume") is satisfied: resume does not go through `/api/pos/scan`
and leaves the basket non-empty, so `before_sale` never re-prompts. In
`at_pay` the flag is per-page-load, so a resumed order is asked again at Pay
— arguably correct for that mode (the toggle is hidden, so this is the only
place to see it), but the modal shows two neutral buttons with no indication
of the order type the resumed sale already carries, so the cashier can flip
it without noticing. Minor; noted rather than changed.

### 9. LOW — no error state if `/api/pos/order-type` fails — **NOT FIXED (accepted)**

`orderTypeModalAnswered` returns early on a failed request: the modal stays
open with no message, and in `before_sale` a subsequent Cancel drops the
paused add silently. The endpoint is local (offline-first, no network), so
this is a remote path; the degradation is safe (nothing is charged or lost
beyond the one add) and consistent with the neighbouring dialogs.

---

## Checks that passed

**Recurring bug classes (both N/A, confirmed):** the Go diff contains no
`os.Create`/`os.WriteFile`/`os.OpenFile`/`os.MkdirAll` and no `filepath.Join`
or `paths.*` call at all — it is a settings-table change with no file I/O,
so neither the missing-`os.MkdirAll` class nor the cwd-relative-path-instead-
of-`paths.Data(...)` class applies.

**Repository pattern / money / offline-first.** No SQL outside
`internal/data` (`guard-data-access.sh` green). No monetary value is
introduced. Nothing in the change can block checkout on the network; the
modal is a business-logic prompt in the sale flow, the same category as
`#hold-modal`, not a connectivity/sync blocker — the UX gate-check's
reasoning holds and the reviewer agrees.

**Self-order kiosk.** The stage is only added to `index.html`'s page data, so
`base.html`'s `{{ if .orderTypePromptStage }}` emits nothing on self-order
surfaces and the gating script's `|| 'cart_top'` default applies.
`guard-kiosk-engine.sh` green.

**UX checklist (`ut-docs/reference/ux-guidelines.md`).**
- Design tokens reused; the only new CSS is two `display: none` declarations.
  No new colors, no new spacing, no `left`/`right` literal — RTL-safe by
  construction.
- Rendered and inspected live at 1024×600 and at 360px: the settings card
  fits at both, the `<select>` holds the longest English option
  ("At the top of the basket (default)") without overflow, and it stacks
  cleanly at phone width. The RTL (`fa`) settings screenshot from the
  docs-shots run mirrors correctly with the new nav row fitting.
- Status/lock/exit reachability unaffected: `.modifier-modal` is a 28rem
  non-full-viewport dialog opened with `.show()`, never covering the nav rail
  — confirmed in the captured screenshots (status chips visible throughout).
- No text `<input>` and no `autofocus` in the new modal, as the UX gate
  required.
- **German could not be checked directly** — `de` is an external language
  pack, not `web/locales/`. The settings `<select>` follows
  `settings.order_no.scheme`'s markup exactly, which already carries
  comparable string lengths. The `de` help topic prose *is* in this branch
  and reads correctly.

**i18n spot-check (fa / tr / ar), the implementer's own hand translations.**
All plausible, all genuinely in-language, none left as English — and, more
tellingly, every new string reuses the exact terminology of the
already-shipped `basket.order_type.*` keys in its own locale:
`fa` صرف در محل / بیرون‌بر, `ar` تناول في المكان / تيك أواي, `tr` Burada /
Paket. e.g. `settings.order_type_prompt.stage_at_pay` = fa
"هنگام فشردن پرداخت", tr "Öde tuşuna basıldığında", ar "عند الضغط على دفع" —
all correct renderings of "when Pay is pressed". `guard-i18n.sh` green
(1688 keys resolve, all locales match `en.json`).

**Manual.** `web/help/{en,de,fa,ar,tr}/sell.md` each gained a real paragraph
naming the setting, its Settings path and all three behaviours — read in
full, not just checked for a touched file. Accurate to the implemented
behaviour. One nit accepted: *"the basket shows no dine-in/takeaway control
at all"* is literally about the sale-level control; the per-item icons do
remain, which the very next sentence says explicitly.

**Golden-list update (`settingsnav_test.go`).** Not a blind rubber stamp:
the added row's key matches `uislot.CoreSettings`' new entry, its label is
exactly `en.json`'s `settings.order_type_prompt.title` ("Ask dine-in or
takeaway"), its position matches `Order: 850` between Order numbers (800) and
Barcode types (900), and `Href: "#settings-order-type-prompt"` matches the
new card's `id` in `settings.html`. Independently confirmed against the
regenerated `/settings` screenshot.

**Security / test data.** No secret-shaped literal anywhere in the diff. Test
data is the existing demo-seed Coca-Cola / `5000000000012`; no real client or
shop name.

---

## TDD re-verification (done personally, in an isolated worktree)

| test | what was reverted | result |
|---|---|---|
| `internal/pages/index_order_type_prompt_stage_test.go` | the `data-order-type-prompt-stage` attribute in `base.html` | all three tests fail with real assertion errors (*"expected the default cart_top stage on body"*, *"expected stage \"before_sale\" on body"*, *"expected garbage to fall back to cart_top"*) — no compile error. Pass again on restore. |
| `TestSettingsPage_OrderTypePromptStageControl` | the `#settings-order-type-prompt` card block in `settings.html` | fails *"settings page has no order-type-prompt stage control"*. Passes on restore. |
| `TestOrderTypePromptStage_NeverAffectsOrderTypeOrTax` | see note below | mutation applied: made `/api/pos/order-type` skip the whole-basket order type when the stage is `at_pay`. Fails *"stage \"at_pay\": order type = \"\", want takeaway"*. Passes on restore. |
| `internal/pages/order_type_prompt_stage_css_test.go` (added by this review) | the `*/` → `-->` terminator in `app.css` | fails *"selector … is not live — it is … inside a CSS comment"*. Passes on restore. |
| `e2e/tests/order-type-prompt-stage-2282.spec.ts` | (a) the `before_sale` field snapshot/restore, (b) the `app.css` terminator | (a) test 2 fails, (b) test 4 fails; 5/5 pass with both fixes. |

**Correction to the Dev report's TDD claim.** The tax-regression test cannot
have been "confirmed failing pre-fix" in the red-green sense: on `main` it
does not compile (`data.OrderTypePromptStage*` do not exist), and once the
constants exist the invariant already holds, so it is green. It is a
legitimate regression **pin**, not a red-green test — its teeth were proven
here by mutation instead, and it does have teeth.

**`internal/data/order_type_prompt_stage_test.go`** is a pure unit test of a
new pure function; reverting the function can only ever produce a compile
error, so it was reviewed by reading rather than by revert. Its cases are
correct and cover trimming, case-folding and the unknown→`cart_top` fallback.

---

## Gate (re-run independently, after every fix)

`gofmt -l .` clean · `go build ./...` · `go vet ./...` · `go test ./...` ·
`golangci-lint run ./...` **0 issues** · all `ci.yml` `build`-job guards
**PASS**, including `guard-docs-shots.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
`check-brand-assets.sh`.

`guard-shellcheck-version.sh` could not run — no `shellcheck` binary in this
sandbox. Environmental only: this diff changes no shell script.

`e2e/tests/order-type-prompt-stage-2282.spec.ts` **executed** (Playwright,
reused `/opt/pw-browsers` Chromium) — 5/5 pass. Related sell-screen specs
re-run as a regression check on the touched JS: `basket-hx-sync-race-1337`,
`hold-named-tab`, `sale-screen-osk-scan-submit-1177`,
`sale-screen-scan-focus-search-423`, `new-sale-closes-payment-overlay-1386`
— 13/13 pass.

---

## Follow-ups to file

1. **`at_pay` must also gate the one-tap quick-pay button** (finding 4).
   Blocks / must land before ut-docs#2309.
2. **`before_sale` must not prompt on a non-item scan** — customer/loyalty,
   voucher `GS-` and promo codes (finding 5). Suggested approach: prompt
   *after* an add takes the basket 0→1 rather than intercepting before it.
3. Optional: show the current order type in the `at_pay` modal for a resumed
   held/parked order (finding 8).

Also outstanding, not a code finding: the new `en.json` keys are **brand
new**, so per the reviewer rule the core change merges first and
`lang-pack-drift` goes red on `main` until the
`ut-plugin-language-{de,es}` follow-up PRs land — the same lane owns them in
this cycle.

---

## Verdict

**Safe to merge with the review fixes applied.** Two shipped-mode-breaking
bugs (`at_pay`'s dead CSS, `before_sale` never adding the item) and one red
CI guard were found and fixed, each pinned by a test that was verified to
fail without the fix. Two real gaps (quick-pay in `at_pay`, non-item scans in
`before_sale`) are accepted as documented known limitations with follow-up
cards, neither being money-, tax- or data-loss-class given the per-line
controls still present on this branch.
