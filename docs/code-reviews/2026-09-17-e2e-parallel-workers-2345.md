# Code review: parallel Playwright E2E workers, one till per worker

**Card:** universaltill/ut-docs#2345
**Branch:** `fix/2345-e2e-parallel-workers`
**Complexity:** hard — Dev via a `fable`-model subagent, review at Opus
(deliberately not Fable, per the hard-tier routing rule)

## What shipped

The `default` E2E project (134 of 138 spec files) used to drive ONE
shared `unitill-pos` server (`internal/pos.Engine` is a server-side
singleton), which is why the whole suite ran at `workers: 1`. This
raises `workers` to `process.env.CI ? 4 : 2` by giving each *worker* its
own till server instead of sharing one, while leaving the other 4
projects (`auth`/`ai-identify`/`layout`/`diagnostics` — 1-5 files each,
one static server each) untouched in mechanism.

- `e2e/tests/worker-till.ts` (new): boots a per-worker till on port
  `9091 + parallelIndex`, its own throwaway data dir + seeds, torn down
  when the worker ends. Locally, an already-healthy till on that port is
  reused (mirrors `reuseExistingServer`); in CI a busy port is a hard
  error (a leaked process, not silently shared).
- `e2e/tests/fixtures.ts`: a worker-scoped `workerServerURL` fixture
  (only active on the `default` project, via the `e2eWorkerServer`
  project option) and a test-scoped `baseURL` override that forwards it.
  `baseURL` had to stay test-scoped — Playwright refuses to re-register a
  fixture at a different scope — independently confirmed against
  Playwright's own source, not just the code comment.
- `e2e/global-setup.ts` (new): builds the till binary ONCE per run into
  `e2e/.bin/` (gitignored), instead of once per worker.
- `e2e/playwright.config.ts`: removed the `default` project's static
  `webServer` entry; the other 4 projects' entries, ports and scripts are
  untouched. Each of those 4 now carries its own `workers: 1`
  (`STATIC_SERVER_WORKERS`) — found live on the first 4-worker run: the
  `auth` project has 5 spec files, and without this cap Playwright spread
  them across workers against the same shared till, breaking
  `login.spec.ts`'s file-sort-order dependency on running first.
- `.github/workflows/e2e.yml`: `actions/cache@v4` for
  `~/.cache/ms-playwright`, keyed on the lockfile, before the install
  step.
- 4 spec files (`categories-record-dialog-2010`, `pos-divider-resize-2308`,
  `sale-screen-scan-focus-search-423`, `settings-pos-notice-918`) plus
  `helpers.ts`'s `setOskMode`: real root-cause fixes for latent races the
  parallel run exposed (see "Independent review" below for what verified
  each is genuine, not a papered-over flake).
- `e2e/tests/bugreport-panel.spec.ts`: the one hardcoded-`8091` assertion
  now reads the test's own dynamic `baseURL`.
- New regression test `worker-server-isolation-2345.spec.ts`.

## Independent review

Opus, isolated worktree, cold context — genuinely different model from
the `fable` subagent that built this, per the hard-tier routing rule.
Re-verified every load-bearing design claim against Playwright's own
source rather than trusting code comments (the scope-registration
constraint, the fixture-forwarding mechanism reaching `page`/`context`/
`request`, worker-slot-recycling safety, the 4-race-fixes' root causes),
ran the real suite itself (2 full `CI=1` 4-worker runs, one with a real
failure it reproduced and diagnosed), and ran the 4 static projects
independently.

**Verdict: sound architecture; two findings fixed before merge, two
deferred/documented, two accepted as-is.**

| # | Severity | Finding | Resolution |
|---|---|---|---|
| F1 | Should-fix | `sale-screen-camera-barcode-scan-548.spec.ts` had the identical `delay: 5` + `Promise.all(waitForResponse, press('Enter'))` pattern the #423 spec was rewritten to remove — the reviewer's own run hit the exact #423 failure mode there (buffer reset, item never rang up) once in 2 runs. | Extracted `scanAtScannerSpeed` from the #423 spec into `helpers.ts` (parametrized by barcode), used at both call sites. Re-run: 11/11 passing across both spec files. |
| F2 | Should-fix | `workerServerURL`'s worker-scoped fixture had no explicit `timeout`, so it inherited `project.timeout` (30s) instead of the 60s `BOOT_TIMEOUT_MS` the code's own error message promised — proven, not inferred: a forced stuck boot produced Playwright's own 30s fixture-timeout error, never the intended one. The binary-build fallback path (~47s cold) couldn't fit in 30s at all. | Added `timeout: 120_000` to the fixture registration, matching the old `webServer` entry's own timeout. |
| F3 | Should-fix | `worker-till.ts`'s `onExit` handler killed the child on process exit but never removed the temp data dir; the reviewer reproduced the leak two independent ways (forced fixture timeout, occupied port). | `onExit` now calls `cleanup()` too (`fs.rmSync` is sync, legal in an `'exit'` handler); the listener itself is now declared outside the `try` block so both the success and catch paths can remove it. |
| F4 | Deferred, documented | `internal/server`'s `UT_LISTEN_ADDR` is a *preference*: a busy port silently falls back to base+1..+20, which could land one worker on a sibling worker's own port. Not reachable on a clean CI runner (each worker's port is genuinely free); a real risk only when several agent worktrees share a host and ports are a global resource. | Closed rather than left deferred: `worker-till.ts` now watches the child's stderr for the server's own `"was busy"` log line and fails the boot immediately if seen, instead of silently drifting onto a neighboring port. |
| F5 | Should-fix (docs) | `e2e/README.md`'s new text claimed the 4 static-server projects "have too few files to ever be split across workers" — false, and contradicted by the very fix (F-in-code above) that exists because `auth` (5 files) *was* split. | Rewritten to state the real mechanism (the per-project `workers: 1` cap actively prevents the split) plus two small pre-existing staleness fixes noticed while there ("both projects" → "all 5"; "`--project=auth` # just login.spec.ts" → "...and its siblings"). |
| F6 | Accepted, noted | CI cache `restore-keys` will accumulate an old Playwright browser version alongside a new one across a version bump (~150MB growth), rather than evicting it. | Left as-is — a conscious, minor trade-off (still a real cache hit on an unrelated lockfile change), not a defect. |
| F7 | Should-fix, found post-review | The orchestrator's own final re-verification run (after applying F1-F5, before merge) hit a genuinely new flake: `tender-panel-reachable.spec.ts`'s held-sales-chip test sampled `.held-chip.count()` once right after the hold modal closed. `#held-sales` is NOT part of the `/api/pos/hold` response's own `#basket` swap — it refreshes itself via a *separate* `hx-get="/ui/held" hx-trigger="held-changed from:body"` round trip (`internal/pages/hold_api.go`) that nothing in the test had waited on. Same class of bug as F1/the other 4 spec fixes (asserting before an async op the test never awaited completes), just in a 6th file the Opus pass didn't happen to hit in its own 2 runs. | Changed the one-shot `.count()` + `expect(...).toBe(...)` to a polling `expect(locator).toHaveCount(...)`, which waits out the second round trip instead of sampling once. Re-run 10/10 green; a further full-suite run confirms no regression. |

No second review round: none of F1-F7 is money/tax/data-loss/security —
the fixes above were applied directly and re-verified against the
specific area each touched, per the process's "earn a second round"
threshold.

## Verified beyond automated tests

- **Port-collision reasoning independently re-derived, not trusted**:
  `9091 + parallelIndex` for any non-negative `parallelIndex` is provably
  `> 8096` (the highest static/fake-cloud port in use), so collision with
  the 4 untouched static projects is impossible regardless of a future
  `workers` increase.
- **Worker-slot-recycling safety independently verified against
  Playwright's own source**: `_runJobInWorker` awaits the failed worker's
  `stop()` (which runs fixture teardown, including `till.stop()`) before
  a replacement starts in the same slot — no port-handoff race.
- **The 4 "latent race" fixes are root-cause, not band-aids** — each
  verified against the actual product code the race depends on
  (`settings.html`'s real `reload()` call, `categories_page.go`'s real
  `HX-Redirect`, `index.html`'s real `hx-trigger` values), not just the
  dev's own claim.
- **Full `CI=1` (4-worker) runs, several rounds**: 2 clean runs during the
  Opus review itself (571-572/572 passing; one run's single flake was the
  #423/#548 wedge-scanner race, independently reproduced and fixed as F1);
  1 more after applying F1-F3/F5 (571/572, 1 flaky — the held-sales-chip
  race that became F7 above); 1 final run after fixing F7: **572/572
  passed, 0 flaky, 7.2 minutes**, including the exact previously-flaky
  case (`held-sales chips … at 1024x600 (kiosk floor), 1-3 held sales`)
  passing cleanly. No leaked ports/processes/temp dirs afterward.
- No leaked ports/processes/temp dirs after any run, including the one
  run that contained a real failure + worker restart.
- `go build ./...`, `go vet ./...`, `gofmt -l .` clean. No non-`e2e`
  files touched except `.github/workflows/e2e.yml`.
- `bash scripts/ci/guard-e2e-fixtures-import.sh` — still passes (specs
  unaffected by this diff still import `test`/`expect` from `./fixtures`).
- No secret-shaped literal, no real client/shop name.
- No i18n/manual implication — this is CI/test infra, no product UI
  string or behavior changed.

## Deferred / follow-up candidates

- **F4's underlying `listenWithFallback` preference-not-binding
  behavior** in `internal/server` itself is unchanged (out of scope for
  this card — a broader product change, not an E2E-harness fix); this
  diff only makes the E2E harness fail loudly the moment that fallback
  fires instead of drifting silently.
- **F6** (CI cache growth on a Playwright version bump) — noted above,
  not filed as a separate card; revisit if cache size becomes a real
  problem in practice.

## Verdict

**Safe to merge.** No correctness, security, offline-first, money, or
data-access issues — the POS server itself is untouched by this diff
outside of how the E2E harness drives it. F1-F3 and F5 fixed and
re-verified; F4 closed with a cheap, targeted guard; F6 accepted as a
minor, conscious trade-off.

**Not merging this automatically**, same as several sibling PRs opened
this cycle: `ut-docs#2277` (Admin Review, unresolved) is asking whether
this pipeline's standing auto-push authorization ("no real users yet")
still holds given evidence of a live till fleet. This PR is pushed and
open for CI (reversible, no live effect) but deliberately held unmerged
until a human answers that card.
