# Code review: publisher-floor precondition — Skip, not Fail, under scheduler starvation

- **Card:** universaltill/ut-docs#1198
- **PR:** universaltill/universal-till (branch `fix/1198-publisher-floor-skip-not-fail`)
- **Complexity:** medium — built at Sonnet, reviewed at Opus (independent subagent, isolated worktree)
- **Date:** 2026-09-06

## What was wrong

`internal/plugins/reload_busy_production_test.go`'s
`TestReload_SurvivesRealisticPublisherContention` asserts a *precondition*
before its real assertions: that the publisher goroutine completed at least
`reloadCount` (20) publishes across the 20 reloads, as evidence enough
concurrent write load actually occurred for the run to be a meaningful
contention test. Per ut-docs#1198, this precondition check was found
tripping for real under severe artificial CPU/scheduler starvation (the
test process pinned to one core alongside several CPU-hog processes),
reproduced on both pre- and post-#1151 code, always with **zero publish
errors** — i.e. the publisher goroutine simply never got scheduled enough,
not a product regression. The check was a hard `t.Fatalf`, so this
precondition failure reported as a test FAILURE rather than an inconclusive
run, in a case that carries no evidence of an actual defect.

## What changed

- Extracted the floor comparison into a pure helper,
  `publisherFloorMet(publishOK, reloadCount int64) bool`, and changed its
  failure response from `t.Fatalf` to `t.Skip`. The floor **value** itself
  (`reloadCount`, 20) is unchanged — this alters the response to a trip,
  not the threshold, per the issue's explicit instruction not to lower it.
- Added `TestPublisherFloorMet`, a deterministic table-driven unit test
  pinning the boundary (19→false, 20→true, 21→true, 153→true), since the
  real integration test can't exercise this boundary deterministically
  (it depends on real goroutine scheduling).
- Updated the surrounding comments to explain the Skip-vs-Fail reasoning
  and fixed two comments that had gone stale/inaccurate as a result
  (the header's "filed as a follow-up rather than fixed here" — now
  fixed by this PR — and "~100x the floor asserted below," since the
  floor is now a skip condition, not an assertion).

## What the independent review found (Opus, isolated worktree)

**One blocking defect, found and reproduced by the reviewer, fixed in this
same PR before commit:** the new `t.Skip` block was placed **before** the
existing `publishErr != 0` hard-fail. Because `t.Skipf` calls
`runtime.Goexit` (same as `t.Fatal`), nothing after it in the function ever
runs. A genuinely broken publish path drives `publishOK` toward zero (the
counter only increments on success) while `publishErr` climbs — so it trips
the floor *first* and skips, making the `publishErr` assertion — the one
specifically designed to catch this — unreachable in exactly the scenario
it exists for. The reviewer reproduced this directly: forcing every
`bus.Publish` call to fail produced `--- SKIP` with the message "scheduler
starvation, not a product regression," while the same broken path against
the original pre-change code correctly `FAIL`ed. Red → green: a real
detection regression.

**Fix:** reordered the two blocks so the `publishErr != 0` hard-fail runs
first, unconditionally, before the floor/Skip check. Re-verified personally
(see below) that this closes the gap while leaving the intended #1198 case
(floor trip with zero publish errors) still skipping correctly.

**Two non-blocking notes accepted as-is / deferred:**
- The Skip is invisible in non-verbose CI output (`ci.yml`'s
  `internal/plugins` step doesn't pass `-v`), so a test that starts
  skipping regularly would show green with no visible signal. Real, but a
  separate, distinct concern from this card's scope (CI observability, not
  test correctness) — filed as a new Backlog card,
  universaltill/ut-docs#1626, rather than widening this PR to touch shared
  CI config.
- `TestPublisherFloorMet` re-declares its own local `reloadCount = 20`
  rather than importing the integration test's constant (unexported, same
  package, no straightforward way to share without restructuring) — noted,
  harmless today since the helper's semantics are generic.
- Minor comment wording issues (dangling em-dash, "asserted" vs "checked")
  — fixed in this PR.

## Verification (beyond the reviewer's independent pass)

- `gofmt -l`, `go build ./...`, `go vet ./internal/plugins/...`,
  `golangci-lint run ./internal/plugins/...` (0 issues) — clean, both
  before and after the reorder fix.
- `go test ./internal/plugins/... -race` — full package green, twice (once
  pre-reorder, once post-reorder), ~520s each. Real integration test run
  observed: 20/20 reloads, 0 busy-exhaustion retries, publishOK in the
  140–265 range across runs, 0 errors.
- **TDD false-pass check (mine, independent of the reviewer's own,
  independent of Dev's):** mutated `publisherFloorMet`'s comparison
  (`>=` → `>`), confirmed `TestPublisherFloorMet/exactly_at_the_floor`
  failed with the expected message, reverted, confirmed green again.
- **Ordering-fix verification (mine, re-running the reviewer's exact
  reproduction after applying the fix):** forced every `bus.Publish` call
  in a scratch copy to return a synthetic error; the test now correctly
  `FAIL`s with "publisher hit N errors while contending with Reload" and
  the right diagnostic, instead of the pre-fix SKIP. Restored the real
  file from backup afterward; `git diff --stat` and `gofmt` confirmed
  clean.
- `t.Skip` + `t.Cleanup` interaction confirmed safe by the reviewer via a
  scratch test and by observing the real test's publisher-drain complete
  cleanly under `-race` with the Skip branch forced — no hang, no race.
- `scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` both
  pass (test-file-only change: no SQL, no user-facing strings, no money
  types, no file writes — confirmed, not assumed).

## Safe-to-merge verdict

Safe to merge. The blocking defect the independent review found was fixed
and re-verified in this same PR; no other blocking issues remain.
