# 2026-09-08 — Scan a Gutschein barcode to resolve the voucher and apply its balance as tender (ut-docs#1833)

## What shipped

Scanning a `GS-`-prefixed code at the sale screen (`/api/pos/scan`,
`internal/pages/pos_api.go`) now resolves it as a voucher instead of
falling through to "item not found":

- New `looksLikeVoucherCode` helper (case-insensitive `GS-` prefix),
  checked immediately after the existing customer-code check and before
  item resolution — a prefix disjoint from `CUST`/`LOY-`/`LOY<digit>` and
  from every digit-only GS1 barcode symbology already in
  `internal/barcode/registry.go`, so a real product/customer scan can
  never be swallowed by this branch (the ordering mirrors the existing,
  accepted `looksLikeCustomerCode` precedent exactly).
- Resolution reuses the pre-existing `repo.GetVoucherBalance` +
  `fetchVoucherFromPrimary` two-step lookup unchanged — same
  offline-first / cross-till behaviour `GET /api/vouchers/{id}` already
  has (ut-docs#1668, ADR-0084). Three distinct outcomes, three distinct
  toasts, no fallthrough to promo/refund/item-not-found in any of them:
  found+active+balance>0, found-but-unusable (empty/inactive), not-found.
- New "pending voucher" state on the sale engine
  (`internal/pos/service.go`): `Basket.VoucherID`/`VoucherBalance`,
  `SetPendingVoucher`/`ClearPendingVoucher`/`PendingVoucherID`/
  `PendingVoucherBalance`, cleared in `resetLocked` at the same point
  `CustomerID`/`CustomerName` already are.
- New pay-grid button (`web/ui/pages/index.html` + a `data-voucher-*`
  bridge on `#basket` in `web/ui/partials/basket.html`) that appears once
  a voucher is pending, posts to the pre-existing, **unmodified**
  `/api/pos/tender` voucher-payment path, and clamps its amount to
  `min(balance, amount still due)` in integer minor units client-side.
- 4 new i18n keys in all four shipped locales (en/fa/tr/ar), naturally
  translated; a matching translation landed in `ut-plugin-language-de`
  (#195) and `ut-plugin-language-es` (#196) *before* this PR, per this
  ecosystem's `lang-pack-drift` rule.
- `web/help/en/sell.md` gained a short scan-to-redeem section (screenshot
  regenerated where the doc changed).

**Explicitly not touched, verified unchanged**: the tender/redemption
handler's core logic, `internal/pages/voucher_sync_proxy.go` (cross-till),
`internal/barcode/registry.go` (symbology registry),
`TestPOSTender_VoucherIssueAndRedeem`.

**Explicitly out of scope, filed separately**: voucher-issuing UI and a
full "vouchers" help topic (ut-docs#1832, in progress under a different
lane); combining a tracked voucher with a second payment method in the
split-tender panel (ut-docs#1851, filed by this review — see below); an
unrelated pre-existing i18n orphan key found while landing the language
packs (ut-docs#1847, filed by this review, not caused by this change).

## Independent review (Opus, isolated worktree)

Found and fixed one **blocker** before this could ship:

- **The pay button's tender request was form-encoded, not JSON, and the
  form-encoded branch of `/api/pos/tender` never read `voucher_id`.**
  No `json-enc` htmx extension is registered anywhere under `web/ui`, so
  the new `#pay-voucher-btn`'s `hx-vals` posted through the same
  form-encoded fallback every other quick-tender button uses — which
  built a `pos.PaymentInput` with no `VoucherID`. Per that handler's own
  doc comment, an empty `VoucherID` means a generic, **untracked**
  voucher payment: the sale completed and the receipt printed, but
  `vouchers.balance` was never debited, no `redemption` row was written,
  and — because the untracked path skips the balance/overspend check
  entirely — a sale for *more* than the voucher's balance was accepted
  outright. Verified end-to-end in a real browser both before the fix
  (balance unchanged after "redemption") and after (balance correctly
  debited), and at the Go level (a 300 redemption against a 100 voucher
  succeeded pre-fix, refused post-fix).
  **Fix**: the form-encoded branch now reads `r.Form.Get("voucher_id")`
  with the same `TrimSpace` + 64-char bound the JSON branch already
  applies, and threads it into the same `pos.PaymentInput.VoucherID`
  field. Three new regression tests
  (`internal/pages/voucher_tender_form_test.go`): tracked debit via the
  form path, overspend refusal via the form path, and the id-length
  bound. Confirmed each fails with the pre-fix code (real assertion
  failures, not comment-only) and passes after.
- **Manual correction**: `web/help/en/sell.md` had claimed a voucher
  worth less than the sale can be topped up "the same way you would for
  any split payment" — false as shipped (see ut-docs#1851). Corrected to
  describe the actual current limitation; screenshot manifest
  regenerated for the changed topic.
- **Coverage gap that let the blocker through**: the original e2e spec
  asserted the button's `hx-vals` payload but never clicked it, so
  nothing exercised the real form-encoded request path. Added a third
  e2e case that clicks the button and reads the voucher's balance back
  through `GET /api/vouchers/{id}` to prove the debit actually happened.

Reviewed and accepted as-is (not blockers):

- A shop using `CODE128`/`CODE39`/`INTERNAL_PLU` item barcodes that
  happen to start with `GS-` would have that scan swallowed as a voucher
  lookup — structurally identical to the already-accepted
  `CUST`/`LOY-` precedent this design follows; no worse than existing
  behaviour.
- A malformed/missing `data-voucher-balance-minor` attribute would
  degrade to `amount: null` → the tender handler's zero-amount fallback
  → the *full* sale total, rather than the intended clamp. Not
  reachable today (both bridging attributes render from Go integers),
  flagged for anyone touching that bridge later.
- `ClearPendingVoucher` has no production caller yet (no UI affordance
  to drop a mis-scanned voucher short of resetting the sale) — minor,
  not a blocker for this card's scope.
- The two regenerated `web/help/img/ar/*.png` differ by ~1 byte from a
  Chromium-version mismatch between this sandbox and the pinned
  `@playwright/test` version (`resolve-chromium.sh`'s own known warning),
  not a real screenshot change.

## Verified beyond the automated suite

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./internal/pages/... ./internal/pos/...` (full packages) —
  all green, independently re-run after the fix, including the new
  `TestPOSTender_FormEncodedVoucher*` tests and the pre-existing
  `TestPOSTender_VoucherIssueAndRedeem` (unmodified, still green).
- `scripts/ci/guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
  `guard-data-access.sh`, `guard-compliance-claims.sh`,
  `guard-page-http-error.sh` — all pass, re-run after the fix.
- `e2e/tests/voucher-scan-pay-amount-1833.spec.ts` (3 cases, including
  the review's added click-through case proving a real debit) — passed
  in a real browser, independently re-run after the fix.
- Manual driven-browser pass (Tester phase): active/inactive/unknown
  voucher scans, en/fa/ar (RTL confirmed correct), kiosk viewport,
  two curated themes — screenshots taken and actually reviewed, nothing
  overlapping/clipped. `tr` locale's translated strings inspected but not
  separately screenshotted.
- Both language-pack PRs (`ut-plugin-language-de#195`,
  `ut-plugin-language-es#196`) merged before this PR, each verified
  locally against this branch's `web/locales/en.json` via
  `UT_CORE_EN_JSON=<path> scripts/check-key-drift.sh` before opening —
  zero new drift once this PR lands.

## Verdict

Safe to merge. The blocker found in review is fixed and independently
re-verified (build/vet/test, guards, and a real e2e click-through) by
this record's own author, not just taken on the review subagent's word.

## Explicitly deferred

- ut-docs#1832 — voucher-issuing UI, full "vouchers" help topic, and
  actual barcode generation/printing at issue time.
- ut-docs#1851 — combine a tracked voucher with a second payment method
  in the split-tender panel (this card's own scan/resolve/single-tender
  path is unaffected and correct on its own).
- ut-docs#1847 — a pre-existing, unrelated i18n orphan key
  (`import.status.name_already_in_catalog`) found in both language packs
  while landing this card's translations.
