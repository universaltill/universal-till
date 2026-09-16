# Code review — plugin-load loop fails open on a ListPluginHookEvents DB error (ut-docs#2280)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2280 (`complexity:medium`)
- **Branch:** `fix/2280-hook-events-fail-open-broken`
- **Reviewer:** independent pass, Opus subagent working in an isolated
  worktree (per this card's `complexity:medium` routing — different model
  from the Sonnet implementation, never saw the dev reasoning).
- **Verdict: SAFE TO MERGE AS-IS.** No blocking findings; one should-fix
  (filed as a follow-up card, deliberately not folded in — see below) and
  three nits, two of which were folded in before merge.

## The bug

`WasmRuntime.Sync`'s plugin-load loop (`internal/plugins/wasm_runtime.go`)
had, right after a successful `w.load`:

```go
loaded = append(loaded, row.ID)
if row.InstallState == data.PluginStateBroken {
    if markHealed(ctx, repo, row) { stateChanged = true }
}
events, err := repo.ListPluginHookEvents(ctx, row.ID)
if err != nil || len(events) == 0 {
    continue
}
```

A genuine DB error on `ListPluginHookEvents` was folded into the same
`continue` as "this plugin genuinely has no hooks configured" — the plugin
was already counted `loaded` (and possibly just healed back to
`'installed'`) with **zero** registered event subscriptions, and the error
itself was never logged. This reopens, one layer earlier, the exact
fail-open ut-docs#2278 closed in `ListPaymentEntries`'s own error handling:
`blockingPaymentEventWithResponseAndID` (`internal/pages/refund_page.go`)
treats `bus.HasSubscribers(event) == false` as "nothing to check, proceed"
— an active payment plugin left with no subscriptions by a transient DB
error let a sale/refund through with no plugin consulted and no error
anywhere. Filed as a named follow-up in #2278's own review record
(`2026-09-16-payment-gate-fail-open-2278.md`), scoped there deliberately
rather than folded into that PR: different function, different package,
one layer earlier in the load path.

## What shipped

- `internal/plugins/wasm_runtime.go`: the `ListPluginHookEvents` error is
  now handled exactly like the `w.load` failure a few lines above it —
  logged, appended to `failed`, `markBroken` called — and this now happens
  **before** `loaded = append(...)` and the `markHealed` check, so both
  only run once the hook-events lookup has also succeeded. The genuine
  "no hooks configured" case (`err == nil, len(events) == 0`) is
  unchanged, just now reached after the loaded/healed bookkeeping instead
  of before it.
- `markBroken`'s doc comment updated to say it can now also fire on a
  successfully-loaded module whose hook-events lookup failed, not only on
  a module that failed to load at all.
- `internal/plugins/wasm_sync_hook_events_error_test.go` (new):
  `TestWasmSync_ListPluginHookEventsErrorMarksBrokenNotSilentlyLoaded` —
  drops the `plugin_hooks` table to force a genuine DB error (same
  technique ut-docs#2278's own tests use) for an active plugin seeded with
  a payment entry, asserts `install_state` flips to `'broken'`, the tally
  log reads `0 plugins loaded` / `1 failed` (not double-counted), the
  specific error is logged, and the bus ends up with no subscribers.

## What the independent review found

Ran the full gate itself: `go build ./...`, `go vet ./internal/plugins/...`,
`gofmt -l`, the new test (including under `-race`, relevant since `Sync`
spawns goroutines), the full `internal/plugins` package (+
builtinlayouts/marketplace/oauth sub-packages), `golangci-lint run
./internal/plugins/...`, and `guard-data-access.sh` — all green. Verified
*why* the data-access guard passes rather than trusting it: the fix adds no
SQL text (only calls to the existing `repo.ListPluginHookEvents`/
`markBroken`), and the test file's raw SQL is exempt (`_test.go` files are
excluded by `guard-data-access.sh`).

**TDD re-verification, done for real, by the reviewer independently — not
just re-running the already-passing test**: extracted the `wasm_runtime.go`
hunk to a patch, reverse-applied it (keeping the new test), confirmed the
pre-fix `if err != nil || len(events) == 0 { continue }` shape was back,
and re-ran the test. It failed with exactly the predicted symptom
(`install_state` stayed `"installed"`, tally read `1 plugins loaded (0
failed)` — the pre-fix tally was actively lying). Restored the fix,
confirmed green again.

**The most important question this review ran down**: does marking the
plugin broken actually change the *runtime* posture of the payment gate,
or only its observability? Grepped every call site of
`HasBrokenActivePluginForEvent` — `tax_hook.go`,
`tax_rate_switcher_banner.go`, `fiscal_signer_banner.go` — **and confirmed
`internal/pages/refund_page.go` is not among them.**
`blockingPaymentEventWithResponseAndID`'s only guard is
`bus.HasSubscribers(event)`; it never reads `install_state`. So this fix
makes the failure **observable** (plugins-page chip, logs, and the *tax*
fail-closed path) but does **not** by itself make an in-flight sale/refund
fail closed at the moment of the DB error — the payment gate still has no
`HasBrokenActivePluginForEvent`-style check the way `tax_hook.go` does.
This matches the card's acceptance criteria exactly as scoped (mark-broken
+ log + regression test, the same scope #2278's own review record already
blessed for this follow-up) — teaching the payment gate to consult
`install_state` the way tax does is real, separate work, not something to
fold into this diff. Filed as ut-docs#2276's sibling concern is adjacent
but distinct (that card is about a central tenant-scope interceptor); this
specific gap — payment gate not checking broken state — is worth its own
future card if picked up, noted here rather than invented as new scope on
this PR.

Four findings:

1. **should-fix, filed as a follow-up card (ut-docs#2304), not folded into
   this diff** — the identical fail-open survives one step later:
   `bus.SubscribeWithHandler` (called right after the now-fixed
   `ListPluginHookEvents` check) can itself fail — `subscribe()`
   (`internal/plugins/ipc.go`) returns an error on a `repo.HasActiveHook`
   DB failure, a read against the same `plugin_hooks` table — and that
   path is still a bare `continue` after the plugin is already counted
   `loaded`/healed. Correctly out of this card's scope (a different call,
   found only by the review, not named in the original card), so filed
   separately rather than silently expanding this PR.
2. **nit, not folded in** — the tally log's fixed suffix
   (`"— failed plugins are marked 'broken' until their files are
   restored"`) and `partitionBrokenByRecovery`'s re-import/re-fetch
   guidance are both file-restoration framed, which doesn't fit a
   `plugin_hooks` read error (the file is fine). Judged an acceptable
   pre-existing generic-tally limitation — the real cause is logged
   immediately above it — not worth a special case for one failure class
   among several the shared tally already covers.
3. **nit, fixed** — the `markBroken` doc-comment edit left an awkwardly
   wrapped line (`// does NOT touch the` orphaned mid-sentence). Rewrapped.
4. **nit, fixed** — the new test's tally assertion only matched substring
   `"1 failed"`, which would not have caught a hypothetical wrong fix that
   double-counted the plugin into both `loaded` and `failed`. Strengthened
   to also assert `"0 plugins loaded"` in the same log line.

After folding in findings 3 and 4: re-ran `gofmt -l`, `go build ./...`,
`go vet ./internal/plugins/...`, the full `internal/plugins` suite, and
`golangci-lint run ./internal/plugins/...` — all green again.

## Specific checks the review ran

- **`markHealed` semantics correctly preserved.** Traced all four cases
  through the reordered loop. The one behavior change is intentional and
  correct: a previously-`broken` plugin whose module now loads but whose
  hook-events lookup errors no longer heals to `'installed'` — it must
  not, since the subscriptions were never verified wired. `broken`→`broken`
  leaves `stateChanged` false (no spurious `bus.BumpGeneration()`);
  `installed`→`broken` correctly fires it, invalidating the tax memo cache.
- **No double-counting**: `loaded` and `failed` are mutually exclusive —
  every `failed` append is immediately followed by `continue`, before the
  `loaded` append is ever reached.
- **`w.modules` stale-entry check**: the compiled module stays cached (it
  did compile via `w.load`) even though the plugin is marked broken with no
  subscriptions. Grepped every call site of `HandleEvent` — the only
  production caller is inside the `handle` closure registered via
  `SubscribeWithHandler`, which this fix's `continue` now prevents from
  ever being created. No reachable path invokes a broken plugin's resident
  module.
- **Recurring bug classes**: N/A — zero file I/O in the fix hunk; the
  test's only path construction (`filepath.Join(t.TempDir(), ...)`) is
  absolute, and its file write goes through the existing
  `writeFileWithParents` helper, which does `os.MkdirAll` first.
- **Test data / secrets**: clean — synthetic ids only
  (`com.test.hookeventserr`, `testpay`, `Test Pay`); no real shop/client
  name, no secret-shaped literal.
- **Backend-only, confirmed**: diff touches exactly two `.go` files under
  `internal/plugins`, no template/`web/`/locale file — UX-guidelines
  checklist and help-topic-update requirement don't apply.

## Explicitly deferred (not this card)

- ut-docs#2304 — the `SubscribeWithHandler`/`HasActiveHook` fail-open one
  step later in the same loop (finding 1 above).
- Teaching the payment gate (`blockingPaymentEventWithResponseAndID`) to
  consult `install_state`/`HasBrokenActivePluginForEvent` the way
  `tax_hook.go` already does — noted during review, not filed as its own
  card yet since it wasn't independently rediscovered as a standalone
  actionable gap beyond what #2304 already tracks structurally; worth
  revisiting if a future card touches the payment gate's fail-closed
  behavior directly.
