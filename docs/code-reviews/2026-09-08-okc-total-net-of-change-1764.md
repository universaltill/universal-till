# ut-docs#1764 — TR ÖKC `total`/`amount` sent to the device is now net of change

Date: 2026-09-08
Branch: `fix/1764-okc-total-net-of-change`
Review: one independent subagent, fresh context, Sonnet (`complexity:easy`).

## The bug

On a cash-with-change tender, the amount published to Turkey's certified
ÖKC fiscal device was the **gross cash the customer handed over**, not the
sale total net of change given. `pos.PaymentInput.Amount` is the physical
cash tendered; `ChangeGiven` is tracked separately and was never
subtracted before the amount reached the device — so a customer paying
5000 for a 3000 sale and getting 2000 change had the device told (and
printing) 5000.

Two call sites carried the same gross value:
`internal/pages/fiscal_device_hook.go`'s `deviceAuthorizePayloadExtras`
(the `"total"` field, summed across payment legs) and
`internal/pages/pos_api.go`'s `completeTender` (the per-leg `"amount"`
field sent to every payment plugin, OKC included).

## The fix

1. `deviceAuthorizePayloadExtras` now sums `p.Amount.Sub(p.ChangeGiven)`
   per leg instead of raw `p.Amount`.
2. `completeTender`'s per-leg payload nets the same way, but **only for
   `fiscal.MethodKeyOKC`** — every other payment method (card, QR, demo)
   keeps its raw `p.Amount`, unaffected since their `ChangeGiven` is
   always zero today. Scoping the netting to the OKC leg keeps it equal to
   `deviceExtras`' `"total"` for a normal, non-split tender, preserving
   `plugins/tax-tr/okc/bridge.go`'s existing `Amount == Total` invariant
   (otherwise a legitimate cash-with-change tender would wrongly trip
   `ErrSplitTender`).
3. **Independent-review finding, fixed in the same branch**: netting
   `ChangeGiven` before `pos.CompleteSale`'s own validation
   (`internal/pos/sales.go`'s `netPayments`, which already rejects
   `ChangeGiven > Amount`) opened a new window — a malformed request could
   reach the OKC plugin's blocking `authorize` call with a negative
   `amount`/`total` *before* that validation ever ran. For any other
   payment method this was harmless (the raw amount sent was unaffected by
   `ChangeGiven`), but for a legally-binding fiscal device a bad
   value reaching it before the till's own guard fires is a real
   compliance risk — a printed receipt can't be un-printed. Fixed by
   rejecting `change < 0 || change > amount` in `pos_api.go`'s
   tender handler, before any payment enters the plugin-authorize loop,
   for every payment method (not just OKC) — this is a general input
   invariant, not something specific to the fiscal-device path, and it
   only rejects requests `CompleteSale` would have refused anyway, just
   earlier.

Amounts stay `internal/money.Money` throughout; `.Minor()` is only called
at the JSON-payload/int64 boundary.

## Tests (TDD — written first, confirmed red, then green)

- `internal/pages/fiscal_device_hook_test.go`:
  `TestDeviceAuthorizePayloadExtras_NetsOutChangeGiven` — a 5000-tendered/
  2000-change payment yields `"total": 3000`, not `5000`.
- `internal/pages/pos_api_test.go`:
  - `TestTenderHandler_OKCPluginReceivesNetOfChangeAmount` — a real
    `POST /api/pos/tender` with `amount:200, change:80` against a 120
    basket drives the actual event bus; the OKC plugin subscriber
    receives `"amount": 120` and `"total": 120`, not `200`.
  - `TestTenderHandler_RejectsChangeGreaterThanAmount` — `change:250` on
    `amount:200` is rejected with `400` **before** the plugin subscriber
    is ever invoked (asserted directly: the subscriber sets a flag if
    called at all).

All three were confirmed failing against the pre-fix code with the exact
predicted wrong values (5000/200/plugin-called-with-negative-amount)
before the fix, and passing after — independently re-verified by both the
Tester step and the independent reviewer via a stash/revert-then-restore
of the two implementation files while keeping the tests.

## Independent review

Fresh-context Sonnet subagent, isolated worktree. Ran `go build`,
`go vet`, `gofmt -l`, and the full `internal/pages`/`internal/pos`/
`plugins/tax-tr` suites (all green), and independently reverted/restored
the fix to confirm the TDD claim itself rather than trusting the report.

**Found one real issue** (described above under "The fix", point 3): the
initial diff netted `ChangeGiven` for the OKC leg without validating it
first, so a malformed `change > amount` request reached the fiscal
device's blocking authorize call with a negative value before
`pos.CompleteSale`'s existing validation could reject it. Verified live
against a scratch request. **Fixed in this same branch** with an
early, method-agnostic `change < 0 || change > amount` rejection in the
tender handler, covered by the new `TestTenderHandler_RejectsChangeGreaterThanAmount`.

No other findings. Confirmed: the OKC-only guard doesn't touch any other
payment method's behavior; the split-tender invariant (`bridge.go`'s
`Amount == Total` check) still holds for a normal tender; no other call
site in the repo references `fiscal.MethodKeyOKC` needing the same fix;
no UI/i18n surface touched (backend payload-construction only); no
secret-shaped literals or real shop names in test data.

**Safe to merge**: yes, after the fix above landed in this same branch.

## Non-goals (unchanged from the card)

`plugins/tax-tr/okc/bridge.go`'s split-tender check, `main.go`, and
`drivers.go` are untouched. No other payment method's amount field
changed. No ADR (straightforward bug fix, not a new architectural
decision). No i18n keys (no new user-facing string).
