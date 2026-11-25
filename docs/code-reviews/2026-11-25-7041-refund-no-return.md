# Review — provider refunded, return not recorded (ut-docs#7041)

Date: 2026-11-25 · Author: Dev (Sonnet) from the Architect design · Reviewer: Opus 5.5 (independent; code by Sonnet 5.5)

## What shipped

- A provider refund (`payment.<key>.refund`) is a journaled leg under `refund:<original sale>:<method>:<request id>` (ADR-0136 Decision 9.8): `asked` before the hook (fail closed, 503 `refund.error.journal_unavailable`), `captured` on approval, `settled` inside CompleteSale's transaction.
- CompleteSale failing after the provider refunded appends `warned`/`refund_no_return` at once (Problems item) and answers `refund.error.refunded_not_recorded`; boot warns a `captured` refund leg the same way and never reverses it.
- A retry whose key is `captured`/`warned`/`resolved` is refused (409) before the provider is asked (`captured` added in review, new key `refund.error.already_sent` for `captured`/`resolved`).
- Payments needing attention lists it ("Refunded, no return recorded", original receipt); Mark as handled, PIN, audit, nothing sent.
- No migration. Help topic `payments.md` and `docs/data-model.md` updated.

## Findings

| # | Severity | Finding | Disposition |
|---|---|---|---|
| R1 | Medium (money) | Pre-ask check refused only `warned`/`resolved`, and `AskLeg` returns (began=false, nil) for a `captured` key, so a same-key re-submit while the first request is still between capture and CompleteSale, or after the `warned` write itself failed (Tester O1), re-sent the refund to the provider — double refund unless the provider dedupes on the request id; contradicts 9.8 "never re-sent". | **Fixed**: `captured` refused too (409). New key `refund.error.already_sent` (+meta) for `captured`/`resolved`; `warned` keeps `refunded_not_recorded`. Test `TestRefund_RetryWhileCaptured_RefusedBeforeProvider` — red verified by reverting (retry = 200, provider asked twice), green restored. `docs/data-model.md` updated. |
| R2 | Low | UX note: `resolved` answered `refunded_not_recorded` ("a manager must check it…") for a row no longer listed. | **Fixed** with R1 (`already_sent`: "check the receipt's returns; if none is listed…"). |
| R3 | Low | ÖKC "approved, no device evidence" early return (ut-docs#1788) runs after `captured`, so that leg is warned only at next boot, not "at once" (Dev follow-up). Live double refund is now blocked by R1 (same-key retry refused). | **Deferred** — Backlog card (needs an ÖKC refund test rig). |
| R4 | Low | If the `captured` append fails, the leg stays `asked`; a CompleteSale failure then cannot append `refund_no_return` (gate needs captured→warned), so it is listed only at boot as `till_restarted`. Still durable and listed. | **Accepted** (double DB-failure path). |
| R5 | Info (pre-existing) | Two concurrent submits of the same refund attempt (both pass the pre-check while the key is `asked`) can both ask the provider; there is no in-flight lock on `/api/refund`. Not introduced here. | **Deferred** — Backlog card (in-flight guard / refuse `asked`). |
| R6 | Info | ADR-0136 9.8 text says "already `warned` or `resolved` is refused"; the till now also refuses `captured` (stricter, same intent "never re-sent"). | **Deferred** — one-line ADR wording follow-up in ut-docs. |

Checked, no finding: journal moves all pass `appendTx` (refund_no_return gated to captured→warned only, both directions tested); `settled` written inside CompleteSale's tx via `SettleLegsTx`, so a rolled-back return leaves `captured` and the warn path runs; boot `reconcileCaptured` short-circuits refund keys before any reversal (mutation M6 killed); refund keys `refund:<sale>:<method>:<id>` cannot collide with authorize ids (server-minted UUIDs) or reversal journal rows (reversals carry no journal hook; `refund:reversal:` is only a paybroker key); reversal dispatches through the now-journaling `refund` suffix carry no `withLegJournal` hook, so they are not affected; Resolve keeps route-level PIN/audit/demo denial, PIN summary chosen server side; Retry offered only for `reverse_failed`; money via `money.FromMinor`/`Minor()`; no raw SQL outside `internal/data`; no migration; English-only strings with meta.

Tester: PASS (7 mutations killed, all ACs covered, full `internal/pages` + race run green, driven run OK; O1 → R1 fixed, O3 → R3 deferred).
UX: PASS (2 wording edits made; 1024×600/360px not screenshotted; already-resolved note → R2 fixed).

Pre-flight: `gofmt -l .` (clean); `go build ./...` ok; `go vet ./internal/pages/ ./internal/data/ ./internal/pos/` ok; `go test ./internal/data/... ./internal/pos/...` ok; `go test ./internal/pages/ -run 'Refund|LegAttention|Reconcile|Leg|Tap|Demo' -count=1` ok (42s); `golangci-lint run ./internal/...` 0 issues; `guard-i18n.sh`, `guard-i18n-meta.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` exit 0. Not re-run by reviewer: full `internal/pages`, e2e, shellcheck (no scripts touched).

## Verdict

**Approve** after R1/R2 fixes. Follow-ups: R3, R5, R6.
