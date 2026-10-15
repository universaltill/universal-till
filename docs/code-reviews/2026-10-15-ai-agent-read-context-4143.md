# Review: AI-agent reads — operator context, role check, 25-call ceiling (ut-docs#4143)

**Date:** 2026-10-15 · **Card:** ut-docs#4143 (ADR-0153 Decision 1 "The
operator's role still applies to reads", Decision 3 "Who is asking", the
reads part of Decision 6) · **Author:** Opus 5.5 (lane:cloud-24) ·
**Reviewer:** Fable, fresh context, isolated worktree.

## What shipped

For a plugin whose installed manifest declares `ai.agent`, `view_query`
now also needs (`internal/plugins/wasm_views.go` `agentReadAllowed`):

1. **An operator.** Only a hop-0 `ui.view.ask` / `ui.action.ask` (operator
   stamped from the session by the page layer, never from the payload), or
   a hop-0 job started from such an action's answer. A job keeps the ask's
   operator *and request id*, plus a session check run before every use:
   the session must still resolve to the same user with the same role
   (`auth.Service.SessionCheck`, hash-only, never touches the session).
   Everything else — schedule ticks, cloud directives, sale-path and
   published events, the identify job, a job without the check — gets no
   operator: `-2`, audited.
2. **The operator's role** for the view's data class
   (`data.AIReadRolePermission`): sales → `reports`, staff →
   `user_management`, till_health → `audit`, catalog/stock/shop → none,
   customers → refused (no view yet; decided with #2842). Cashier gets no
   sales/staff/till-health view. `-2`, audited.
3. **At most 25 calls per operator request** (`agentaction.ToolCalls`,
   keyed by session + request, across the ask and its job). Call 26: `-5`,
   audited. The per-event 64 still applies.

`action_propose` shares the operator helper (`hostState.askingOperator`),
so it now also works in a live job started from an ask and is refused in
one whose session ended. Plugins without `ai.agent` are untouched
(`agentReadAllowed` returns before any work). Page layer: the action
handler stamps the operator once (`withAgentOperator` is now idempotent)
and hands it, with the session check, to `startPluginJob`.

Docs (ut-docs, same card): `reference/contracts/plugin-views.md` 1.6.0
(new "Plugins that declare `ai.agent`" section, which also documents
#2837's data-class gate the contract never had), `reference/plugin-host-functions.md`,
`architecture/wasm-runtime.md`. No user-facing strings, no UI change
(docs-shots surface hash refreshed: context wiring only, no pixel).

## TDD

Failing first, each with the real error: the four
`wasm_views_agent_test.go` tests (`view_query = 2, want -2`, `call 26 = 2,
want -5`, …), `core_views_role_test.go` (undefined symbol), and the page
test `TestAgentJob_KeepsAskOperatorAndSessionCheck_4143` — re-verified by
reverting `plugin_view.go` alone: `job operator {}, want the ask's
{mgr-1 …}`; restored → pass. The budget's own unit test was written with
its implementation.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Ceiling test used a fixed request id against the process-global budget: `-count=2` failed on the rerun. | **Fixed**: the test swaps in its own `NewToolCallBudget()`. |
| 2 | minor | With 1024 live turns every new turn of every session was refused for up to 10 min, audited as the per-request ceiling. | **Fixed**: a full tracker evicts the oldest turn instead; window shortened to 6 min (job max 300 s). Test updated. |
| 3 | minor | The job's session check captured the raw session cookie. | **Fixed**: `auth.Service.SessionCheck` keeps only the token hash (`resolveHash` split out of `ResolveFor`, behaviour unchanged). |
| 4 | minor | `ResolveNoTouch` semantics (may idle-lock) now run from the job. | Accepted (consistent with background polls, ut-docs#2901); documented on `withAgentJobSession` and `SessionCheck`. |
| 5 | minor | `-3` on a failed role lookup undocumented. | **Fixed** in plugin-views.md. |
| 6 | nit | Up to 64 denial rows per event for a looping plugin. | Accepted: same shape as the existing `views_used` denial. |
| 7 | nit | Role frozen at ask time during a job. | **Fixed**: the check also compares the role; page test covers a mid-job role change. |

Reviewer's security checks with no finding: published events can't be
hop 0; schedule/cloud contexts are never stamped; identify and
non-ask jobs get no operator; content-slot asks are admin-gated and
role-checked anyway; budget key not plugin-controlled; non-agent plugins
unaffected; no file writes or paths in the diff.

## Verified beyond unit tests

Real wasip1 guest driven through `WasmRuntime.HandleEvent` for every
gate (not a stub host state). Page-layer test with a real
`auth.Service` session: live, role change → false, restored → true,
logout → false. No shipping plugin declares `ai.agent` yet, so there is
no user-visible path to drive in the running app.

## Deferred

- ut-docs#4444 — the camera identify job has no operator, so an
  `ai.agent` plugin's identify reads are refused; must land before #4142
  ships the plugin with `ai.agent` (commented on #4142).

**Verdict:** safe to merge once the gate is green.
