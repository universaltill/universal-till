# Code review — deposit-refund Pay Out button never submits a real amount via the OSK (ut-docs#1249)

- **Date:** 2026-08-29
- **Branch:** `fix/1249-deposit-refund-payout-osk`
- **Reviewer:** independent reviewer (fresh-context, different model — Opus,
  this pipeline's `complexity:medium` review tier)
- **Verdict: SAFE TO MERGE AS-IS.** No blocking issues; reviewer made no
  code changes.

## What shipped

Reported live: "after filling the amount and manager pin, the pay out
button on the deposit refund not working." Root cause was two compounding
bugs in the Pfandrückgabe dialog's amount field (`#pfand-amount`,
`web/ui/pages/index.html`):

1. **The submitted amount never updated when typed via the kiosk's
   on-screen keyboard.** `#pfand-amount` (`type="number"`) only copied its
   value into the hidden field actually POSTed (`#pfand-amount-minor`,
   `name="amount"`) via an `onchange` handler. `osk.js` types by mutating
   `.value` and dispatching only `input` — never `change` — and a
   script-set `.value` never sets a control's "dirty value" flag, so blur
   can't synthesize `change` either. On a real touch till (osk.js's
   default/auto mode) the submitted amount was therefore always empty, and
   every payout 400'd with "amount must be greater than zero" — read by
   the reporting user as "the button does nothing."
2. **Compounding: `type="number"` silently resets `.value` to `""` on a
   momentarily-invalid intermediate string.** osk.js's `insert()` does
   naive `value += text` for number-type inputs (no `setRangeText`/
   selection available for that type). Typing a decimal point produces an
   invalid interim float string (e.g. "5."), and the browser resets
   `.value` to `""` right then. Verified live (both by Dev and
   independently by Reviewer): typing '2','.','5','0' via osk.js's exact
   code path produces a final `.value` of `"50"`, not `"2.50"` — so a
   naive `onchange`→`oninput` fix alone would have turned a £2.50 payout
   into a **20× wrong £50 cash payout**, not just a dead button.

**Fix:** `#pfand-amount` is now `type="text" inputmode="decimal"` with a
numeric `pattern` (mirrors the existing `currency.Decimals == 0` branching
convention already used for the placeholder), captured on `oninput`
instead of `onchange`. Text inputs never coerce/reset an in-progress
value, and this moves osk.js's `insert()`/`backspace()` onto their
cursor-aware `setRangeText` path (unavailable for number/email inputs).
`window.utCurrency.toMinor()` already degrades any unparsable string to
`0`, so a malformed amount still surfaces the existing, correct 400 —
never a silent wrong charge. No backend change needed.

New e2e spec `e2e/tests/deposit-refund-payout-osk-1249.spec.ts` drives the
dialog through osk.js's actual on-screen `button[data-k]` keys (not
`.fill()`): types a decimal amount + PIN via the OSK and confirms the
payout actually completes (200, dialog closes); types "0" and confirms the
existing "amount must be greater than zero" error still surfaces (not a
silently-dead button).

## Verification performed

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `go test ./...` (full suite) | pass |
| All 29 CI-blocking guards in `ci.yml`'s `build` job | pass |
| `make docs-shots` (index.html is in the hashed surface) | regenerated, `guard-docs-shots.sh` passes |
| New e2e spec + sibling specs (deposit-refund-osk-1248, form-label-layout-300, autofill-suppression-400, phone-width-layout-413, osk-central-guard, settings-osk, rtl, form-input-contrast-305) | 53/53 pass |
| Full e2e suite (independent reviewer run) | 217/219 pass; 2 failures independently confirmed pre-existing/environmental (one a known ordering flake in isolation, one reproduces identically on the pre-fix template) |

### Independent re-verification of the root-cause claim

Reviewer reproduced both bugs from scratch in real Chromium, driving
osk.js's exact `insert()` source against live inputs:

| Step | Result |
|---|---|
| `type=number`, `value += ch` for `2`,`.`,`5`,`0` | `"2"` → `""` → `"5"` → `"50"` |
| `onchange` fired by script-set `.value` + dispatched `input`, then blur | 0 times |
| `type=text` (setRangeText path), same keys | `"2.50"`, hidden field → `250` |

### TDD claim, verified twice (Dev, then independently by Reviewer)

Both new tests fail against the unfixed markup (`#pfand-amount-minor`
receives `""` instead of `"250"`/`"0"`) — restoring the fix returns them to
green. Reviewer's own run killed and restarted the server between runs
(templates are `go:embed`ed, so a reused server would have false-passed).

### Screenshot diff sanity-check

`ar/sell.png`, `fa/sell.png`, `tr/sell.png` changed by 10-22 raw bytes out
of ~1.8MB each (0.001%) — encoder/antialiasing noise, not a content
change; the `<dialog>` is closed on the sale screen and this field is
never painted in the screenshot. `fa/users.png`'s similar-sized diff is
unrelated to this change and has churned the same way across unrelated
prior commits — established harness noise. `manifest.json`'s
`surface_sha256` moving is expected and required (`index.html` is in the
hashed surface).

## Findings and disposition

1. **F1 — Low, pre-existing, not introduced.** `form.reset()` does not
   clear the hidden `#pfand-amount-minor` (a `type=hidden` input's `.value`
   write is the spec's "default value" mode, which `reset()` doesn't
   touch). Harmless today: `#pfand-amount` is `required`, so a stale
   hidden value can never be submitted without a fresh `oninput` overwrite
   first. Identical under the old `onchange` and in all sibling fields
   (see F4). **Not fixed here** — rides along with the F4 follow-up.
2. **F2 — Low, minor UX regression, not blocking.** Dropping
   `type="number"` drops its `min`/`step` native validation messages;
   `pattern` has no `title`, so a malformed entry now trips the generic
   browser message instead of a number-specific one, and `0` now
   round-trips to the server for an English-only "amount must be greater
   than zero" (pre-existing string, not widened by this change — every
   other error on this dialog is already an English literal from
   `respondAdjustmentError`). Test 2 deliberately codifies this round-trip
   as the intended behavior. **Accepted as-is.**
3. **F3 — Informational.** The new spec is the first default-project spec
   to open a shift on the shared e2e till and never close it. Idempotent,
   full suite green today; noted for awareness, not a defect.
4. **F4 — Scope discipline correct, follow-up scope upgraded.** The
   identical `onchange`→hidden-minor-field pattern (and the same
   `type="number"` decimal-corruption exposure) exists in **five** fields
   across three more dialogs, not the two originally flagged by BA/
   Architect: `web/ui/pages/shifts.html`'s `#opening-cash`,
   `#closing-cash`, `#skim-pounds` (shift open/close/skim) and
   `#adjust-pounds` (cash adjustment), plus
   `web/ui/partials/reports_tab_tips.html`'s `#tips-amount` (tips add).
   `#closing-cash` is the sharpest of these: closing a shift on a touch
   till would post an empty counted-cash figure, reading as a large false
   cash variance rather than a visible error. **Additional finding for
   that follow-up card:** all five inline-compute
   `Math.round(parseFloat(...) * 100)` instead of reusing
   `window.utCurrency.toMinor()` — a hardcoded `×100` that is 100× wrong on
   every 0-decimal currency (IRR/IRT/IQD/AFN/JPY). This fix correctly uses
   `toMinor()` already. None of the five share code with `#pfand-amount`,
   so leaving them unfixed here does not undermine this fix. **Filed as
   universaltill/ut-docs#1272** (p1, given the `#closing-cash` risk).

## Checked and found clean

- `osk.js` still recognizes the field: `wantsOSK()` accepts `type="text"`;
  `isNumeric()` reads the saved `oskPrevInputmode="decimal"` → numeric
  layer; `guardField()` still suppresses the native IME (numeric fields
  bypass the `localeSupported()` gate).
- `autofill.js`'s `TEXTY_TYPES` covers `text` and `number` identically —
  ut-docs#400 suppression unaffected.
- No CSS rule targets this field by type; layout unaffected (verified live
  in en and fa/RTL, light theme).
- Real (non-OSK/physical-keyboard) entry verified live: `12.34` → hidden
  `1234`, `checkValidity()` true.
- `currency.Decimals` is only ever `0` or `2` across
  `internal/httpx/currency.go`'s whole table (unknown-code fallback = 2),
  so the pattern's hardcoded 1-2 decimal digits is not a latent bug for any
  currently-supported currency.
- No i18n/RTL surface touched (no new user-facing strings); manual prose
  in `sell.md`/`reports.md`/`display.md` describes the payout above the
  level this change touches — nothing went stale.
- No real shop names, no credentials in test fixtures (`1234` PIN is inert
  on the auth-off e2e till).
