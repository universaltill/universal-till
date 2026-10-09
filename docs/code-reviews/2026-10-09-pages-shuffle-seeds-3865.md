# Review: run internal/pages under pinned -shuffle seeds (ut-docs#3865)

Branch `ci/3865-pages-shuffle-seeds`, lane:cloud-54. Reviewer: independent
subagent on a different model from the author.

## What shipped

- `.github/workflows/ci.yml`: new `pages-shuffle` job, a matrix of two
  pinned seeds (`1791386291628608919`, `22`) running
  `go test -timeout 20m -count=1 -shuffle=<seed> ./internal/pages`. It runs on
  PRs and on pushes to `main`. It has no `needs:`, so it runs beside `build`
  and adds no wall-clock time. It never uses `-shuffle=on`.
- `internal/pages/main_test.go`: a comment pointing `resetProcessGlobals`
  readers at the job.

It's a separate job because `build` already runs this package three times
(~3.5 min each). Running it beside other test binaries is what pushed it
past the default timeout before (ut-docs#1992).

## Verification beyond CI

- Both seeds pass locally on the branch (213 s each).
- **Leak check (acceptance):** a throwaway test that stubbed
  `saleGridFirstPaint`, `basketFirstPaint` and `fiscalSignAskBudget` and
  never restored them made both seeds fail. Seed 1791386291628608919 had 4
  failing tests and seed 22 had 12+, including `TestIndex_InlinesSaleScreenGrid`
  and the fiscal-sign tests. The leak was not committed.
- **The card's literal example does not fail:** a leaked
  `httpx.SetDefaultLocale("tr")` passes under both seeds, and so does a leak
  of `pairingJoinNow`/`bluetoothPlatform`. `chdirRoot`'s `resetProcessGlobals`
  (ut-docs#3822) restores the httpx baseline before nearly every test. That
  mechanism covers the httpx class, so the job's value is in unrestored
  pages-level seams. Don't "fix" the job to make the httpx example fail.
- actionlint v1.7.7: clean.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The job comment over-claimed catching a missed `httpx.Init*`. The httpx example from the card doesn't fail (see above). | Fixed: the comment now says a leaked httpx global is mostly undone by `resetProcessGlobals`. Recorded here. |
| 2 | minor | No job-level `timeout-minutes`, so a wedged setup would run for 6 h. | Fixed: `timeout-minutes: 30`. |
| 3 | nit | Two seeds is thin coverage. More legs would cost runner minutes, not wall clock. | Accepted: two seeds is what the card asks for. Add more if a leak slips through. |
| 4 | nit | The check name embeds the seed, so changing a seed would rename the check if it were ever required. | Accepted: the repo has no required status checks today. |
| 5 | nit | Comment accuracy (timings, three runs in build). | Verified accurate. |

The reviewer also checked: the seed fits int64, and Go rejects anything past
that bound. Quoting the seeds as strings avoids JSON precision loss. The
triggers are correct. setup-go `1.27` matches the other jobs. Branch
protection has no required checks, so this change is purely additive.

## Verdict

Safe to merge. Nothing deferred.
