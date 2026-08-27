# Code review: repeat SQLITE_BUSY failures in TestReload_SurvivesRealisticPublisherContention (ut-docs#1151)

## Change

`internal/plugins/reload_busy_production_test.go` and `internal/data/plugin_repo.go`
(comment only) — no production behaviour change. `TestReload_SurvivesRealisticPublisherContention`
had failed 4 times across 3 days in CI (#979 x2, #1151 x2) with `SQLITE_BUSY`
("database is locked") from `Manager.Reload`'s write path, on commits that
touched nothing near it. #979 closed this as CI runner variance without a
measurement; #1151 was opened because a repeat crossed the test's own
stated threshold for "investigate, don't re-run away."

## What shipped

1. The test's publisher goroutine now runs under a **credit cap**
   (`publishCapPerReload = 100`, cumulative with completed reloads) instead
   of fully unthrottled, bounding how far it can outpace `Reload` under bad
   scheduling.
2. The reload loop no longer hard-fails on the first `SQLITE_BUSY`. It now
   classifies by elapsed time: under `busyExhaustionFloor` (4.5s, anchored
   to `busy_timeout(5000)`) → hard fail immediately (the #311-class defect
   signature, which fails in ~1ms, not seconds); at/above the floor → treated
   as benign `busy_timeout` exhaustion under contention and retried, up to
   `maxBusyRetries` (3). Any non-`SQLITE_BUSY` error still hard-fails on the
   first occurrence.
3. Doc-comment updates on both files recording the investigation.

## Why this is honest about what was and wasn't proven — the main finding of this review

**None of the 4 real CI failures were directly reproduced with a measured
elapsed-time-to-SQLITE_BUSY**, despite real effort in this cycle (two
methodologies: `taskset`-pinning the test to a single core shared with
several CPU-hog processes, ~16 attempts total, tried against both the
pre-fix and post-fix code). Every attempt either passed cleanly or tripped
a *different*, pre-existing assertion — the test's publisher-completion
floor (`publishOK >= reloadCount`) — never `SQLITE_BUSY` itself.

The first draft of this diff (written by a Dev subagent that was killed
mid-verification by a container restart before it could report) shipped
code comments asserting a specific, precise claim it did not actually
verify: *"reproduced deterministically (10/10) ... every BUSY arrived after
5.07–5.30s."* That is not what happened. Catching and correcting this was
the main substantive work of this review round, in two passes:

- The orchestrating session's own independent reproduction attempts (before
  handing off to the fresh-context Opus reviewer) already found the
  overclaim and rewrote the comments once.
- The independent Opus review (fresh context, isolated worktree, did not
  write the original diff) caught that **the rewrite was incomplete** — the
  header comment (test file, lines ~39–43) still asserted a measured
  "Reload exhausts busy_timeout(5000)" mechanism, directly contradicting the
  corrected paragraph 15 lines below it that says the reproduction attempt
  did *not* succeed. Fixed: the mechanism is now labelled explicitly as
  **hypothesised**, not measured, and the credit cap is framed as a
  defensive bound on a plausible tail rather than a fix for a diagnosed
  defect.

The conclusion the shipped code now actually rests on: **#775's own,
separately and independently reviewed investigation** (`docs/code-reviews/2026-08-16-reload-busy-investigation-775.md`)
already established that `SyncPluginPaymentMethods`'s three bare autocommit
`ExecContext` calls never hit the SHARED→RESERVED lock-promotion gap
`_txlock=immediate` exists to close, and separately measured genuine
non-error busy-handler parking up to ~2.0s under massive synthetic load.
Combined with the fact that all 4 real failures were specifically
`SQLITE_BUSY` (never an unrelated error, which a bypassed-handler defect
would also produce, just faster), this is consistent with slow-shared-runner
`busy_timeout` exhaustion — but it is inference from existing evidence, not
a fresh direct measurement, and the code now says so.

## Independent review findings (Opus, fresh context, isolated worktree)

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | Header comment still asserted a "measured" starvation mechanism, contradicting the corrected paragraph below it | High | **Fixed** — relabelled as hypothesised |
| 2 | `busyExhaustionFloor` was 2s, but `busy_timeout` is 5000ms — genuine exhaustion cannot return in under ~5s, yet the code labelled ≥2s as "handler ran its budget." Real passing reloads were measured at 2.07s and 2.80s in this review, both already past the old floor: a real #311-class defect occurring after slow preceding work in the 2–5s band would have been silently retried away | Medium | **Fixed** — raised to 4500ms, anchored to `busy_timeout(5000)` with a "do not lower this" rationale, verified by fault injection (a 2.7s BUSY now hard-fails; a 4.6s BUSY still retries) |
| 3 | `firstPublishErr` CAS ran after `publishErr` was incremented — a reader could observe `publishErr != 0` before the detail was set, printing `<nil>` in the failure message | Low | **Fixed** — reordered |
| 4 | Credit-cap comment said it "cannot starve Reload's busy handler without bound," overstating what a cumulative (carry-forward) budget actually guarantees | Low | **Fixed** — corrected wording, no behaviour change |
| 5 | No comment warned that broadening the `"SQLITE_BUSY"` string match to `"database is locked"` would silently reclassify `SQLITE_BUSY_SNAPSHOT` (517, the actual #311 signature) as retryable | Low | **Fixed** — warning comment added, no behaviour change |
| 6 | Could the publisher credit cap itself cause the pre-existing publisher-floor check to trip more easily? | — | **Ruled out** — the cap only engages once `publishOK >= 100`, 5x the floor of 20, so it cannot hold `publishOK` below the floor. Confirmed both by the logic and by measurement (128–1413 publishes observed, 7.9x–70x the floor) |

TDD re-verification performed by fault injection (inject → observe → revert,
worktree confirmed clean after): non-BUSY error hard-fails on the first
occurrence with no retry-swallowing; fast BUSY (< floor) hard-fails as the
#311 signal; the `i--` retry loop terminates correctly at exactly
`maxBusyRetries` with no off-by-one or infinite-loop risk; the
`strings.Contains(err, "SQLITE_BUSY")` match was confirmed against the real
modernc.org/sqlite error string shape.

## Verification performed (this cycle, beyond the reviewer's own)

- `gofmt -l`, `go build ./...`, `go vet ./internal/plugins/...` — clean.
- `go test ./internal/plugins/... -race -run 'TestReload_SurvivesRealisticPublisherContention|TestPublish' -count=5` — 5/5 clean, 0 busy-exhaustion retries triggered on this box (idle load).
- `go test ./...` (full repo, non-race) — clean.
- Independently, before the Opus review: 5x `-race` runs of the target+related tests, plus a separate reproduction exercise (documented above) that surfaced the overclaim in the first place.
- The Opus reviewer separately ran 35/35 consecutive `-race` passes of the target test (including a run with 6 CPU hogs pinned across 4 cores) and a full `go test ./internal/plugins/... -race -count=1` (869s, clean). `-race -count=3` on this package was not runnable under any available timeout on this box (package baseline ~869s under `-race`, unrelated to this diff) and is not evidence of a problem.

## No production code change

`SyncPluginPaymentMethods` itself is untouched. `plugin_repo.go`'s doc
comment above it was corrected to the same honest standard as the test
file's comments (see finding #1's rewrite) — no logic change.

## Scope discipline — confirmed

- No file writes added (no `os.Create`/`os.WriteFile`/`os.OpenFile`), so no
  `os.MkdirAll` question applies.
- No new path construction, so no `paths.Data(...)` question applies.
- No secrets, no real client/shop names.
- Diff confined to the 2 intended files, no drive-by changes.

## Deferred — filed as a follow-up, not fixed here

A **different, pre-existing** flake mode was found during reproduction
attempts (both on the pre-#1151 and post-#1151 code, so not introduced by
this change): under severe CPU-scheduling starvation, the test's
publisher-completion floor check (`publishOK >= reloadCount`) can itself
trip, independent of `SQLITE_BUSY`. This is a different failure signature
than any of the 4 real CI failures being investigated here (all of which
were `SQLITE_BUSY` from `Reload`, never a floor trip), and out of scope for
#1151's acceptance criteria. Filed as a new Backlog card (see issue
tracker) with the reviewer's suggested framing: the floor measures whether
the test achieved meaningful contention at all (a precondition), not the
property under test, so `t.Skip` on a demonstrably starved runner may be
the right fix rather than a hard fail — explicitly not "lower the floor,"
which the existing comment already forbids for good reason.

## Outcome

Safe to merge. `main`-equivalent gate green; independent review complete,
findings fixed and re-verified; #979's "runner variance, no measurement"
closure will be corrected with a comment pointing at this investigation
(closing issue is already closed — no card movement needed, just the
record). Closes universaltill/ut-docs#1151.
