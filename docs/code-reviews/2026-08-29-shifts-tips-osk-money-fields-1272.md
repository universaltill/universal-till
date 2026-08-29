# Code review — five more money fields silently broken via the on-screen keyboard (ut-docs#1272)

- **Date:** 2026-08-29
- **Branch:** `fix/1272-osk-money-fields-shifts-tips`
- **Reviewer:** independent reviewer (fresh-context, different model — Opus,
  this pipeline's `complexity:medium` review tier)
- **Verdict: SAFE TO MERGE AS-IS.** No blocking issues; reviewer made no
  code changes.

## What shipped

Follow-up from ut-docs#1249's own review (F4 there), which found the
identical defect in five more fields across three dialogs, all
`type="number"` + `onchange` into a separate hidden minor-units field:

- `web/ui/pages/shifts.html`: `#opening-cash` (open), `#closing-cash` +
  `#skim-pounds` (close), `#adjust-pounds` (cash adjustment)
- `web/ui/partials/reports_tab_tips.html`: `#tips-amount` (tips add)

Same two compounding bugs as `#pfand-amount` (ut-docs#1249):

1. `onchange` never fires for osk.js's `input`-only keystrokes, so the
   submitted amount was always empty on a real touch till.
2. `type="number"` silently resets `.value` to `""` on a momentarily-invalid
   decimal string while typing.

Plus a third, found alongside (not present in #1249's own fix): each field
hardcoded `Math.round(parseFloat(v) * 100)` instead of reusing
`window.utCurrency.toMinor()` — wrong by 100× on any 0-decimal currency
(IRR/IRT/IQD/AFN/JPY).

**Fix**, same pattern as `#pfand-amount`: `type="text" inputmode="decimal"`
with a numeric `pattern`, captured on `oninput` instead of `onchange`,
converted via `window.utCurrency.toMinor()`. `#adjust-pounds`' pattern
additionally allows a leading `-` (unlike the other four) since a cash
adjustment can legitimately be negative — the server's
`RecordCashAdjustment` (`internal/pages/shifts_api.go`) requires
`type=payout`/`skim` to be negative and allows `type=adjustment` either
sign. No backend change.

New e2e spec `e2e/tests/shifts-tips-osk-1272.spec.ts` drives all five
fields through osk.js's real on-screen keys (not `.fill()`/`.type()`):
shift open → cash adjustment → shift close (counted cash + skim) as one
flow, plus a separate tips-allocation test.

## Verification performed

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` | empty / pass |
| `go test ./...` (full suite) | pass |
| All CI-blocking guards in `ci.yml`'s `build` job | pass |
| `make docs-shots` (surface hash changed) | regenerated, `guard-docs-shots.sh` passes |
| New e2e spec + full OSK/shift/tips-tagged subset (40 specs) | 40/40 pass |
| Full e2e `default` project (independent reviewer run) | 205 passed, 2 failed — both pre-existing and unrelated (confirmed by re-running with `--grep-invert` on the new spec: identical 2 failures, `catalog-image-to-till.spec.ts` and `split-tender-i18n-925.spec.ts`) |

### TDD claim, verified twice (Dev, then independently by Reviewer)

Both new tests fail against the unfixed markup for exactly the diagnosed
reason — `#opening-cash-minor` stuck at the untouched carry-forward prefill
(`"0"` instead of `"15025"`), `#tips-amount-minor` stuck empty (`""` instead
of `"750"`) — and pass once the fix is restored. Reviewer reproduced this
independently via `git stash` on the two production template files only,
leaving the new spec in place.

## Findings and disposition

1. **Nit — `#adjust-pounds`' pattern rejects a leading-dot decimal
   (`".5"`)**, which `type="number"` used to accept. Identical to the
   `#pfand-amount` precedent (same rule already accepted there), so this is
   consistency with the established pattern, not a new gap. **Accepted
   as-is.**
2. **Non-blocking — `tips-amount`'s dropped `min="0.01"` makes a
   silently-swallowed server error newly reachable.** Server-side is
   correctly guarded (`amountMinor <= 0` → 400 in
   `internal/pages/reports_page.go`), so no bad row can ever be written.
   But `reports.html` has no `htmx:responseError` handler the way
   `shifts.html`/`index.html` do, so a "0" submission (now pattern-valid,
   previously blocked client-side) 400s with no visible feedback — the
   button reads as dead. Pre-existing gap on this page (the same swallow
   already applied to a 500), just made slightly easier to hit. **Filed as
   a follow-up**, not fixed here (needs a new error target + a
   localization decision on the raw `http.Error` body — a design call).
3. **Non-blocking — the page is not currency-aware, and this fix doesn't
   change that.** `window.utCurrency.toMinor()` handles 0-decimal
   currencies correctly (`Math.round(Number(v) * 10**decimals)`), but the
   hardcoded `pattern="[0-9]+(\.[0-9]{1,2})?"` and `(£)` labels don't
   branch on `currency.Decimals` the way `#pfand-amount`'s did, and
   `shifts_page.go`'s `CarryForwardDisplay` independently hardcodes `/100`
   regardless of the real currency. Two bugs that used to cancel out under
   the old JS no longer do under the new one, for a 0-decimal shop
   specifically. `/shifts` already hardcodes `£` in three labels — this is
   a whole-page currency-awareness gap, not something to patch inline
   here. **Filed as a follow-up.**
4. **Informational — a new, out-of-scope finding: the identical
   `type="number"` decimal-corruption root cause (bug 2 above, not bug 1)
   is still live on ~15 other fields** (`promotions.html`, `settings.html`
   fee fields, `inventory.html`'s `#stock-cost`, `tax_codes.html`,
   `country_settings.html`) — these submit via `name=` directly so bug 1
   doesn't apply, but a typed decimal can still silently corrupt to the
   wrong value. Arguably worse than what #1272 fixes (wrong, not just
   empty). Admin screens, not the till/kiosk flow, clearly outside this
   card's scope. **Filed as a follow-up.**
5. **Informational — `osk.js`'s numeric layer has no `-` key**, so a
   negative payout/adjustment can't actually be typed on a touch till
   today, on the fixed markup or the unfixed. Real, pre-existing, unrelated
   to this diff (the old `type="number"` markup hit the identical dead
   end) — noted in the diff's own comment and the e2e spec, not blocking.
   **Filed as a follow-up.**
6. No missed fields — a repo-wide grep after the fix finds zero remaining
   `Math.round(parseFloat` or `onchange=…-minor` occurrences.

## Checked and found clean

- No i18n change (comments only; `guard-i18n.sh` green).
- No compliance-claim wording touched.
- Pure template/JS — no SQL, no `internal/` change; data-access and
  kiosk-engine guards trivially satisfied.
- Money handling strengthened, not weakened: the JS side now delegates to
  the single shared `toMinor()` helper instead of a hardcoded `×100`.
- Nothing network-dependent added — offline-first intact.
- No new CSS — no RTL/logical-property concern.
- No real shop names or secret-shaped literals in the new spec.
- `guard-docs-shots.sh` green with the regenerated surface hash;
  `guard-help-topics.sh` green; no help prose describes the changed input
  mechanics, so nothing went stale.

## Follow-up cards filed

- ut-docs#1273 — `/reports` tips form silently swallows every server error
  (no `responseError` handler on that page).
- ut-docs#1274 — `/shifts` + tips amount entry is not currency-aware
  end-to-end, including `CarryForwardDisplay`'s hardcoded `/100`.
- ut-docs#1275 — remaining `type="number" step="0.01"` fields (admin
  screens) corrupt a decimal typed via the on-screen keyboard.
- ut-docs#1276 — osk.js's numeric keyboard layer has no `-` key, so a
  negative amount can't be typed on a touch till.
