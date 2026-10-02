# Review — charge.policy.ask: warn when a winner shadows other plugins (ut-docs#2955)

**Date:** 2026-10-02 · **Branch:** `fix/2955-charge-policy-multi-answer-warn`
**Author:** Sonnet dev subagent (complexity:easy) · **Reviewer:** Opus, fresh context, separate worktree

## What shipped

- `EventBus.SubscriberIDs(eventType)` (`internal/plugins/ipc.go`): plugin IDs
  subscribed to an event, in dispatch order, de-duplicated, fresh slice.
- `pluginChargePolicyAsker.AskChargePolicy` (`internal/pages/charge_hook.go`)
  now uses `AskFrom` to learn the answering plugin. When a fresh ask is
  answered and other plugins are subscribed **after** the winner (never
  asked — `AskFrom` stops at the first answer, plugins load by id), it logs
  one `logging.L().Warnf` (reaches the operator Problems ring) naming the
  winner and the shadowed plugins — at most once per bus generation.
  Winner rule unchanged (ADR-0061 D1).
- A result from an older generation than the one already cached is returned
  to its caller but neither cached nor warned on.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | "Ignored" list named every non-winner subscriber, including ones that came *before* the winner and declined / were not permitted — pointing the operator at a conflict that didn't exist. | **Fixed:** only subscribers after the winner are named; none → no warning. New test `TestAskChargePolicy_DeclinerBeforeWinnerNoWarning`. |
| 2 | Low | Warn-once gate untested: same-generation re-asks hit the answer cache, so removing the gate left tests green. | **Fixed:** `TestAskChargePolicy_ConcurrentMissesWarnOnce` (8 callers held on a barrier inside the ask). Mutation (gate removed) → `warning count = 8, want 1`. |
| 3 | Low | A slow ask from an older generation could overwrite a newer generation's cache/warn state → second warning. | **Fixed:** stale-generation results skip cache + warn. |
| 4 | Low | Subscriber list read after the ask could disagree with the winner. | **Fixed by #1:** winner not in list → no warning. |
| 5 | Nit | "plugin-id order" holds because the load query orders by id. | Accepted; wording says "plugin-id order", `SubscriberIDs` doc says subscription order. |
| — | Nit | No-warning test matched any `[WARN]` line. | **Fixed:** matches `[WARN] charge.policy.ask:`. |

## Verified

- TDD re-verified by the reviewer in a separate worktree: production change
  reverted → `TestAskChargePolicy_MultiAnswerWarnsOncePerGeneration` fails
  `warning count = 0, want 1`; `ipc.go` reverted → `SubscriberIDs undefined`.
- Mutation checks of the new tests (gate removed; "after winner" logic
  replaced by "all but winner") both fail as expected; restored → pass.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues) on the
  touched packages; `go test ./internal/pages/ ./internal/plugins/` full
  packages green; ChargePolicy tests green under `-race`;
  `guard-core-neutral.sh`, `guard-data-access.sh`, `guard-i18n.sh` and the
  other build-job guards run locally pass (`guard-deadcode-baseline.sh`
  flags `internal/logging/file.go` only because the desktop root is skipped
  without GTK headers here — untouched by this change; CI analyses it).
- No UI surface, no locale keys, no migration, no manual change (operator
  log line only).

**Verdict:** safe to merge.
- CI `guard-docs-shots.sh` tripped on the `internal/pages/*.go` edit; the
  change renders no pixel (a log line + an asker field), so the surface hash
  was refreshed with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`).
