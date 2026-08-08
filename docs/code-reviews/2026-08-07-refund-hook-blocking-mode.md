# Code review: gate `.refund` hooks as Blocking in `WasmRuntime.Sync`

**Card:** universaltill/ut-docs#434
**Branch:** `fix-refund-hook-blocking-mode`
**Complexity:** easy → reviewed by a fresh-context Sonnet subagent (per
model-routing rules; independent instance, no prior exposure to the dev
reasoning).

## What shipped

`WasmRuntime.Sync` (`internal/plugins/wasm_runtime.go`) registered a hook
event as `Blocking` dispatch mode only when its name ended in `.authorize`
or `.ask`. `.refund`-suffixed events (`payment.<key>.refund`, published by
`blockingPaymentEventWithResponse` in `internal/pages/refund_page.go` for a
refund's payment leg) fell through to the async default, so in production a
payment plugin's decision to decline a refund never actually stopped it —
`resp`/`err` came back `nil, nil` regardless of the plugin's answer. This
predates the ut-docs#385 log-redaction fix and was independent of it (that
fix's `HandleEvent`/`wasmResultLogLine` redaction runs on both the blocking
and async-drain paths either way).

Fix: added `.refund` to `Sync()`'s suffix check, mirroring `.authorize`/
`.ask`. One line of production logic, plus a comment explaining why.

Regression coverage: `TestWasmRuntimeSyncLoadsRealModuleAndSubscribes`
(`internal/plugins/wasm_sync_test.go`) — which already drives a real `Sync()`
end-to-end against a real compiled WASM module — now also seeds a
`payment.test.refund` hook and asserts `bus.GetEventMode(...) == Blocking`
after the real `Sync()` call, rather than a new file hand-wiring
`SetEventMode` directly (which is exactly how the original gap escaped
`TestBlockingPaymentEventGate`'s notice).

## TDD verification (personally re-verified, not taken on trust)

- Dev's own claim: test fails pre-fix, passes post-fix.
- Independently re-verified twice this cycle: once by Dev before handoff,
  and again by the review subagent, which reverted only the production line
  in `wasm_runtime.go` and re-ran the test — confirmed failure
  (`refund event not blocking`) against the unfixed code, confirmed pass
  after restoring the fix.

## Independent review findings

Spawned an independent fresh-context Sonnet subagent (general-purpose) with
the diff, `CLAUDE.md` rules, and an instruction to actually run build/vet/
tests and hunt for real problems, not confirm the diff is fine. It:

- Re-verified the TDD failing→passing claim itself (see above).
- Grepped the whole repo for every event *name* ending in `.refund` —
  confirmed the only producer is `blockingPaymentEventWithResponse`
  (`payment.<key>.refund`), so the suffix match doesn't accidentally sweep
  in an unrelated reporting/analytics event.
- Traced `EventBus.publish()`'s `Blocking` vs default dispatch branches —
  confirmed a `.refund` event now taking the `Blocking` branch cannot also
  land on the per-plugin async-drain channel, so there's no double-handling
  introduced by this change; same mechanism already relied on for
  `.authorize`/`.ask`, unchanged here.
- Confirmed the new test exercises the real `Sync()` registration path
  (seeded DB row → real `Sync()` call → real `GetEventMode` read), not a
  hand-wired stand-in.
- Checked for the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`) —
  not applicable; this diff does no file I/O.
- Checked for hardcoded strings / SQL outside `internal/data` / money-type
  misuse / real client names — none; confirmed via inspection and
  `guard-data-access.sh`.
- Ran `go build ./...`, `go vet ./...`, the targeted test, and the full
  `internal/plugins` + `internal/pages` package suites fresh (`-count=1`,
  no cache) — all green.
- One non-blocking finding: `ut-docs/architecture/wasm-runtime.md`'s
  "Payment authorization" section documented `.authorize` getting Blocking
  automatically at Sync but never mentioned the symmetric `.refund` case,
  even though the refund gate already existed. **Fixed in this cycle** —
  added a sentence mirroring the `.authorize` line, referencing ut-docs#434.

Verdict from the independent pass: **safe to merge as-is**, no blockers.

## Verified beyond automated tests

- No UI/runtime-facing surface touched (pure event-bus dispatch-mode
  logic + its own unit test) — no driven/screenshot run applicable, per
  the Tester step's own assessment, confirmed by reading the full diff.
- Full gate run once, after implementation was finished: `go build ./...`,
  `go vet ./...` clean; `go test ./...` green except the pre-existing,
  unrelated `internal/issuereport` `TestSaveCleansUpDirectoryOnWriteFailure`
  flake (tracked separately as ut-docs#415, root-user-only; confirmed to
  fail identically on unmodified `main` before this branch's changes, so
  not a regression introduced here); `guard-data-access.sh` and
  `guard-i18n.sh` both pass.

## Deferred / out of scope

Nothing deferred — the one review finding (doc update) was small enough to
fix in this same cycle rather than spin off a separate card.
