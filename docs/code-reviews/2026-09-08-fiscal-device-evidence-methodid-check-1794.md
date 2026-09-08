# 2026-09-08 — `pickDeviceEvidence` MethodID check: a non-OKC payment plugin can no longer fabricate fiscal-device evidence (ut-docs#1794)

## What shipped

`internal/pages/fiscal_device_hook.go`'s `pickDeviceEvidence` gains a
`methodID` parameter and returns `current` untouched — never parsing the
plugin's response at all — for any payment method that is not
`fiscal.MethodKeyOKC` (manifest key `"okc"`, Turkey's YN ÖKC).

Before this change the function parsed a `fiscal_device` object out of
*any* payment method's blocking-authorize/refund answer. A non-OKC plugin
(card terminal, QR, `demopay`) whose response happened to carry — or
deliberately forged — a `fiscal_device` object had that evidence persisted
to `fiscal_device_receipts` via `recordFiscalDeviceEvidence`, and on the
first such record it flipped `fiscal.signing_device_configured` to `true`
for the shop's country. That flag is the ADR-0048 posture flag that lifts
`BlockedNeverConfigured` — so a shop's fiscal device could be "paired" by
a plugin's say-so, with no certified device ever involved. The gap was
filed out of ut-docs#1788's own review as a LOW-severity follow-up; it is
higher than LOW in the one posture reachable in practice (see the review
finding below).

- `pickDeviceEvidence(current, methodID, resp)` — the MethodID check is
  the *first* statement, so a non-OKC response is never handed to
  `fiscal.ParseDeviceEvidence` at all. Not "parsed then discarded".
- Sale/tender path: `internal/pages/pos_api.go`'s `completeTender` loop
  passes `p.MethodID` — the same value it just used to route
  `blockingPaymentEventWithResponseAndID`, so the identity checked is
  provably the identity that answered.
- Refund path: `internal/pages/refund_page.go`'s `POST /api/refund`
  handler passes `method` — likewise the same value it used to dispatch
  `payment.<key>.refund`.
- Doc comments in `pos_api.go`, `refund_page.go` and `pos_api_test.go`
  that previously asserted "pickDeviceEvidence … has no MethodID check of
  its own" were updated, while preserving why the OTHER two independent
  gates (ut-docs#1768's pre-authorize `hasOKCLeg` leg-identity check,
  ut-docs#1779's per-leg fail-closed evidence check) still deliberately
  do not depend on this one.
- Tests: `TestPickDeviceEvidence_FirstWins` updated for the new
  signature; new `TestPickDeviceEvidence_RejectsNonOKCMethod` (unit),
  `TestTenderHandler_NonOKCPluginForgedEvidence_NotPersistedOrConfirmed`
  (sale path), `TestPostRefund_NonOKCPluginForgedEvidence_NotPersistedOrConfirmed`
  (refund path), plus — added during review —
  `TestTenderHandler_TRShadowMode_NonOKCPluginForgedEvidence_DoesNotPairDevice`
  (see finding 1).

Backend-only. No locale keys, no templates, no routes, no schema, no
migration.

## Independent review

Opus, fresh context, isolated worktree, diff copied in and reviewed
against `origin/main`'s base. **Verdict: SAFE TO MERGE.** One MEDIUM
finding (a test that could not fail on the half of the acceptance
criteria that matters most) and one LOW finding (silent drop) were both
found and fixed in this same branch. No blockers left unresolved.

**Finding 1 (MEDIUM, fixed): the shipped regression tests could not fail
on the `signing_device_configured` half of the acceptance criteria.**
Both new handler tests assert that no country's
`signing_device_configured` flips true — but both run on the default
(GB) shop, where `recordFiscalDeviceEvidence`'s `fiscalDeviceMarketActive`
guard (ut-docs#1750) already refuses to touch that flag for *any*
evidence, fix or no fix. The reviewer confirmed this empirically:
disabling the MethodID check makes both tests fail on their
`fiscal_device_receipts` assertion only — the flag assertion stays green
pre-fix, i.e. it is vacuously true and pins nothing. Acceptance criterion
2 says "must NOT have that evidence persisted **or flip
signing_device_configured**", and only the first half was actually being
tested.

The posture where the flag genuinely was reachable is a **TR shop in
shadow mode with the tax-tr plugin installed and active** — a realistic
pilot state, and the one that turns this card from "a stray row in a
table nobody reads" into a real security defect: shadow mode leaves
`fiscal.KeySystemOfRecord` unset, so ut-docs#1768's `hasOKCLeg` gate does
not apply and a `demopay`-only sale completes; but `fiscalDeviceMarketActive`
IS true (TR + tax-tr active), so the forged evidence both persists AND
pairs the device. **Fixed**: added
`TestTenderHandler_TRShadowMode_NonOKCPluginForgedEvidence_DoesNotPairDevice`,
which asserts TR's `signing_device_configured` stays unset, that no
`fiscal_device_confirmed` audit marker is written, and that no receipt
row is persisted. Reviewer confirmed by revert-then-restore that this
test fails pre-fix with exactly the escalation the card describes:
`TR signing_device_configured = "true"`.

**Finding 2 (LOW, fixed): the rejection was completely silent.** The
original diff dropped a non-OKC leg's `fiscal_device` object with no log
line, no audit row and no counter. A payment plugin attempting to
fabricate fiscal evidence is exactly the event a Turkish shop's support
or audit trail would want to see, and the only trace the till kept was
the *absence* of a receipt row — indistinguishable from nothing having
happened. **Fixed**: the non-OKC branch now emits a
`logging.L().Warnf` naming the offending method and the claimed receipt
number, but *only* when the response actually carried a valid-looking
`fiscal_device` object — a cash/card/QR leg with no such object (the
normal case, every leg of every sale) stays silent, so this adds no log
noise. Costs one extra `ParseDeviceEvidence` call on the non-OKC branch;
`completeTender` already parses every leg's response for ut-docs#1779's
own gate, so this is not new work on the hot path. Consistent with the
`Warnf` the function already emits for the second-OKC-leg case.

**Findings verified and accepted as-is (no change needed):**

- **End-to-end closure, both paths, traced independently.** `pickDeviceEvidence`
  has exactly two call sites repo-wide (`git grep`), both updated.
  `recordFiscalDeviceEvidence` has exactly two call sites, both fed only
  from `pickDeviceEvidence`. `deviceEvidence` (the sale-wide accumulator)
  is read in exactly one place, the `recordFiscalDeviceEvidence` call. So
  there is no path from a plugin response to a persisted receipt that
  bypasses the new check.
- **No other code path trusts a `fiscal_device` object.** Repo-wide
  search for `ParseDeviceEvidence` / `fiscal_device`: the only other
  `ParseDeviceEvidence` callers are ut-docs#1779's and ut-docs#1788's
  fail-closed gates, which both already condition on
  `== fiscal.MethodKeyOKC` themselves and only ever *refuse*, never
  persist. `print_api.go` reads persisted receipts back from the DB, not
  from a plugin answer. `internal/pages/inventory_api.go`'s return path
  hardcodes a `cash` leg and never touches device evidence at all.
- **Kiosk path covered for free.** `self_order_shop.go`'s anonymous
  checkout calls the same `completeTender`, so the fixed call site is the
  only tender path on either surface — verified, not assumed.
- **No sync-side ingestion.** `fiscal_device_receipts` is on
  `sync_admin_repo.go`'s explicit *never-synced* list ("per-sale,
  per-till"), so a replica cannot be handed forged receipt rows over the
  sync bundle either. No second trust boundary in scope.
- **Split-tender orderings re-reasoned.** non-OKC-then-OKC: the non-OKC
  leg contributes nothing, the OKC leg's own evidence is accepted —
  correct, and `TestTenderHandler_SplitTender_EarlierLegEvidenceCannotCoverMissingOKCReceipt`
  still passes. OKC-then-non-OKC: first-wins keeps the real receipt and
  the MethodID check independently blocks the second leg (pinned by the
  new unit test's second half). OKC-then-OKC: unchanged, still governed
  by ut-docs#1779's per-leg gate —
  `TestTenderHandler_SplitTender_SecondOKCLegWithNoEvidenceRefused`
  still passes, confirming the updated comment's claim that #1794 does
  not subsume that gate.
- **Empty / whitespace methodID fails closed.** `""`, `" "`, `"OKC"` all
  fail `!= fiscal.MethodKeyOKC` and drop the evidence. Every degenerate
  input lands on the safe side.
- **No interaction with ut-docs#1795 (case canonicalization), and no
  worsening of it.** The routing layer
  (`blockingPaymentEventWithResponseAndID`, `e.EntryKey != method`)
  already matches the entry key case-sensitively and exactly, and
  `plugins/tax-tr/plugin.json` fixes that key at `"okc"` — so the real
  device path is untouched. A hypothetically wrong-cased key would now
  have its evidence dropped where previously it was recorded: a change in
  the *fail-closed* direction, one more reason to land #1795 as its own
  card, not a regression introduced here.
- **Comment accuracy.** All five updated comment blocks re-read against
  the code they describe. Each correctly re-frames the old "no MethodID
  check of its own" claim as historical ("at the time this check was
  written") and correctly states what #1794 does and does not subsume —
  notably `pos_api.go`'s per-leg gate comment, which is right that #1794
  closes the non-OKC-leg laundering case but leaves the second-OKC-leg
  case entirely to #1779's gate.
- **Scope discipline.** Six files, all in `internal/pages`, all on the
  card's own subject. No drive-by refactors, no unrelated behavior
  changes, no attempt at #1795.
- **Manual needs no update.** `web/help/*/fiscal-device.md` already
  describes the correct behavior — "Take a sale with **Yazarkasa (ÖKC)**
  as the payment … The first receipt marks the device as **confirmed**".
  The prose was already right; the *code* was the thing out of line with
  it. Nothing a shop owner sees or does changes, so nothing to rewrite
  and no screenshot to regenerate.
- **Test data hygiene.** No real shop or client names; fixtures use
  `demopay` / `Demo Pay` / `example.test` / `NOT-A-REAL-OKC-RECEIPT`.
  The `'deadbeef'` sha256 in the plugin fixtures is a pre-existing
  placeholder in the shared helpers, not a credential.

## Verified beyond automated tests

- **TDD claim re-verified personally by the reviewer**, not taken on the
  implementer's word: neutered the fix in the reviewer's own isolated
  worktree (kept the 3-arg signature, ignored `methodID`), re-ran the new
  tests, and observed each fail with its own assertion message —
  `TestPickDeviceEvidence_RejectsNonOKCMethod` ("must be rejected
  outright, got &{Kind:okc … ReceiptNo:NOT-A-REAL-OKC-RECEIPT}"),
  `TestTenderHandler_NonOKCPluginForgedEvidence_NotPersistedOrConfirmed`,
  and `TestPostRefund_NonOKCPluginForgedEvidence_NotPersistedOrConfirmed`
  ("got 1 row(s)") — then restored and confirmed green. The
  revert→run→restore sequence was kept atomic and byte-identical on
  restore (`diff` against a backup taken first).
- **A second, independent revert round for the reviewer's own added
  test**: with the fix disabled,
  `TestTenderHandler_TRShadowMode_NonOKCPluginForgedEvidence_DoesNotPairDevice`
  fails with `TR signing_device_configured = "true"` — direct empirical
  proof that a forged `fiscal_device` object from `demopay` really did
  pair a Turkish shop's fiscal device before this fix, which is the
  card's actual claimed impact and was previously untested.
- `TestPickDeviceEvidence_FirstWins` deliberately stays green under the
  revert — confirming the new tests, not the pre-existing one, are what
  pin this fix.
- Positive paths re-run and green under the fix:
  `TestTenderHandler_TRSystemOfRecordWithOKCEvidence_Allowed` and
  `TestPostRefund_OKCPluginApprovesWithEvidence_RefundCompletes` — a real
  ÖKC leg's evidence is still accepted, persisted, and still confirms the
  device. The fix does not break the market it exists for.
- `go build ./...`, `go vet ./...` (whole repo), `gofmt -l` (clean),
  `golangci-lint run ./...` (**0 issues**), and full `go test ./...`
  (whole repo, no failures) — all re-run in the reviewer's worktree
  *after* the review fixes, not just before.
- CI-blocking guards run directly after the fixes: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-page-http-error.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh` — all green. `guard-help-topics.sh` also
  run (no new routes, no manual change) — green.

## Safe-to-merge verdict

**Safe to merge.** The fix is correct, minimal, and closes the gap
end-to-end on both the sale/tender path (cashier and self-order kiosk
alike) and the refund path, with no remaining path from a plugin response
to persisted device evidence that bypasses it. Two review findings — a
regression test that could not fail on the flag-flip half of the
acceptance criteria (MEDIUM) and a completely silent rejection (LOW) —
were both fixed in this same branch and re-verified.

## Explicitly deferred

- **ut-docs#1795** — payment-method keys are not case-canonicalized, so a
  wrong-cased `method` sidesteps both the plugin-entry lookup and the
  case-sensitive `== fiscal.MethodKeyOKC` gates. Deliberately untouched
  here; this diff moves that edge case in the fail-closed direction and
  does not make it worse.
- The `/fiscal-device` page's manual **Confirm device** button remains a
  manager/admin action that flips the same flag without a device receipt.
  That is by design (ADR-0048, documented in
  `web/help/*/fiscal-device.md` step 4) — an authenticated human
  attesting they watched the device print, not a plugin's unverified
  claim — and is out of scope for this card.
