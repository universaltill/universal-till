# Code review: dine-in/takeaway prompt — modal, sale-start, modifier gate (ut-docs#2371)

**Date:** 2026-09-17
**Card:** universaltill/ut-docs#2371 — "Sell screen: 'before first item'
dine-in/takeaway prompt fires after the modifier picker (and is locked
under it), is not asked at New Sale, and is not a centred blocking modal"
**Complexity:** medium
**Implementer:** Sonnet (Dev subagent), Tester fix by the orchestrator (Opus)
**Reviewer:** Opus (independent subagent, isolated worktree, fresh context)

## What shipped

Product-owner bug report against ut-docs#2282's *before the first item*
placement (`sale.order_type_prompt = before_item`). Three defects, one
cause: the prompt was a non-modal `.show()` dialog wired only to the item-add
requests.

- **True modal, centred.** `#order-type-prompt-modal` opens with
  `showModal()`; the `position: fixed; inset-block-start: 8vh; z-index: 500`
  rule is gone, so the UA `dialog:modal` styles centre it and it inherits
  `.modifier-modal::backdrop`. The on-screen-keyboard reason `#hold-modal`/
  `#pfand-modal` use `.show()` for does not apply — this dialog has no text
  input. No new CSS positioning, no left/right literals (RTL verified).
- **Asked at sale start.** `maybePromptAtSaleStart()` runs on load, on every
  `htmx:afterSettle` (covers all three New Sale buttons' `/api/pos/reset`
  swap, leaving the receipt view, and boosted navigation onto the sell
  screen) and on any dialog `close` (capturing listener — `close` does not
  bubble). It bails unless: mode is `before_item`, the *real* `#basket` has
  loaded (index.html ships a `hx-trigger="load"` placeholder — see the
  Tester finding below), it is empty and unanswered, no other
  `dialog[open]` exists (payment overlay, hold, modifier/category picker),
  and the sale-start nag was not dismissed for this sale.
- **`dismissedThisSale`.** Cancel or native Escape suppress only the
  sale-start nag; the per-item and per-Pay gates still fire. Reset on a
  choice, on `htmx:beforeRequest` for `/api/pos/reset`/`/api/pos/tender`
  (so New Sale always re-asks, per the owner), or when a sale is observed
  in progress. `beforeRequest`, not `afterRequest`: with the app's
  `defaultSettleDelay:0` the response swap, settle and the overlay's own
  `hx-on::after-request` all complete before `htmx:afterRequest` fires.
- **Modifier/variant tiles gate BEFORE the picker.** The `htmx:confirm`
  gate now also intercepts a tile's `hx-get /ui/pos/modifiers…`; on a
  choice it re-issues the GET via `htmx.ajax` into `#modifier-modal` and
  `showModal()`s it (skipped if the swap left it empty). The old
  `/api/pos/scan-with-modifiers` intercept stays as a fallback — and since
  the prompt is itself top-layer now, it can no longer be locked under the
  picker even if that path fires.
- Help: `web/help/{en,ar,fa,tr}/sell.md` "When you're asked" paragraph
  updated (translated in-session); `docs-shots` regenerated.

Files: `web/public/app.js`, `web/public/app.css`, `web/ui/pages/index.html`
(comment only), `web/help/*/sell.md`, `web/help/img/manifest.json`,
`e2e/tests/order-type-prompt-placement-2282.spec.ts` (+6 tests). No Go
changes, no new locale keys (so no language-pack follow-up).

## Tester (driven run on a real till, before review)

Real Chromium against a seeded till on :8097, screenshots looked at:
kiosk floor 1024×600, phone 360×740, desktop 1280×800 — dialog centred
(x within 1px of centre at every size), backdrop present, `:modal` true,
tiles behind it not clickable; `fa` RTL at 1024×600 — mirrored correctly,
buttons order flips, no clipping; modifier tile → prompt first, picker
after the choice, basket toggle reflects Takeaway; cash tender → receipt
view shows **no** prompt over it; New Sale from the receipt → prompt;
Escape → dismissed, a rail poll 2.5 s later does not reopen it; first add
after a dismissal → gate prompt → item lands. **Not looked at:** dark
theme (a shop setting, not `prefers-color-scheme`; the dialog reuses
`.modifier-modal`, which the dark theme already styles) and real touch
hardware (emulated only — the change adds no pointer handlers).

**Real bug found and fixed here:** `#basket` is not in the initial HTML, so
the load-time check saw "no basket" as "empty, unanswered" and re-asked
after a reload even though the choice was already made (server-side
`OrderTypeChosen=true`), and nothing closed it. Regression test
`a reload after the choice was made does not re-ask` — red (`Received:
visible`) → guard on `#basket` carrying `data-lines-count` → green.

## Independent review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (blocker for a scanner-first shop) | A wedge/camera scan while the sale-start prompt is open was silently dropped: the window-level keydown buffer submits the scan form regardless of focus, the gate cancels it, then `showOrderTypePromptModal()` returned early on `modal.open` without re-arming the resume — the eventual choice resumed the sale-start no-op. Confirmed live by the reviewer (0 scan requests, basket empty after the choice). | **Fixed**: the resolve is armed before the open guard; only `showModal()` is skipped. New e2e `a wedge scan while the sale-start prompt is open is not lost` — red (Coca-Cola absent) → green. |
| 2 | should-fix | Help text in all four locales said the prompt appears "right after a payment completes"; it appears when the receipt view is left (New Sale / New customer / auto-reset), deliberately never over the receipt. | **Fixed** wording in en/ar/fa/tr + the two code comments. |
| 3 | nit | The full revert makes all new tests fail at the same first assertion, so it proves less than it looks. Reviewer did partial reverts: `show()` → `:modal`/Escape tests fail; gate removed → modifier test fails at the pre-picker assertion. | Accepted; tests are discriminating per bug. |
| 4 | nit (UX) | The rail is inert while the prompt is open, and it now opens at every sale start; focus lands on Cancel. | Accepted — same precedent as `#modifier-modal`; a fast wedge's Enter is `preventDefault`ed by the scan buffer so it cannot activate Cancel (and finding 1's fix makes the scan itself land). |
| 5 | nit | `htmx.ajax(...).then(showModal)` resolves on a 4xx too → empty picker. | **Fixed**: `showModal()` only if the swap left content. |
| 6 | out of scope | `web/help/de/sell.md` never got the #2282 "When you're asked" paragraph and still documents the per-line 🍽️/🥡 control removed by #2309; `guard-help-drift` cannot see it. | Filed as its own card (see close-out). |

Checked and fine by the reviewer: `close` fires as a queued task so
`choiceMade` is set before the capturing listener runs; overlay New Sale
re-asks (probe: Escape-dismiss → sale → Pay → overlay New Sale → prompt,
no stacked dialogs); hold flow; no double-open; `at_pay`/`top`/quick-pay
untouched; self-order pages have no `#basket`/no app.js; `evt.detail.path`
carries the query string in htmx 1.9; Categories-tab clones go through the
same body-delegated gate; OSK hides on focusout; no new keys; no file
writes.

## Verification

- `gofmt -l .` clean; `go build ./... && go vet ./...` clean.
- `go test ./...`: all packages ok except the 4 `internal/pages/catalog`
  image-upload tests, which fail identically on untouched `main` with the
  local Go 1.27/darwin toolchain (known local-only fixture issue; CI on
  Go 1.25 is green) — unrelated to this diff.
- e2e: `order-type-prompt-placement-2282.spec.ts` (10 tests) +
  `sell-screen-categories-tab-2283.spec.ts` (4) → 14 passed; Dev's
  `--repeat-each=2` run also clean. Reviewer independently: 10 passed →
  revert 7 failed → restore 10 passed.
- Guards: `guard-i18n`, `guard-data-access`, `guard-help-topics`,
  `guard-help-drift`, `guard-htmx-loaded`, `guard-docs-shots` — all green.

## Verdict

Safe to merge. Two should-fix findings fixed and re-verified; nits 3/4
accepted with reasons; 6 filed as a card.
