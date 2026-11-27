# Review: e2e launchers remove their temp data dir on every exit (ut-docs#7177)

Branch `fix/7177-e2e-temp-dir-cleanup`. Built by Sonnet (Dev). Tested by a separate Sonnet Tester. Reviewed independently by Opus 5.5.

## What shipped
- **Root cause, part 1.** Eight `e2e/run-till*.sh` launchers ended in `exec "$BIN"`. The affected files are `run-till.sh`, `-auth.sh`, `-csp.sh`, `-diagnostics.sh`, `-layout.sh`, `-plugins.sh`, `-release-notes.sh` and `-templates.sh`. `exec` replaces the shell, so the `trap 'rm -rf "$DATA_DIR"' EXIT` set earlier never ran.
- **Root cause, part 2.** No Playwright `webServer` entry set `gracefulShutdown`, so Playwright SIGKILLed each process group. The four launchers that already wait on their child (`move`, `notices`, `cutout`, `incoming-notes`) could not clean up either.
- **Result.** Every run left a `tmp.*` dir of about 100 MB per static till: the database, plugins, TLS and a copy of the binary.
- **New `e2e/run-till-lib.sh` (`ut_e2e_serve`).** It runs the binary as a child. A TERM or INT stops the child, waits for it to exit, then exits 143, so the caller's EXIT trap runs only once nothing can still write into the data dir. The trap is installed before the child starts.
- **New `e2e/till-webserver.ts` (`TILL_SHUTDOWN`).** It sets SIGTERM with a 10 s timeout. The till's HTTP drain is capped at 5 s (`internal/server/server.go`). All 17 `webServer` entries across `playwright.config.ts`, `playwright.docs.config.ts` and `playwright.locale-audit.config.ts` use it.
- **New guard `scripts/ci/guard-e2e-till-cleanup.sh`.** It fails when a launcher `exec`s a variable binary, or when a config has a `webServer` `command:` without `gracefulShutdown: TILL_SHUTDOWN`.
- **New `_test.sh`.** It covers the guard cases. It also has a behaviour test: a launcher in its own process group gets SIGTERM, and the test checks that the data dir and the fake till are gone. The same harness reports `leaked-dir` for the old `exec` shape.
- **Wiring and docs.** Both scripts are wired into the `ci.yml` build job, and `e2e/README.md` has a new section.
- The per-worker tills in `tests/worker-till.ts` were already correct (SIGTERM, then `rmSync`, with one shared prebuilt binary). They are unchanged.

## Verification
- **Tester, real Playwright run.** The command was `--project=csp`, which boots all 10 webServers, with `TMPDIR` set to a fresh mktemp.
  - origin/main left **10 `tmp.*` dirs, about 1 GB**.
  - This branch left **0 dirs and 0 till processes**.
- **Tester, Ctrl-C.** After a SIGINT to Playwright mid-run, 0 dirs were left within 15 s.
- **Tester, false-pass checks.** Restoring `exec "$BIN"` in `run-till-csp.sh` fails the guard. Removing one `gracefulShutdown` also fails it.
- **Reviewer, Playwright 1.61.1 source.** `gracefulShutdown` signals the whole process group (detached `sh -c`), then SIGKILLs it after the timeout. A second SIGTERM to the till is harmless (`NotifyContext`).
- **Reviewer, bash behaviour.** Checked: the trap interrupts `wait`, `set -e` combined with `|| rc=$?` behaves, and a till that crashes on its own passes its exit code through and still runs the EXIT trap.
- **After the fixes.** Removing the handler's `wait` from `run-till-lib.sh` now fails the behaviour test (`want gone, got leaked-dir`). Restoring it makes it pass again.
- **Checks.** shellcheck over `scripts/ci/*.sh` and `e2e/*.sh` reports 0 issues. `guard-ci-tests-sandboxed`, `guard-pipefail-grep-q`, every `guard-e2e-*` and its `_test.sh` pass. `npx playwright test --list` loads all three configs.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The behaviour test used `setsid`, which stock macOS doesn't have, so the guard test was red on developer Macs. | Fixed: it now uses `perl -e 'setpgrp(0,0); exec @ARGV'`. |
| 2 | should-fix | The test still passed with the handler's `wait` removed. The fake till died instantly, and bash 5 runs EXIT on TERM anyway. So rm-before-the-till-has-exited went unnoticed. | Fixed: on TERM the fake till takes 0.5 s and writes into the data dir. The test also waits for the till's pid. Re-verified: it fails without the `wait`. |
| 3 | should-fix | Three `# shellcheck disable=SC2016` lines in `_test.sh` had no reason line above them. | Fixed. |
| 4 | nit | `ut_e2e_serve` dropped extra arguments. | Fixed: it passes `"$@"`. |
| 5 | nit | A signal between starting the child and installing the trap could run EXIT while the till was still alive. | Fixed: the trap is installed first. |
| 6 | nit | The four launchers that wait on their child kill it without waiting before `rm -rf`. | Deferred: checklist line on epic ut-docs#7175. |
| 7 | nit | The guard misses some shapes: `exec env X=1 "$BIN"`, `cd d && exec "$BIN"`, `exec ./bin`, launchers not named `run-till*`, and `command:'x'` with no space. The current tree has none of them. | Deferred: checklist line on epic #7175. |
| 8 | nit | Uncovered leaks remain: a second Ctrl-C, a runner crash or Windows still SIGKILLs. `go run` seeds and `go build` stopped mid-start leave `go-build*` dirs, which predates this change. `/tmp/gocache-*` comes from local detached-run tooling, not this repo. | Deferred: checklist line on #7175 (global-setup sweep of stale `.ut-e2e-*bin` dirs). |

Pre-flight: review record updated; `e2e/README.md` updated; till user-visible behaviour n/a (test plumbing only); `web/help/` n/a; en.json n/a; ADR n/a; follow-ups → epic ut-docs#7175 checklist (findings 6–8).

## Verdict
Ship. All findings marked should-fix are fixed and re-verified.
