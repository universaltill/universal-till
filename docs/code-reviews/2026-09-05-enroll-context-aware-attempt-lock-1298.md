# Code review: `enroll.RegisterNow`'s attempt lock isn't context-aware (ut-docs#1298)

**Date:** 2026-09-05
**Card:** ut-docs#1298
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagent, read-only). One round; no blocker-class finding, so no second round.

## What shipped

`internal/enroll/enroll.go`'s `attemptMu` serialized registration attempts
(the background retry loop in `run()` and the Settings "Register now"
button / `EnsureRegistered`'s callers) as a plain `sync.Mutex`, which does
not observe a caller's context. Three call sites in `internal/pages`
(`setup_page.go`'s `autoRegisterForSetup`, `setup_base_plugins.go`,
`setup_tse.go`) bind a single `EnsureRegistered`/`RegisterNow` attempt with
a short `context.WithTimeout` (5s in practice), expecting that to cap the
worst case. If the background loop already held the mutex around its own
longer-bounded (~15s `httpClient.Timeout`) signing-key fetch on a
black-holed network, a caller would queue behind that fetch first — roughly
quadrupling its intended bound (~20s observed worst case, not 5s). None of
the three callers can actually fail their response because of this (all
best-effort, log-and-swallow), so this was a latency-tail bug, not a broken
guarantee — real but not urgent, filed from an earlier independent review
(`docs/code-reviews/2026-08-29-setup-wizard-opt-in-eager-registration.md`,
finding F3) rather than blocking that card.

- `attemptMu sync.Mutex` replaced with `attemptSem chan struct{}` (a
  1-buffered channel semaphore) plus two helpers: `acquireAttempt(ctx)
  bool` (blocks until the slot is free or `ctx.Done()`, whichever first)
  and `releaseAttempt()` (releases a slot previously acquired; panics on
  an unpaired call rather than blocking silently — see "Findings" below).
- `RegisterNow` now gives up and returns `CurrentStatus()` + an error
  wrapping `ctx.Err()` if it can't acquire before its caller's deadline,
  instead of blocking past it.
- `run()` (the background loop) uses the same acquire path with its own
  lifetime context — still effectively blocks until the slot is free
  (preserving today's behavior), but can now also exit promptly if `ctx`
  is cancelled while queued, not just while an HTTP call is in flight.
- No caller-side changes: all three callers already log-and-swallow errors
  from `RegisterNow`/`EnsureRegistered`.
- `internal/pages/setup_language_catalog.go`: comment-only fix — a doc
  comment there named this exact bug (measured 7.1s/worst-case ~30s) as
  one of two reasons that handler uses `enroll.Effective` instead of
  `enroll.EnsureRegistered`. Corrected to say the mutex is now
  context-aware, while keeping the (still valid on its own, ADR-0015
  lazy-registration) first reason as the standing justification — no
  behavior change in that file.
- No ADR: internal concurrency-correctness fix, no external contract change.

## Tests

`internal/enroll/enroll_test.go`, three new cases:

- `TestRegisterNowRespectsCallerContextWhenAttemptSlotHeld` — holds the
  slot, calls `RegisterNow` with a 100ms-timeout context, asserts it
  returns within 500ms with a non-nil error (not `Registered`). **Genuine
  red-first test**: re-verified independently (below) against a
  reconstructed pre-fix `sync.Mutex` implementation — it fails (hangs)
  against the old code, as expected.
- `TestBackgroundLoopStillAcquiresAttemptSlotOnceFree` — holds the slot,
  calls `Init` (which starts `run()` needing the signing key), releases
  after 50ms, asserts the key is eventually fetched. Exercises `run()`
  itself, not just `RegisterNow`.
- `TestBackgroundLoopExitsPromptlyWhenCancelledWhileQueuedOnAttemptSlot` —
  holds the slot for the whole test so `Init`'s loop can only be queued on
  `acquireAttempt`, confirms via `wg.Wait()` that it's still running,
  cancels `ctx`, confirms it exits within 2s instead of waiting out the
  held slot. Covers the half of the fix `TestInit_BackgroundLoopJoinsOnCancel`
  doesn't (that test cancels while an HTTP call is in flight, not while
  queued on the slot).

`resetState()` deliberately does **not** touch `attemptSem` — see its own
doc comment for why (a defensive drain shipped once during this fix's
development and produced a real hang; see "What the independent review
found" below).

## Independent review (Opus, read-only, this repo — not a worktree)

Re-derived every claim rather than trusting it: reconstructed the pre-fix
`sync.Mutex` implementation via `go test -overlay` (repo left untouched)
and ran the new tests against it directly, took a goroutine dump under
`-race -count=8`/`-count=30` to check for lost wakeups, and pulled a
coverage profile to confirm `run()`'s new early-return branch is actually
exercised rather than just written.

**No blocker-class finding** (no deadlock, no lost wakeup, no data loss).
`run()`'s retry/backoff semantics (attempt counter, `retryDelays`
indexing, exit condition) are unchanged — the only behavioral difference
is an early exit on `ctx` cancellation while queued, which the old code
reached one iteration later anyway.

Findings, and what was done with each:

- **Error message was non-deterministic and misleading (fixed).** The
  original message (`"registration attempt already in progress"`) was a
  static string regardless of cause, and — since `ctx` can already be
  expired at the moment `acquireAttempt` is called while the slot happens
  to be free, in which case Go's `select` picks a case at random — the
  same input could produce either a real network error or this message,
  roughly coin-flip. Fixed: the message now wraps `ctx.Err()` (guaranteed
  non-nil whenever `acquireAttempt` returns false, since that's the only
  path that produces `false`), so it both names the real cause and
  supports `errors.Is(err, context.DeadlineExceeded)`.
- **One of the two original tests didn't test what it claimed (fixed).**
  `TestBackgroundLoopStillAcquiresAttemptSlotOnceFree`'s doc comment said
  "The background loop (run, via Init)…" but the original version never
  called `Init` — it called `EnsureRegistered` directly, which passed
  against the *old* mutex code too (not a regression test), and left
  `run()` at 0% coverage. Rewritten to call `Init` and assert on the
  loop's own fetch completing.
- **The new early-return path in `run()` had zero coverage (fixed).**
  Added `TestBackgroundLoopExitsPromptlyWhenCancelledWhileQueuedOnAttemptSlot`
  (above). Post-fix coverage: `run` 96.0% (up from 0% on this specific
  path — the function overall was previously exercised via other
  pre-existing tests, but never this branch).
- **The genuine red-first test failed by 10-minute package timeout, not
  by assertion (fixed).** The original release-on-`t.Cleanup` pattern
  meant the release only happened after the (still-blocked) test function
  returned — which never happens on its own against the old code. Changed
  to release from an independent timer goroutine (matching the pattern
  the other new test already used), so the old-code regression now fails
  fast (~600ms) with the actual assertion message instead of a bare
  timeout.
- **Cross-goroutine unpaired acquire/release, and same-class hardening
  (accepted as noted, plus one improvement).** Both new tests acquire in
  one goroutine and release in another. The reviewer traced this as safe
  as written (a cap-1 buffer means no other receive can succeed while
  full, so the unpaired receive provably drains the seeded token) but
  flagged it as one careless edit away from reproducing the exact hang
  `resetState`'s comment documents. Added `releaseAttempt`'s panic-on-
  unpaired-call (see below) as a direct mitigation: a future mistake of
  this shape now fails loudly and immediately instead of hanging.
- **Bare (non-`defer`) release in `run()` vs. `RegisterNow`'s `defer`
  (accepted, not changed).** Noted as a nit: a panic inside
  `fetchSigningKey`/`registerDevice` would leak the slot in `run()`. Moot
  in practice — an unrecovered panic in a goroutine kills the process
  regardless — so left as-is per the reviewer's own assessment; not worth
  restructuring for no behavioral gain.

**`resetState()` history, confirmed clean.** An earlier draft of this fix
added a defensive drain of `attemptSem` in `resetState()` — meant to guard
against a test leaving the slot held. It shipped, and `go test -race
-count=5` hung for 485s. Root cause, confirmed via goroutine dump: several
pre-existing tests in this file call `Init(context.Background(), …)` and
never cancel it, so `run()`'s background goroutine can genuinely still be
running (holding the slot) well after the test that started it returns. A
non-blocking drain can't distinguish "stale token left behind" from
"another goroutine's live token" — it drains either. Stealing a live token
let a second goroutine wrongly believe it held exclusive access while the
first was still running; when the first later released, it drained the
*second* goroutine's token instead of its own, permanently blocking that
goroutine's own release on an empty channel. The drain was removed before
this went to review (production code always pairs acquire/release via
`defer`, so nothing legitimate needed cleaning up); the independent
reviewer confirmed via repo-wide grep that no non-blocking receive/drain
remains anywhere, and confirmed no hang across `-count=8` and `-count=30`
under `-race` independently.

## What was verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...` clean (both scoped and
  whole-repo).
- Full `go test ./...` green — no regressions anywhere in the suite (run
  twice: once before the review's findings were fixed, once after).
- `go test -race -count=15 ./internal/enroll/...` green (~19s) — well
  beyond the `-count=5` that originally caught the `resetState` hang.
- `golangci-lint run ./...` (whole repo): 0 issues.
- Relevant CI guards: `guard-data-access` (no SQL anywhere in the diff),
  `guard-kiosk-engine`, `guard-i18n` (the new/changed error string is a
  Go-side error, not a rendered template string — guard confirms no
  hardcoded Go-side response string was introduced), `guard-compliance-
  claims` — all pass.
- Coverage on the touched functions after the review's fixes:
  `acquireAttempt` 100%, `run` 96.0%, `RegisterNow` 89.5% (the untouched
  remainder is a pre-existing branch unrelated to this change),
  `releaseAttempt` 50% (the panic branch is a defensive assertion, not
  meant to be hit in normal test flow).
- No SQL, no filesystem writes, no UI/template/locale/migration change,
  no offline-first impact — backend concurrency fix confined to
  `internal/enroll` plus one doc comment in `internal/pages`.
- No real client/shop name or secret-shaped literal in the diff.

## Safe-to-merge verdict

**Yes.** Independent review found no blocker-class issue; the concurrency
fix is sound (no lost wakeup, no deadlock, `run()`'s retry semantics
unchanged) and the real failure mode it targets (a caller queuing behind
the background loop's longer-bounded work) is fixed and now covered by a
genuine red-first regression test. All review findings (error-message
accuracy, two test-quality gaps, one hardening opportunity) were fixed in
this same branch before merge; the one accepted-as-is nit (`run()`'s bare
release vs. `defer`) is genuinely moot per the reviewer's own analysis.
