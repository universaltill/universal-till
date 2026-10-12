# Review: AI-agent write confirmation gate (ut-docs#2838)

Date: 2026-10-12 · Lane: lane:local (resumed from a lane:sdk run that hit its
budget before a PR) · Reviewer: independent Fable subagent on a snapshot
worktree; the code was built by Opus.

## What shipped

ADR-0153 Decision 3, core only:

- `internal/agentaction`: registry of two named, versioned actions,
  `catalog.item.create.v1` (catalog / `catalog_management`) and
  `stock.adjust.v1` (stock / `stock_management`). Bounded parsers (at most
  50 rows), plan, still-current check, and apply in one transaction with the
  audit row. In-memory proposal store: single use, 5-minute TTL, bound to
  plugin + user + session, ceilings 3 per request, 20 per session per hour,
  256 open in total.
- `action_propose` host function: operator only from a hop-0
  `ui.view.ask`/`ui.action.ask` (session-stamped, never from the payload),
  `ai.agent` declared, `ai.tools.write:<area>` granted, and the operator's
  role holding the action's role permission. Errors: -2, -4, -5.
- `confirm` view component, drawn by core in core's own words, and
  `POST /ui/agent/proposals/{id}/confirm|cancel`, which re-check everything
  at confirm. `agent.action.*` audit rows and the `agent.action.result`
  event to the plugin.
- `auth.SessionKey`: a one-way hash of the session cookie. SDK
  `plugin.ActionPropose`. en/ar/fa/tr keys. The en help topic. ut-docs
  host-function, plugin-view and runtime docs.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `cleanText` let Unicode format characters (U+202E bidi override, U+200B zero-width) through, so the card could show a different or invisible name | **Fixed**: `unicode.Cf` refused; test cases added (failed first) |
| 2 | minor | A transient `InstalledManifest` error was audited as "ai.agent not declared" | **Fixed**: separate "manifest unreadable" reason |
| 3 | minor | `resolveAgentConfirms` ignored the plugin, so plugin B's view could show plugin A's proposal if it learned the id | **Fixed**: only the view's own plugin resolves; test added (failed first) |
| 4 | minor | Satellite catalogue refusal happens only at confirm | Deferred to ut-docs#4168 |
| 5 | minor | Expiry audit is written lazily (on the next store call) | Deferred to ut-docs#4168 |
| 6 | minor | An unknown action name returns -1 before the grant check (enumeration) | Deferred to ut-docs#4168 |
| 7 | minor | Help only in en; de/tr lack the AI section and ar/fa predate it | Deferred to ut-docs#4168 (those topics already lag English) |
| 8 | minor | ADR says "CSRF token"; the till uses SameSite=Lax plus a `Sec-Fetch-Site` refusal like every `POST /ui/*`; no test for that branch or for a concurrent double-confirm | Deferred to ut-docs#4168 |
| 9 | info | `UT_AUTH=off` stamps no operator, so `action_propose` returns -2 | Deferred to ut-docs#4168 (note for the AI-plugin e2e) |

Also fixed during Dev: `auth.WithSessionKey` was reachable only from tests
(deadcode guard); the middleware now calls it.

## Verified beyond the automated tests

- Mutation check: disabling the role check in `action_propose` fails
  `TestActionPropose_Refusals_2838/cashier_with_grant`; disabling it at the
  confirm route fails `TestAgentConfirm_CashierRefusedAtRoute_2838`.
  Restored, both green.
- Full `go test ./...` passes except
  `TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry`, which also
  fails 3 runs in 10 on main (ut-docs#4167, not this change).
- Sale-screen e2e (`sale-rail-orders-1349`, `portrait-tablet-sale-3050`):
  17 passed.
- Local guards green, except deadcode's `internal/logging` entries (used
  only by `cmd/unitill-desktop`, which can't compile on macOS; CI analyzes
  it) and macOS-only tool differences.
- Not looked at visually: the confirm card in a running till. No plugin can
  raise it until the AI-plugin wiring card; the card's HTML is asserted in
  `TestAgentConfirm_CardRendersCoreText_2838`. The tablet test belongs to
  that card.

## Verdict

Safe to merge. New keys go to the language packs (de/es/pt) in the same
cycle, after core merges.
