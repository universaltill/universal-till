# Code review: Pfandrückgabe manual bottle-deposit cash payout

**Date:** 2026-08-02
**Scope:** `internal/pos/shifts.go`, `internal/pages/shifts_api.go`,
`internal/pages/shifts_api_test.go`, `web/ui/pages/index.html`,
`web/locales/{en,ar,fa,tr}.json`.
**Trigger:** universaltill/ut-docs#248 (split from #47, Germany POS-parity
backlog).

## What shipped

A first-class, manager-gated "Pfandrückgabe" (bottle deposit return) cash
payout, reachable as a button + dialog on the main kiosk/sale screen,
unrelated to any specific earlier sale.

- `pos.CashAdjustmentReasonPfandrueckgabe` — a fixed, non-free-text reason
  constant, so a payout can't masquerade as an arbitrary adjustment.
- `POST /api/shifts/pfandrueckgabe` (`internal/pages/shifts_api.go`):
  requires a session user (401), requires manager-PIN approval via the
  same `AuthorizeManager` pattern `refund_page.go`/`inventory_api.go`/
  `pairing_api.go` already use (429 on lockout, 403 otherwise, the
  *approving manager* becomes the audit actor), resolves the currently
  open shift via the existing `CurrentOpenShift` repo method (404 if
  none), then records the payout via the existing `pos.
  RecordCashAdjustment` — no new SQL, no new table, no migration.
- A new `<dialog id="pfand-modal">` on `index.html`, modeled on the
  existing `#hold-modal` static-dialog pattern.
- New i18n keys in all 4 locale files.

## New tests

5 tests in `internal/pages/shifts_api_test.go`: happy path (payout
recorded, expected cash reduced via a real shift-close computing real
SQL aggregation), manager-PIN-required, wrong/correct-PIN with manager
recorded as actor, no-open-shift rejected (404), validation errors.

## Verification (self, before independent review)

- `go build ./... && go vet ./...`: clean.
- `go test ./...`: green except the pre-existing, already-filed
  `TestSaveCleansUpDirectoryOnWriteFailure` (ut-docs#258, fails under a
  root-run sandbox) — confirmed unrelated.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-emoji-font.sh`: green.
- Mutation-tested personally: disabled the manager-PIN gate, disabled the
  no-open-shift check, flipped the payout's amount sign — each broke the
  test that's supposed to catch it, each reverted and confirmed green
  again.
- Drove the real running app in a real browser (Playwright against a
  built binary, not a rendered-HTML-string assertion). This surfaced two
  real bugs pre-independent-review, both fixed in the same pass: htmx
  doesn't swap non-2xx responses by default and the app's only existing
  override for that is scoped to `/api/pos/` paths, so the dialog's
  specific error text was being silently dropped in favor of a generic
  banner; and the first fix attempt (`isError = false`) also flipped
  `event.detail.successful`, wrongly auto-closing the dialog on error —
  fixed by checking `event.detail.xhr.status` directly instead.

## Independent review

Different-model subagent (Opus), full independent re-verification (own
build/vet/test/guard run, plus its own from-scratch mutation test on the
payout amount sign — reproduced the exact claimed failure symptom
independently). Findings:

- **Real, fixed (blocking):**
  - **100x cash-payout bug on zero-decimal currencies.** The dialog's
    amount field hardcoded `Math.round(parseFloat(this.value||0) * 100)`
    and a literal `(£)` label. `internal/httpx/currency.go` ships IRR,
    IRT, IQD, AFN, and JPY with `Decimals: 0` — for those, minor units
    *are* major units, so entering `500` would have posted `amount:
    50000`, a manager-approved, irreversible 100x overpayment. The
    codebase already has the correct decimals-aware pattern twice in the
    same file (`index.html`'s own tender-amount field, and the global
    `window.utCurrency.toMinor()`/`.display` helper wired via
    `base.html`'s body dataset). Fixed: the amount input's
    `step`/`min`/`placeholder` are now `{{ if eq currency.Decimals 0 }}`-
    gated like the tender field, the onchange handler uses
    `window.utCurrency.toMinor(this.value)`, and the label uses
    `{{ currency.Display }}` instead of a hardcoded `£`. Re-verified live
    (symbol renders correctly for the configured currency; conversion
    now goes through the same helper the rest of the app uses).
- **Real, fixed (should-fix):**
  - The `before-swap` override left `isError` true, which — independent
    review traced through the vendored htmx source to confirm — fires
    `htmx:responseError` regardless of `shouldSwap`, which the app's
    global handler turns into a **persistent** generic red `#pos-alert`
    banner behind the dialog (cleared only by a later *successful*
    request). Fixed: also set `event.detail.isError = false` in the same
    handler — confirmed free (per independent review's htmx-source read,
    and re-verified live) because the close-on-success check already
    reads `event.detail.xhr.status` directly, not `event.detail.
    successful`, so the dialog still correctly stays open on error.
  - `#pfand-result` was never cleared, so a stale confirmation/error
    message from a prior open leaked into the next one. Fixed: cleared
    on both the opening button's `onclick` and the Cancel button.
  - Re-verified all three fixes live via Playwright against a rebuilt
    binary: error case shows the specific message, dialog stays open,
    `#pos-alert` stays hidden, stale text is gone on reopen; happy path
    still shows the success message and closes.
- **Real, documented rather than fixed (deliberately out of this card's
  scope — new Backlog cards filed rather than expanding this diff):**
  - **universaltill/ut-docs#266** — the pre-existing generic `POST
    /api/shifts/adjustment` endpoint has no manager-PIN gate at all and
    accepts an arbitrary `type`/`reason`, so any cashier can already
    produce an audit row byte-identical to a manager-approved
    Pfandrückgabe payout by posting to it directly. The new endpoint's
    gate is real and correctly wired, but it's a UI-level deterrent, not
    a data-layer guarantee, for this reason string specifically. This is
    a pre-existing hole (not introduced here) and Architect's own design
    note explicitly scoped retrofitting that endpoint's gate as separate
    future work — filed as its own card rather than folded in.
  - **universaltill/ut-docs#267** — nothing currently reads the
    `reason` field for reporting (`SumShiftAdjustments` sums amounts
    only); an operator can't yet pull a "total Pfand paid out" figure
    from anywhere but the raw audit JSON blob. The acceptance criterion
    this card actually satisfies is "distinct from sales, identifiable
    by a fixed reason in the audit trail" (both true) — a real reporting
    UI on top of that is a materially bigger, separate feature.
  - **universaltill/ut-docs#268** — `CurrentOpenShift` resolves "most
    recently opened shift across any register," which this diff is the
    first *write* path to rely on (existing usage was read-only, on the
    Shifts admin page). On a multi-register till running concurrent
    shifts, a payout could land against the wrong shift/cashier. Not
    novel to this diff, but flagged because this is the first place it
    matters for a write.
- **Considered and deliberately not filed:** no German locale file
  exists in this repo at all yet (`pfand.*` keeps the German term
  verbatim in all 4 locales, matching Architect's explicit call and the
  reference POS's own convention) — this is the pre-existing, already-
  tracked `universaltill/ut-docs#74` ("German language coverage across
  everything"), not a new gap this card introduces.
- **Accepted as nits (not filed, low value relative to effort):** no
  success-confirmation delay before the dialog auto-closes (operator
  sees the message swapped in but the dialog closes on the same tick);
  no dedicated e2e/Playwright spec committed for this dialog (the
  existing `#hold-modal`/`#pos-alert` specs are the precedent this
  should eventually follow); minor edge cases around a swallowed
  `strconv.ParseInt` error and an unbounded amount (mirrors the
  pre-existing generic adjustment endpoint's own behavior, not a
  regression); no double-submit guard on the form.

## Verdict

**Safe to merge after fixes.** Independent review found one blocking
issue (a real, severe overpayment bug on non-2-decimal currencies) and
two should-fix issues (a persistent misleading error banner, stale
dialog text) — all fixed in this same pass and re-verified live in a
real browser against a rebuilt binary, not just re-asserted. Three real
but out-of-scope gaps were filed as new Backlog cards (#266, #267, #268)
rather than silently left undocumented or used to balloon this diff.
Full gate (build/vet/test/guards) green after every fix.
