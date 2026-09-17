# Code review — plugin-load loop fails open on a SubscribeWithHandler/HasActiveHook DB error (ut-docs#2304)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2304 (`complexity:easy`, `security`) — filed as a
  named follow-up by the independent review of ut-docs#2280
  (`2026-09-16-wasm-hook-events-fail-open-2280.md`, finding 1).
- **Branch:** `fix/2304-wasm-subscribe-fail-open`
- **Reviewer:** independent pass, fresh-context Sonnet subagent working in
  an isolated worktree (per this card's `complexity:easy` routing — never
  saw the implementation reasoning).
- **Verdict: SAFE TO MERGE.** No blocking findings, no code nits. One
  process gap (this record itself, and the commit's `WIP:` prefix) — both
  closed by this same step.

## The bug

`WasmRuntime.Sync`'s plugin-load loop (`internal/plugins/wasm_runtime.go`)
counted a plugin as `loaded` (and healed its `install_state` back to
`'installed'` if it had been broken) **before** attempting
`bus.SubscribeWithHandler`. That call itself can fail — it wraps
`repo.HasActiveHook`, a DB read against the `plugin_hooks` table, the same
table `ListPluginHookEvents` (fixed for ut-docs#2280 one line earlier in
this same loop) reads. Before this fix, that error path was a bare
`continue`, reached **after** the plugin was already counted loaded — so a
subscribe-time DB error left the plugin marked `'installed'`, counted
`loaded`, with **zero** registered event subscriptions, indistinguishable
from a healthy zero-hook plugin. This is the identical fail-open ut-docs#2280
closed, reached through the subscribe call instead of the hook-events
lookup: a blocking payment gate treats "no subscribers" as "nothing to
check, proceed" (`blockingPaymentEventWithResponseAndID`,
`internal/pages/refund_page.go`), so an active payment plugin silently left
un-subscribed by a transient DB error would let a sale/refund through with
no plugin consulted and no error surfaced anywhere.

## What shipped

- `internal/plugins/wasm_runtime.go`: the loop restructured so
  `loaded = append(...)` and the `markHealed` check now run **after** the
  `SubscribeWithHandler` attempt (or immediately, for a plugin with zero
  events, which never calls subscribe at all) instead of before it. On a
  `SubscribeWithHandler` error, the code now calls `markBroken(ctx, repo,
  row)` — the same helper the `ListPluginHookEvents` error path already
  uses — appends to `failed` instead of `loaded`, logs, and `continue`s.
- `markBroken`'s doc comment updated to name both failure points
  (ut-docs#2280's hook-events lookup and this card's subscribe call).
- `internal/plugins/wasm_sync_subscribe_error_test.go` (new):
  `TestWasmSync_SubscribeErrorMarksBrokenNotSilentlyLoaded`. Seeds a plugin
  with a real active hook so `ListPluginHookEvents` succeeds and the loop
  actually reaches `SubscribeWithHandler`, then runs `Sync` against a
  second `*sql.DB` connection to the same on-disk file, wrapped in a
  `driver.Connector`/`driver.Conn` (`failingConnector`/`failingConn`,
  mirroring `internal/data/export_repo_querycount_test.go`'s existing
  `countingConn` pattern) that fails `Prepare`/`PrepareContext` only for
  statements containing `"COUNT(*) FROM plugin_hooks"` — `HasActiveHook`'s
  exact query — while passing every other statement, including
  `ListPluginHookEvents`' own `SELECT DISTINCT` against the same table,
  through untouched. This isolates the subscribe-time failure from the
  earlier `ListPluginHookEvents` failure ut-docs#2280's own test already
  covers (which drops the whole table and so fails one step earlier).
  Asserts `install_state` flips to `'broken'`, the tally log reads `0
  plugins loaded` / `1 failed` (not double-counted), the subscribe error
  itself is logged, and the bus ends up with no subscribers for the
  plugin's event.

## What the independent review found

Ran the full gate itself: `gofmt -l .`, `go build ./...`, `go vet ./...`,
the new test plus its ut-docs#2280 sibling
(`go test ./internal/plugins/... -run 'TestWasmSync' -v`, all 4 pass), the
full `internal/plugins` + `internal/data` packages (green, no new
failures), `golangci-lint run ./internal/plugins/...` (0 issues), and
`guard-data-access.sh` — verified *why* it passes rather than trusting it:
the fix adds no SQL text, and the new test file's raw SQL is exempt
(`_test.go` files are excluded by the guard's own `grep -v '_test.go'`).

**TDD re-verification, done independently by the reviewer**: reverted only
`wasm_runtime.go` to its pre-fix state (test file untouched), re-ran the
new test — it failed with exactly the predicted symptom (`install_state`
stayed `"installed"`, tally read `1 plugins loaded (0 failed)`). Restored
the fix, confirmed green again, confirmed `git status`/`git diff` clean
afterward (no leftover revert).

Four specific regression questions, all answered clean:

1. **Zero-event plugin still counted `loaded` and healed?** Yes — the
   `if len(events) > 0 { ... }` block (the whole subscribe/goroutine
   machinery) is skipped entirely for a zero-event plugin, falling
   straight through to the unconditional `loaded = append(...)` and
   `markHealed` check at the end of the loop body, identical to pre-fix
   behavior for that case.
2. **Drain goroutine (`w.wg.Add(1)`) only started on subscribe success?**
   Yes — it's inside the `if err != nil { ...; continue }` guard's else
   path; on failure `ch` is nil (per `subscribe()`'s own `return nil,
   err`) and is never touched, so no closed/nil-channel read, no goroutine
   leak.
3. **`SetEventMode(Blocking)` called before subscribe — any stale-state
   risk on subscribe failure?** Same order as before this diff (only the
   surrounding block moved, not reordered internally). On failure the
   event mode is left set to `Blocking` for an event with no subscriber,
   but this is pre-existing and harmless: `EventBus.publish()` checks
   `subscribers[eventType]` first and returns early when empty, regardless
   of event mode — the real gate other callers use is `HasSubscribers`,
   not the mode map. No new fail-open introduced here.
4. **Loop-variable capture correctness?** `go.mod` declares `go 1.25.0`
   (per-iteration loop-variable semantics since Go 1.22) — no capture bug.

No double-counting: the new `failed = append(...)` on the subscribe-error
path happens exactly once per row before its own `continue`, same pattern
as the two other failure branches (load error, hook-events error);
`partitionBrokenByRecovery` and the tally log downstream operate correctly
off the combined `failed` slice.

**Backend-only, confirmed**: diff touches exactly two `.go` files under
`internal/plugins`, nothing under `internal/pages`/`web/`/`android/`/
`mobile/`, no user-facing string added — the UX-guidelines checklist and
the `web/help/` manual-currency requirement don't apply.
**Test data / secrets**: clean — synthetic ids only
(`com.test.subscribeerr`, `testpay`, `Test Pay`), no real shop/client
name, no secret-shaped literal.

One process finding, closed by this same step: no review record existed
yet and the commit was still titled `WIP:` — both fixed here (this record,
plus dropping the `WIP:` prefix and squashing before push).

## Explicitly deferred (not this card)

- Teaching the payment gate (`blockingPaymentEventWithResponseAndID`,
  `internal/pages/refund_page.go`) to consult `install_state`/
  `HasBrokenActivePluginForEvent` the way `tax_hook.go` already does — this
  fix makes a subscribe-time failure **observable** (plugins-page chip,
  logs, the tax fail-closed path) but does not by itself make an in-flight
  payment gate fail closed at the moment of the DB error; the gate's only
  guard remains `bus.HasSubscribers(event)`. Already noted as separate
  future work in ut-docs#2280's own review record — not rediscovered here
  as new scope, just carried forward.

## Merge status

**Not merged.** An open Admin Review card, ut-docs#2277, questions whether
the standing auto-push/auto-release authorization (2026-07-29, scoped to
"no real users yet") still holds, given evidence of a live production till
fleet — unresolved as of this cycle. Other concurrent lanes are already
following the same restraint (`universal-till#1200`, `#1201` opened,
reviewed, and deliberately left unmerged pending that decision). This PR
follows the same discipline: committed, pushed, reviewed, CI-worthy —
held unmerged until a human resolves ut-docs#2277, or explicitly confirms
this specific card is safe to merge regardless.
