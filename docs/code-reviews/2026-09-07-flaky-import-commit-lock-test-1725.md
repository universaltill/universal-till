# Code review: deterministic fix for flaky import-commit-lock test (ut-docs#1725)

**Branch:** `fix/1725-flaky-import-commit-lock-test`
**Author:** Farshid Mirza (pipeline, lane:cloud-54)
**Reviewer:** independent Opus subagent (fresh context, isolated worktree), per
`MODEL-ROUTING.md`'s medium-complexity review tier

## What

`TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond`
(`internal/pages/import_commit_lock_test.go`) was reported flaky (3/5, later
5/5 locally). Root cause, established with reproduction evidence before any
fix was written:

- `reserveImportCommit`'s mutex+map exclusivity (`internal/pages/import_stage.go`)
  is correct — a direct 1000-call concurrent-goroutine test against it in
  isolation (bypassing HTTP entirely) never once let two callers hold the
  same hash, clean under `-race`.
- The test itself was racy in the *other* direction: it started two
  goroutines via a closed `start` channel and trusted the OS scheduler to
  make them collide inside `reserveImportCommit`. Nothing guarantees that —
  one goroutine's entire handler (parse → hash → reserve → insert → release)
  can finish before the other's `ServeHTTP` is even scheduled, in which case
  the second request is an ordinary, unblocked sequential re-import and both
  legitimately answer 200 (this row has no barcode/SKU, so nothing dedupes a
  genuinely sequential repeat either). Reproduced 17/20 fails under
  `GOMAXPROCS=1`, 30/30 under `-race`.

Fix: added `importCommitReserveSync func()` (`import_stage.go`), nil in every
real build, called (nil-checked) in `import_page.go` immediately after a
successful `reserveImportCommit` — i.e. while the reservation is still held.
The test installs a hook there that pauses request A mid-handler, sends
request B only once A's reservation is *guaranteed* held (no scheduler bet),
asserts the 200/409 split, then releases A. A first attempt (a barrier
synchronizing only the two goroutines' *arrival* at the reserve call) still
failed under `GOMAXPROCS=1` — once released from a barrier there is still no
guarantee the runtime interleaves them rather than running the "winner" to
completion first — which is why the final design holds the reservation open
on command instead of merely synchronizing arrival.

## Independent review

Full record of the review pass (Opus, isolated `git worktree`, re-verified
every claim itself rather than trusting the brief — see its
`isolation: "worktree"` run per `reviewer`'s skill):

- **Personally reproduced (not taken on trust):**
  - Reverted to the pre-fix files and ran the old test: `GOMAXPROCS=1
    -count=10` → 9/10 fail; `-race -count=5` → 5/5 fail; default
    `-count=10` → 1/10 fail (explaining how the flake shipped in the first
    place — the default/no-flags path rarely reproduces it).
  - Mutation test (`reserveImportCommit` forced to always `return true`):
    new test fails (not a tautology).
  - New test 20/20 green under default, `GOMAXPROCS=1`, and `-race`.
  - Full `internal/pages/...` package green after the change (no global
    hook-state leakage into other tests — the package has zero
    `t.Parallel()` calls today).
- **[non-blocking, fixed] Finding 1** — the original mutation-test failure
  mode was an unguarded `close(reserved)`: if the lock were ever broken, a
  second caller reaching the hook would double-close the channel and panic
  the whole test binary (aborting every test scheduled after it in the
  package) instead of failing cleanly with a diagnosis. Fixed: an atomic
  arrival counter now guards the close — only the first caller pauses; a
  would-be second caller (only possible with a broken lock) returns
  immediately instead of double-closing or deadlocking, and the test asserts
  `arrivals == 1` at the end with a message naming exactly what broke.
  Re-verified live: with the same `reserveImportCommit`-always-true mutation,
  the test now fails with `expected exactly 1 caller to hold the reservation
  while B ran, got 2 — the exclusivity lock let a second caller in` instead
  of a panic.
- **[non-blocking, fixed] Finding 2** — `<-reserved` had no timeout: if
  request A ever failed before reaching the hook (a hash error, an early
  return, or a future refactor moving the reservation point), the test would
  hang to the package's 10-minute timeout and kill the whole package run
  instead of failing itself. Fixed with a `select` against a 10s
  `time.After`, with a message naming what to check if it fires.
- **[non-blocking, fixed] Finding 3** — the hook's doc comment overclaimed
  its precedent: it said the seam "mirrors pluginTaxRateAsker's cacheMax"
  (`tax_hook.go`), but `cacheMax` is a per-instance struct field that can
  never leak between tests, while this hook is a package global. Comment
  rewritten to state that distinction plainly and name the actual current
  safety invariant (no `t.Parallel()` in this package) and what would need
  to change if that ever stops holding.
- **[non-blocking, fixed] Finding 7** — the production comment on the hook
  carried ~27 lines of investigation narrative (the 17/20/30/30 numbers, the
  rejected barrier-only design) duplicated near-verbatim in the test file.
  Trimmed to state only what the hook is and does, with a pointer to the
  test for the full history — the narrative belongs with the test and in
  this record, not repeated in production code.
- **[non-blocking, fixed] Finding 5** (pre-existing pattern, tightened while
  touching the file) — `multipartCSV(t, …)`, which can call `t.Fatalf`, ran
  inside the background goroutine; with the rewrite down to a single
  goroutine, request A's body is now built before `go func()` starts it, so
  nothing test-fatal happens off the main goroutine.
- **[not fixed, noted, deliberate]** Finding 4 — the hook fires immediately
  after `reserveImportCommit` succeeds, proving the reservation is held at
  the earliest possible instant rather than across the parse+insert work it
  actually protects; a zero-production-change alternative (pre-reserving the
  hash directly, bypassing the HTTP handler) was also raised. Both are
  reasonable; the chosen placement was a conscious trade (proving the
  handler holds the reservation for the request's real duration) rather than
  an oversight, and reviewer agreed neither is strictly better — left as is.
- **[not fixed, out of scope]** Finding 6 — the 409 body assertion couples to
  the literal English string `"already running"` (`import.error.already_in_progress`
  in `web/locales/en.json`). Pre-existing before this diff, unrelated to the
  concurrency fix; not touched here.
- Confirmed backend-only: `git diff origin/main...HEAD --stat` is exactly
  the three Go files, 89+/16− — no i18n key, no help topic, no UI/template
  change, no money involved, no file writes (so `os.MkdirAll`/`paths.Data`
  are not applicable — confirmed by grep, not assumed), no secret-shaped
  literal, no real client/shop name (test data is `Unkeyed Widget`, `SEQ1`,
  `RACE-SKU-n`).
- Call-path audit: `reserveImportCommit` has exactly one caller
  (`import_page.go`), the hook exactly one call site, placed *after*
  `defer releaseImportCommit(hash)` is registered so a panicking hook still
  releases the reservation. The only other route into the commit path
  (`commitStagedImportForSetup`) re-enters the same mux but no test drives
  it while the hook is set.

**Verdict: SAFE TO MERGE.** All findings were test-robustness/comment-quality
nits; all judged worth fixing were fixed in this same commit before merge.

## Verification

- `gofmt -l internal/pages/*.go` clean, `go build ./...` clean,
  `go vet ./internal/pages/...` clean, `golangci-lint run ./internal/pages/...`
  → 0 issues.
- `go test ./internal/pages/...` green (full package, 146s).
- Full `go test ./...` green across the whole module (every package, not
  just the one touched).
- Target test green 20/20 under default, `GOMAXPROCS=1`, and `-race`
  (`-count=20` each) — re-run after the review fixes, not just before.
- Guards run locally: `guard-data-access.sh`, `guard-i18n.sh` — pass (no-ops
  as expected for a backend-only diff with no locale/UI/data-access surface
  touched).
- **TDD/mutation re-verified personally, twice** — once by Dev/Tester before
  review, once independently by the Opus reviewer in its own isolated
  worktree, and a third time after applying the review's own fixes: (a) the
  OLD test genuinely flakes against the OLD code (`GOMAXPROCS=1 -count=10`:
  9/10 and 7/10 fail across the two independent runs; `-race`: 5/5 and
  30/30 fail); (b) forcing `reserveImportCommit` to always succeed makes the
  NEW test fail — cleanly, with a diagnostic message, after the review's
  fix to finding 1 (previously a panic); (c) the fixed test is green 20/20
  under all three run modes, and the full package shows no state leakage
  between tests.
- No i18n/help-doc/ADR/UI implications — a pure Go test-determinism fix plus
  a nil-by-default production hook.

Closes universaltill/ut-docs#1725
