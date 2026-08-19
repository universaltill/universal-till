# Code review: fiscal.sign.ask `cannot-sign` status (ut-docs#835)

**Date:** 2026-08-19
**Author:** Universal Till autonomous SDLC pipeline (Sonnet build, Opus independent review)
**Card:** universaltill/ut-docs#835
**Repos touched:** `universal-till` (code + tests + user manual), `ut-docs` (contract doc)

## What shipped

`ut-docs#835` found that `fiscal.sign.ask` (contract 1.2.0, ut-docs#834)
gave a signer only three response states — `approved`, `not-this-terminal`,
and `unreachable` — with no way to say "this SALE specifically cannot be
signed as presented", a deterministic property of the sale's own data (a
tip or sale-level discount/service charge the signer can't reconcile into
a valid Beleg — ut-docs#833/#834). A signer hitting this had to answer
`unreachable`, which core treats as a **backend-level** failure: the
background retry tick aborts early on it, so one permanently-unsignable
sale starved every genuinely-signable sale queued behind it during a real
outage — precisely the failure mode the contract's own text says must
never happen.

This change (contract → **1.3.0**):

- Adds a fourth response status, `cannot-sign`
  (`fiscalSignStatusCannotSign`), routed to a new outcome
  (`fiscalSignCannotSign`) that behaves like the existing **per-entry**
  failure path (`fiscalSignFailedEntry`) for tick-abort purposes — it does
  **not** stop the retry tick early — but is journaled and worded
  distinctly (`internal/pages/fiscal_sign_hook.go`).
- New audit action `unsigned_fiscal_cannot_sign`, separate from
  `unsigned_fiscal_signing` — the two never coexist on a sale
  (`declareUnsignedFiscalSale` is the sole writer of either, exactly once,
  at tender time).
- New locale key `receipt.fiscal.unsigned_cannot_sign` (all four locales)
  and its own ESC/POS line, so the receipt/journal never implies a
  connectivity outage for a sale that was never going to sign
  (`pos_api.go`, `print_api.go`, `web/ui/partials/receipt.html`).
  `saleHasUnresolvedSigningGap` became `saleFiscalSigningGapKind`, which
  returns *which* of the two gap actions (if any) is unresolved.
- The background retry backs off a `cannot-sign` entry to **6 hours**
  between attempts (`pendingFiscalSignRetry.NextRetryAt`,
  `fiscalSignCannotSignBackoff`) instead of the standard 2-minute cadence,
  since a deterministic, sale-data-driven refusal cannot change on a plain
  retry — the tick skips a not-yet-eligible entry without spending its 3s
  budget. `fiscalSignRetryTick`'s resolved-ID removal became
  `finalizeFiscalSignRetryTick`, which also applies these backoff updates
  under the same lock/re-load contract.
- Contract doc (`ut-docs/reference/contracts/fiscal-sign-ask.md`) bumped to
  1.3.0: new response state documented, Failure surface (a)/(b)/(d)
  updated, changelog row added.
- User manual (`web/help/{en,ar,fa,tr}/sell.md`) gets a new subsection
  distinguishing "can't be signed at all" from the existing outage
  subsection, per this repo's standing "manual ships with the feature"
  rule.
- `fiscalSignContractVersion` (the internal request-payload-shape marker
  used for legacy-entry replay refresh) deliberately **stays at 1.2.0** —
  this is a response-only addition, per that constant's own documented
  policy, same as 1.1.0's TSE evidence addition.

## Independent review (Opus, same checkout)

Briefed with the full diff, the surrounding code, the contract doc, and
this repo's CLAUDE.md; asked to check correctness (locking/races in the
retry tick, the two-marker invariant, `NextRetryAt` time handling),
contract-doc accuracy against the real code, i18n compliance, test
quality (including literally reverting pieces of the fix to check whether
the test suite would still catch it), backward compatibility, and — the
one that matters most here — that a `cannot-sign` sale can never end up
looking silently signed/compliant.

**First pass verdict: not safe to merge as written.** Findings, all
addressed before merge:

1. **Blocker** — `guard-docs-shots.sh` failed on the branch: the manual's
   screenshot manifest was stale against the changed `internal/pages/**`
   surface. **Fixed**: `make docs-shots`, twice (once for the code change,
   again after the manual topic edit below), manifest committed.
2. **Should-fix** — the ESC/POS `cannot-sign` line
   (`internal/pages/print_api.go`) had **zero** test coverage: the
   reviewer literally deleted the `case fiscalSignGapActionCannotSign:`
   arm and the full `internal/pages` suite still passed. Failure scenario:
   a `cannot-sign` sale, thermal-printed or reprinted, would carry no
   notice at all — a printed customer receipt indistinguishable from a
   signed sale, exactly the "must never silently appear as signed"
   property this whole card exists to protect. **Fixed**: added
   `TestBuildReceiptDoc_CannotSignPrintsDistinctNoticeAndResolvesClean` in
   `print_api_test.go`, mirroring the existing
   `TestBuildReceiptDoc_ResolvedSigningGapPrintsClean` pattern — asserts
   the distinct wording, that the outage wording does *not* also appear,
   and that a later `fiscal_signing_resolved` row makes the reprint clean
   (this also covers should-fix 3/nit 6 below).
3. **Should-fix** — the reviewer caught, and I independently confirmed
   against `askFiscalSign`, that the contract doc's new "Additionally
   treated as failure" paragraph mis-stated a bare transport/handler error
   (answered within budget) as backend-level/tick-aborting; the code
   actually classifies it per-entry — only a genuine budget timeout
   (`context.DeadlineExceeded`) or a declared `unreachable` aborts the
   tick. The doc briefly contradicted its own § Failure surface (d), which
   had the split right. **Fixed**: rewrote the paragraph to match the code
   exactly.
4. **Should-fix** — the user manual (`web/help/en/sell.md` + ar/fa/tr) was
   not updated in the same branch, a binding CLAUDE.md rule
   (ut-docs#324): the existing "When TSE signing can't be reached mid-sale"
   section only describes the outage path and states retries happen
   "every couple of minutes", which is no longer true for a `cannot-sign`
   entry. **Fixed**: added a parallel "When a sale can't be signed at all"
   subsection to all four locale copies, `make docs-shots` re-run.
5. **Should-fix** — `declareUnsignedFiscalSale` journals the gap action
   exactly once, at tender time, and never reclassifies it on a later
   retry-tick outcome. Concrete (not hypothetical) scenario: `ut-plugin-tax-de`
   v0.4.0 today answers `unreachable` for a sale it can't reconcile (its
   only option pre-1.3.0); if that plugin is later upgraded to answer
   `cannot-sign` instead, an already-queued entry's journal `reason` and
   receipt wording stay "unavailable"/outage-worded even though the
   *cadence* silently switches to the 6-hour backoff from that tick
   onward. Not a silent-signed hole (the sale still visibly shows as
   unsigned throughout — the reviewer confirmed this explicitly), so not a
   blocker, but the doc's (a)/(d) needed to say so rather than imply
   continuous tracking. **Fixed**: documented the tender-time-only
   semantics explicitly in the contract doc's (a), rather than adding
   write-time reclassification logic — the latter would meaningfully
   widen this card's scope (a new audit-write path keyed on "did the kind
   change since last write", more tests) for a narrow edge case (it only
   matters once `ut-plugin-tax-de` itself adopts `cannot-sign`, which
   hasn't happened yet) that is honestly documented instead.
6. **Nit** — `saleFiscalSigningGapKind`'s loop returned `""` directly on
   finding a *resolved* match for the first action checked, rather than
   falling through to check the second action. Not exploitable today (the
   two-marker invariant is enforced by `declareUnsignedFiscalSale` being
   the sole writer of either), but strictly safer to `continue` instead —
   a future path that could ever write both no longer risks a resolved
   `cannot-sign` marker masking an unresolved outage one. **Fixed**.
7. **Nit** — the `fiscalSignFailedBackend` doc comment still said "timeout,
   transport/handler error, or …", the same inaccuracy as finding 3, right
   next to code this diff already touches. **Fixed** — comment now states
   the real split and cross-references `askFiscalSign`.
8. **Nit** — inside `fiscalSignRetryTick`'s resolved branch, an inner
   `now := time.Now().UTC().Format(...)` shadowed the outer `now`
   (`time.Time`, used for the backoff arithmetic) in the same loop body —
   no behavioural bug today, but an easy foot-gun to leave open on a
   compliance-bearing path. **Fixed** — renamed to `resolvedAt`.
9. Noted, no action needed: the 6-hour backoff is computed from tick-start
   `now` rather than the individual attempt's own timestamp — irrelevant
   at a 6-hour horizon.

**Verified correct, no finding**: retry-tick locking and the mid-tick
enqueue race (an entry enqueued while the tick is dispatching is neither
dropped nor given a stray `NextRetryAt` — `finalizeFiscalSignRetryTick`
re-loads under the lock before applying updates, keyed by sale ID);
`NextRetryAt` parsing/comparison (UTC-absolute, no off-by-one, degrades
safely on a parse error); `isFailure()`'s extension not touching the two
original outcomes; backward compatibility for a legacy entry (no
`NextRetryAt`/`ContractVersion` — unchanged 1.2.0 behaviour); the
compliance surfaces (journal + receipt + operator alert + retry) all fire
on the tender-time `cannot-sign` path with no branch that reaches
"silently signed"; i18n key parity and translation quality across all four
locales; `fiscalSignContractVersion` correctly staying at 1.2.0.

## Verification performed personally (beyond the reviewer's pass)

- `go build ./...` and the full `go test ./...` — green, after every round
  of fixes above (not just the first).
- `bash scripts/ci/guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` — all green on the final diff.
- Manually re-read the final `internal/pages/print_api.go` diff line by
  line against a stale copy of itself: after the reviewer's literal-revert
  experiment on the shared checkout left the `cannot-sign` case arm
  missing mid-review (a real instance of the exact hazard ut-docs#386
  documents — a subagent mutating tracked files in a checkout the
  orchestrator is about to commit from), I restored it, re-ran
  `go build ./...` and the targeted `internal/pages` suite before doing
  anything else, and re-diffed against my own intended final state before
  proceeding.
- Manually re-ran the specific new/changed tests
  (`go test ./internal/pages/... -run 'FiscalSign|RenderReceipt|Print' -v`)
  to confirm each one actually exercises the claimed behaviour, not just
  that the package-level `ok` hides a skip.

## Not done, and why

- `ut-plugin-tax-de` (a separate repo) is **not** updated to actually emit
  `cannot-sign` in place of its interim `unreachable` workaround — the
  contract note flags this as available, not required; the plugin's
  existing behaviour stays valid either way. Left as a natural follow-up
  once that repo is next touched.
- No new ADR: this is an additive extension within the `proceed-and-declare`
  failure-surface model ADR-0041/ADR-0044 already establish, not a change
  of course — the same footing as 1.1.0's TSE-evidence and 1.2.0's payload
  additions, neither of which needed one either.
