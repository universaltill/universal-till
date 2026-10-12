# Review: AI-agent proposals — satellite refusal at propose time, timely expiry audit (ut-docs#4168)

- **Card:** ut-docs#4168, items 1 and 2. Items 3–6 shipped in universal-till#1876 and ut-docs#4183. Item 7 (de/tr help translations) is not part of this change and stays on the card.
- **Branch:** `fix/4168-agent-propose-satellite-expiry`
- **Author:** Sonnet dev subagent (complexity:easy).
- **Reviewer:** an independent Opus 5.5 subagent, fresh context.
- **Lane:** `lane:cloud-41`

## What shipped

1. **Satellite refusal at propose time.**
   - New in `agentaction`: `SetProposeRefusal` / `ProposeRefusal`, a predicate core installs, with the same shape as `SetStockLocationResolver`.
   - `pages.registerAgentProposals` installs the till's rule: a `catalog` action on a till that follows a main till is refused with "catalogue is edited on the main till".
   - `plugins.actionPropose` calls the predicate after the grant and role checks and before planning. A refusal returns an audited `-2`, and nothing is stored.
   - `agentConfirmRefusal` uses the same predicate. The rule is still checked at confirm because a till can be linked to a main till after the proposal was made.
2. **Expiry audit on time.**
   - `Store.Put` arms a one-shot `time.AfterFunc` (injectable for tests) at the proposal's expiry. It sweeps the store and writes `agent.action.expired` even when the till is idle.
   - `Take` and the sweep stop the timer of each proposal they remove. At most 256 proposals are open, so at most 256 timers are live.
3. **Docs:**
   - `docs/plugin_guidelines.md`.
   - ut-docs `reference/plugin-host-functions.md` and `architecture/wasm-runtime.md`, in a separate ut-docs PR.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Nothing showed that a *stock* action still applies on a satellite. A mutation that refused every area would have passed the suite. | **Fixed.** Added `TestAgentConfirm_SatelliteStillAppliesStock_4168`. With the `a.Area == "catalog"` condition removed it fails; with the condition restored it passes. |
| 2 | minor | An expiry timer that fires after shutdown hits a closed DB. That gets logged, and the proposal gets no `expired` row. | **Accepted.** This gap already existed: proposals die with the process, per the store's doc comment. It is a warning-only path. |
| 3 | nit | Swept proposals kept their timers, which later fired into an empty sweep. | **Fixed.** `sweepLocked` now stops them. |
| 4 | nit | The audit perm for the satellite refusal is `ai.tools.write:<area>`, the same as for a missing grant. | **Accepted.** The reason text tells the two apart, and the plugin sees `-2` either way, as documented. |
| 5 | nit | The test comment named the wrong type, and a doc line was wrapped badly. | **Fixed.** |
| 6 | nit | The test cleanup resets the global rule to a no-op instead of the previous rule. | **Accepted.** This is the same pattern as `SetStockLocationResolver`, and every harness re-registers the rule. |

## Verified

- **The reviewer's mutation checks:** five mutations, one per production hunk: timer arming, Stop in Take, the refusal in `actionPropose`, the rule closure, and the confirm delegation. Each one made its test fail, and each passed again once restored.
- **The orchestrator re-checked** the new stock test with the area-condition mutation: red, then green after restoring.
- **Build and lint:** `gofmt -l` is clean, `go build ./...` and `go vet` pass, and `golangci-lint run ./...` reports 0 issues.
- **Guards:** data-access, core-neutral, i18n, kiosk-engine, card-data-schema, no-showmodal and deadcode-baseline-growth pass. deadcode-baseline could not run locally because the tool was built with go1.26, so CI runs it.
- **Tests:**
  - `go test -race` passes for `internal/agentaction`.
  - The `ActionPropose|AskOperator` subset of `internal/plugins` passes.
  - The `AgentConfirm` subset of `internal/pages` passes.
  - Full non-race `go test` runs of the three packages are covered by CI.
- **No UI change.** The confirm card is unchanged, and on a satellite the plugin now gets `-2` before any card is drawn.

## Verdict

Safe to merge.
