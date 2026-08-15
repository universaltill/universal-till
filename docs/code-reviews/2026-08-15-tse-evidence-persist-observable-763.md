# Code review — TSE evidence persist failure is observable (ut-docs#763)

**Date:** 2026-08-15
**Card:** universaltill/ut-docs#763 (p3, `complexity:easy`, pilot:germany)
**Branch:** `fix/tse-evidence-persist-failure-observable-763`
**Dev:** inline (Sonnet, this pipeline cycle — easy tier)
**Reviewer:** independent fresh-context subagent, model `sonnet` (easy-tier review
exception — different instance, never saw the dev reasoning)

## What shipped

`recordFiscalTSEEvidence` (`internal/pages/fiscal_sign_hook.go`) persists a
signed sale's §6 KassenSichV TSE evidence. On a write failure it was
previously log-only (`logging.L().Errorf`) — silent to the operator and the
audit journal, unlike its sibling `declareUnsignedFiscalSale` (same file),
which gives an actual signing failure both a journal marker and an
operator-visible Problems-ring alert. A sale that **was** signed but then
lost its evidence is arguably worse than a cleanly-declared unsigned one,
since nothing on the receipt or in the audit trail flagged it as needing
attention (found by the independent Opus review of ut-docs#585, filed as
this scoped follow-up rather than expanding that card's diff).

- `recordFiscalTSEEvidence` gained an `actorID` parameter and, on a
  `RecordFiscalTSESignature` failure, now also writes a
  `sale`/`fiscal_evidence_persist_failed` audit marker (`repo.InsertAudit`)
  and raises a `logging.L().Warnf` operator alert (Problems ring) —
  mirroring `declareUnsignedFiscalSale`'s own two-part treatment exactly.
  Still best-effort/log-only on top of that: never unwinds or blocks the
  already-committed sale.
- Both call sites updated: the live tender path (`pos_api.go`) passes the
  real `actorID` already threaded through `completeTender`; the background
  retry tick (`fiscal_sign_hook.go`) passes `""`, matching the neighboring
  `fiscal_signing_resolved` marker's own convention.
- New regression test `TestFiscalSignAsk_EvidencePersistFailureIsObservable`
  forces the failure by dropping `fiscal_tse_signatures` out from under
  `RecordFiscalTSESignature` (same DB-error-injection technique as
  `TestFiscalSignExclusivity_EnableFailsClosedOnDBError`), and asserts: the
  sale still completes (200, one sale row), no `unsigned_fiscal_signing`
  marker (a persist failure is not a signing failure), the new
  `fiscal_evidence_persist_failed` marker is attached to the real sale id
  with the failure reason in its payload, and a Problems-ring warning
  names the sale.

## Independent review — findings

**0 blocking, 0 non-blocking.** Reviewer verified:

- The new marker/Warnf block sits strictly inside the existing failure
  branch — fires only on failure, confirmed by inspection and by
  `TestFiscalSignAsk_ApprovedWithTSEEvidencePersistsAndRenders` (happy
  path) showing no marker/warning.
- `RecordFiscalTSESignature` runs as its own `ExecContext`, outside the
  sale's own commit transaction — the new code is purely additive and
  cannot unwind or block the sale; `engine.Reset()` and downstream plugin
  dispatch in `completeTender` are unconditional after this block.
- No new SQL outside `internal/data` (`guard-data-access.sh` green) — only
  calls to the existing `repo.InsertAudit` / `repo.RecordFiscalTSESignature`.
- i18n: the Warnf string is an internal operator/Problems-ring log line,
  not templated UI — same exemption already used two lines away by
  `declareUnsignedFiscalSale`'s own Warnf (`guard-i18n.sh` green).
- Compliance wording (ADR-0040): the new strings make no
  GoBD/audit-proof/certified outcome claim, purely factual
  (`guard-compliance-claims.sh` green).
- Money: not applicable, nothing mistyped.

## What the reviewer verified beyond reading

- **TDD re-verified independently**: reverted the fix (back to the
  original log-only `Errorf`), rebuilt, ran the new test alone — it
  **failed** with the expected non-tautological message (`expected a
  sale/fiscal_evidence_persist_failed audit row: sql: no rows in result
  set`). Restored the fix from `git show HEAD`, confirmed `git diff --stat`
  clean against HEAD, re-ran — **passed**.
- Confirmed the `DROP TABLE fiscal_tse_signatures` injection is a clean
  failure mode: the only other post-tender reader of that table
  (`pos_api.go`'s receipt-render fragment) already degrades gracefully on
  a read error (`tseErr != nil → tseSignature = nil`), so nothing else is
  silently broken or masking a false pass.
- `go build ./...` clean; `go test ./internal/pages/... -run TestFiscalSign
  -v` — all 20 tests green, including the new one and its siblings.
- All four required guards green: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`.

## Full gate (orchestrator, before review)

- `go build ./...` clean.
- `go test ./... -race`: every package green except `internal/pages`,
  which hit the package's default 600s `go test` timeout at 600.091s —
  this is the **pre-existing, already-tracked** timing hazard from
  ut-docs#648 (that package's `-race` runtime sits right on the 600s
  default with ~0 margin), not caused by this diff. Confirmed by
  re-running `go test ./internal/pages/... -race -timeout 900s` alone:
  passed cleanly at 672.589s. `internal/plugins` (ut-docs#753, same known
  shape) passed in the same run at 599.898s — also no real margin, also
  pre-existing and already tracked.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` all green.

## Non-goals / follow-ups

- Not touching ut-docs#648/#753's own timeout-margin problem — out of
  scope for this card, already tracked separately.
- No i18n/help-topic change needed: this is an internal operator/log
  surface, not new user-facing UI.

## Safe-to-merge verdict

Yes. Guards, build, targeted package tests, and an independent
non-tautological TDD re-verification all green.
