# 2026-08-01 — SumUp reader tip auto-sync: payment-authorize response path

ut-docs#43. Companion review for the plugin-side half of this change lives
in `ut-plugin-payment-sumup/CLAUDE.md`/README (that repo keeps its own
review history in-repo per this ecosystem's usual pattern, but has no
`docs/code-reviews/` directory of its own yet — noted as a gap below).

## What shipped

The core `payments.tip_amount` field and `/api/pos/tender`'s `tip` field
already existed (2026-07-28,
`docs/code-reviews/2026-07-28-tip-amount-domain-model.md`) — that PR
explicitly scoped out "a tighter reader-driven live-tip callback" as a
separate, larger change. This is that change:

- **`internal/plugins/ipc.go`**: `EventBus.Publish`'s Blocking mode
  computed a handler's response but discarded it — only accept/reject
  mattered. Refactored into a shared internal `publish` (same dispatch
  loop, same permission-denial/no-subscriber/rollback semantics) plus a
  new **`EventBus.PublishAuthorize`** that also returns the approving
  handler's raw JSON. `Publish` itself is now a one-line wrapper that
  drops the response, so every existing `Publish` caller is unaffected.
- **`internal/pages/refund_page.go`**: `blockingPaymentEvent` (shared by
  the tender-authorize gate and the refund gate) gained a sibling
  `blockingPaymentEventWithResponse` using `PublishAuthorize`.
- **`internal/pages/pos_api.go`**: `completeTender`'s per-payment
  authorize loop now reads a `tip_amount` (int64 minor units) off the
  plugin's response via a new `pluginReportedTipAmount` parser, and
  applies it to that payment's `TipAmount` before `CompleteSale` —
  explicitly, via `saleInput.Payments = payments` (see finding #3 below),
  not by relying on the caller's slice happening to alias.
- Docs: `ut-docs/architecture/wasm-runtime.md` gained an "Authorize
  response data" section; this repo's own `README.md` moved "Tips" from
  not-built to shipped (with an honest caveat on the reader-sync half —
  see finding #5).

## Independent review (opus, different model from implementation)

Full independent pass: ran `go build`/`go vet`/`go test` on both repos,
`scripts/build.sh`/`validate.sh` on the plugin, and personally re-verified
every TDD claim by breaking the fix and confirming the specific test
failed with the expected message, then restoring it and confirming green
again (not taken on the implementer's word).

**1 blocking finding, since fixed** (this was actually in the plugin
repo, not this one — noted here because it changed how this repo's
`completeTender` needed to be tested):

- SumUp's `pollTransaction` originally decoded `tip_amount` as a fixed
  `*float64` field on the same struct used to detect the transaction's
  `id`/`status`. Since that field's real shape is an unverified guess, a
  wrong guess (e.g. SumUp sending an amount *object* like this same file's
  own `total_amount` field, rather than a bare decimal) would fail the
  **whole** JSON unmarshal — silently discarding a real `SUCCESSFUL`
  transaction's id/status too, and declining a card the customer had
  already been charged on. Fixed by decoding `tip_amount` as
  `json.RawMessage` (which cannot fail regardless of shape) and
  interpreting it in a fully separate, always-degrading-to-"no tip"
  helper (`parseTipAmountMinor`). Regression-tested directly
  (`TestParseTransactionPoll_WeirdTipShapeNeverBreaksOutcomeDetection`)
  with the exact object/string/null shapes the review probed.

**3 should-fix findings, all fixed:**

- `pos_api.go`'s tip persistence only worked because both call sites
  happened to pass a `payments` slice sharing `saleInput.Payments`'s
  backing array — a caller passing a defensive copy would silently drop
  every reader-reported tip with no compile error and no test failure.
  Fixed: `completeTender` now sets `saleInput.Payments = payments`
  explicitly before `CompleteSale`.
- Only the self-order kiosk path had a test for this; the cashier
  `/api/pos/tender` path (same `completeTender`, but a different caller)
  had none. Added `TestTenderHandler_AppliesPluginReportedTipFromAuthorizeResponse`.
- The plugin's README/CLAUDE.md claimed the unverified-shape risk was
  "safe to ship... worst case, a real tip silently doesn't sync" — false
  as written before finding #1's fix (it could decline a real payment);
  also didn't mention that a *type* mismatch, not just a *shape*
  mismatch, could inflate a tip 100x. Corrected in both docs.

**2 more should-fix documentation-honesty findings, both fixed:**

- The SumUp reader-checkout request never actually enables tip prompting
  on the reader — this plugin has no code path or documented API field to
  do so; it depends entirely on tipping already being on in the
  merchant's own SumUp device profile. Not previously disclosed. Added to
  the plugin's README "Tips" section and a doc comment on `readerCharge`.
- This repo's own `README.md` moved "Tips" straight to Shipped without
  carrying forward the plugin's own "unverified against a live sandbox"
  caveat for the reader-sync half. Fixed — the bullet now links to the
  plugin's README and says explicitly not to rely on it in production
  yet.

**3 nice-to-have findings, accepted/fixed:**

- `pluginReportedTipAmount`'s doc comment overclaimed "can never invent or
  corrupt a tip" — true for totals/coverage (verified, see below) but not
  bounds; not a new hole since the request-supplied `tip` was equally
  unbounded before this change. Left as-is; not a regression.
- `EventBus.publish`'s Blocking mode is last-subscriber-wins on the
  response (`Ask` is first-non-empty-wins). Intentional today — payment
  entries are 1:1 by method key — documented as a known asymmetry, not
  changed.
- The two new tip tests mutate the process-global `plugins.SharedBus`
  singleton without cleanup, risking leaking a stale handler to whatever
  test runs next in the package (this is exactly the pollution bug this
  PR's own Dev pass hit and fixed for itself mid-implementation — see
  "What was verified beyond automated tests" below). Fixed: both new
  tests, plus the pre-existing decline test they sit next to, now
  `t.Cleanup(bus.ResetSubscribers)`.

**False positives checked and cleared** (see the review's own write-up
for the exact commands/greps run): declined-payment tip-parsing ordering,
tip re-entering `computeSaleTotals`/`netPayments` coverage math, the
`Publish`/`PublishAuthorize` refactor preserving every existing behavior
byte-for-byte, the two recurring file-write/cwd-path bug classes (N/A —
no file I/O in this diff), secrets/real-shop-names, money-type discipline
at every boundary, and scope creep. Also flagged unrelated, pre-existing
`-shuffle`-only test flakiness in `internal/pages`
(`TestReportsPage_RefundsAndNetKPIs`,
`TestCollectProblems_IncludesFailedPluginInstalls`, `TestLowStockBadge`)
verified against baseline (stashed diff) before this PR — not this
change's concern; CI doesn't use `-shuffle`. Worth its own ticket if it
starts mattering.

## What was verified beyond automated tests

- Personally re-verified the TDD claim on `completeTender`'s core fix:
  neutralized `payments[i].TipAmount = money.FromMinor(tip)`, confirmed
  `TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse`
  failed with a real assertion mismatch (not a build error;
  `want tip_amount 150 ..., got 0`), then restored and confirmed green.
- Hit the exact `plugins.SharedBus` global-singleton test-pollution class
  myself while writing the first kiosk-side test (a stale declining
  subscriber from an earlier test in the same file, sharing the same
  fake plugin id/event, made the new approving test 402 even though the
  logic under test was correct) — fixed with `ResetSubscribers()`, then
  the independent review found the same class of risk from the opposite
  direction (this test polluting *later* ones) and it's now closed both
  ways.
- Full `go build ./... && go vet ./... && go test ./...` (universal-till)
  and `scripts/build.sh && scripts/validate.sh && go test ./src/...`
  (plugin repo) green, plus both CI guards
  (`guard-data-access.sh`/`guard-i18n.sh`).
- Confirmed the plugin repo previously had **zero test files** (the whole
  `src/main.go` is `wasip1`-tagged, un-testable under a plain `go test`)
  — this PR's `src/convert.go` extraction (no build tag) is the first
  real, running Go test coverage that repo has ever had.

## Deferred / follow-up (not this task's scope)

- **A real wazero end-to-end test harness for `ut-plugin-payment-sumup`.**
  This plugin's own `README.md`/`CLAUDE.md` claim "verified end-to-end
  against the real host runtime... every branch... run through
  universal-till's actual `WasmRuntime.HandleEvent`" — no such test file
  exists in either repo today. Worth closing with an actual test, not
  relying on the claim. Flagged in the plugin's own `CLAUDE.md`; should
  become its own Backlog card.
- The unbounded `int64` tip amount (nice-to-have #1 above) — matches the
  pre-existing request-supplied `tip` field's own lack of a bound; a
  sensible max (e.g. reject anything above the sale total, or some large
  fixed ceiling) is reasonable future hardening for both, not singled out
  here.
- SumUp reader tip-prompt enablement (documented gap, not fixed): no
  known per-checkout API field exists to turn tipping on; revisit if
  SumUp's docs gain one, or drop back to "merchant configures this
  themselves" as the permanent answer.

## Safe to merge

Yes. All required fixes from the independent review are in, full gate is
green in both repos, and the one blocking finding (a wrong API-shape
guess turning a real approved card payment into a false decline) has a
regression test proving it can no longer happen.
