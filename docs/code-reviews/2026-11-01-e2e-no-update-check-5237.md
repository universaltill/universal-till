# Review: e2e tills never poll the real releases URL (ut-docs#5237)

Date: 2026-11-01 · Lane: `lane:cloud-41` · Built by Sonnet, reviewed by Opus 5.5 (fresh context)

## What shipped

- `e2e/tests/worker-till.ts` and every `e2e/run-till*.sh` default
  `UT_UPDATE_CHECK=0` (overridable), next to the ut-docs#3814
  `UT_LAN_DISCOVERY` line. `run-till.sh` is the docs-shots till, so a new
  GitHub release can no longer change the "Software update" card in the
  screenshots.
- `run-till-incoming-notes.sh` forces `UT_UPDATE_CHECK=1`: its spec needs
  checks on, against the loopback fake `UT_UPDATE_RELEASES_URL`.
- `Makefile` `e2e` target and the ci.yml e2e app line set `UT_UPDATE_CHECK=0`.
- New guard `scripts/ci/guard-e2e-no-update-check.sh` (+ `_test.sh`, 14
  cases, fixtures in `mktemp -d`), wired into ci.yml's `build` job. A till
  launcher (code that sets `UT_LISTEN_ADDR`) must set the check off or point
  `UT_UPDATE_RELEASES_URL` at `127.0.0.1`/`localhost`. It is modelled on
  `guard-e2e-no-lan-discovery.sh`.
- README: one paragraph in "LAN discovery (developers and testing)".

## Verification

- TDD: with the guard in place and the launchers at `origin/main`, the guard
  test failed exactly "the real e2e/ launchers" and "default run". The
  guard flagged 9 files and not the incoming-notes till. After the fix,
  14/14 pass. Dev ran this and the reviewer re-ran it independently.
- `npx playwright test tests/landscape-phone-sale-2918.spec.ts
  tests/tablet-tier-totals-416.spec.ts --project=default --workers=2` with no
  env override gave 49 passed, including the 667x375 pinned-Pay case.
- `UT_UPDATE_CHECK=0 npx playwright test tests/incoming-release-notes-3940.spec.ts
  --project=incoming-notes` gave 2 passed, which shows the forced `=1` holds.
- `shellcheck scripts/ci/*.sh e2e/*.sh` gave 0 issues. `guard-ci-tests-sandboxed.sh`,
  `guard-pipefail-grep-q.sh` and `guard-e2e-no-lan-discovery_test.sh` are ok.
- The reviewer confirmed that no default-project or docs spec relies on a real
  update check. `portrait-tablet-sale-3050` injects its own chip, and the
  `checks_off` follow line renders only on a replica. No other till launcher
  under `e2e/` was missed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | The guard doesn't scan ci.yml's e2e app line. The precedent guard has the same gap. | Accepted; the line is set by hand in this diff |
| 2 | low | Regex false passes (`=01`, a value inside an `echo`, a later `=1`, `127.0.0.1$X`). These are contrived for a harness guard. Non-loopback hosts such as `127.0.0.1.evil.com` are correctly refused. | Accepted |
| 3 | nit | False fails on quoted `'0'`, `=False` or `=f`. This is the safe direction, and the error says the fix. | Accepted |
| 4 | low | The incoming-notes till inherited a caller's `UT_UPDATE_CHECK=0`. | Fixed: `export UT_UPDATE_CHECK=1` |
| 5 | nit | The docs-shots `.sb-update` mask comment was stale. | Fixed: comment updated, mask kept as backstop |

The manual Settings "Check for updates" button still reaches the releases
URL with checks off, which is deliberate (`setup_update_check.go`). No default
or docs spec clicks it.

## Verdict

Safe to merge. It changes only the test harness, with no product behaviour change.
