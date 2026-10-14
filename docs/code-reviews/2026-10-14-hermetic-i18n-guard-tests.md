# 2026-10-14 — i18n guard tests run in a sandbox; drain test gets boot headroom (ut-docs#4258)

## What shipped
- **Root cause of the `internal/pages` "ar.json: unexpected end of JSON input" flake:** no Go test writes `web/locales`. The writers are the shell regression tests for `scripts/ci/guard-i18n.sh`:
  - `guard-i18n_verbcheck_test.sh` rewrote the real `web/locales/{en,tr,ar,fa}.json` in place. Its `json.dump(open(p,"w"))` truncates the file first.
  - `guard-i18n_dupkey_test.sh` planted a file in `web/locales`.
  - The other four tests planted fixtures in `web/ui`, `web/public` and `internal/pages/*.go`.

  A `go test ./...` running alongside them read a half-written locale file or compiled a planted `.go` file.
- **New `scripts/ci/i18n-guard-sandbox.sh`:** a sourced helper. It copies exactly what `guard-i18n.sh` reads (the guard itself, `web/{ui,public,locales}` and `internal/**/*.go`) into a `mktemp -d` and `cd`s there. All six `guard-i18n_*_test.sh` run inside that copy, and their EXIT traps remove it. If the copy fails, the helper removes the half-built sandbox itself.
- **New guard `scripts/ci/guard-i18n-tests-sandboxed.sh`** and its `_test.sh`, both wired into `ci.yml`'s build job. The guard fails when a `guard-i18n_*_test.sh` does not source the helper and call `enter_i18n_guard_sandbox`.
- **`internal/app` `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError`:** under `-race` plus a concurrent `go test ./...`, a healthy boot took 10.1–10.3s against a 10s wall-clock bound.
  - The drain-timeout ERROR log check is now documented as the signal that catches the regression.
  - The wall-clock bound is now drain timeout + 10s, and only catches a shutdown that hangs.
  - The outer timeout went from 30s to 60s.
  - No production code changed.

## Review
Independent review by a different model in a fresh context, in a separate worktree. No blockers and no should-fix items. Nits:
1. With the new bound, the wall-clock check can no longer catch the regression: a regression took 19.9s against the 20s bound, and only the log check caught it. **Fixed:** the comment and failure message now say the bound only catches a hang.
2. The static guard is a line grep. It can be fooled by an enter call inside `if false`, by a later `cd` back into the real tree, or by a heredoc. **Partly fixed:** a fixture now proves that a commented-out enter call is rejected. The other evasions need deliberate effort and are accepted.
3. A sandbox could leak in /tmp if the copy failed before the trap was installed. **Fixed** in the helper. I checked the failure path: it returns 1 and no tmp dir is left.
4. The sandbox copies all of `web/public` (26M) when the guard only needs `*.js`. **Accepted** as a deliberate choice: a partial copy would change what "the unmodified tree" means. The copy costs seconds.

## Verified beyond automated tests
- **Before the fix:** a marker file was touched, then `guard-i18n_verbcheck_test.sh` was run. `find web internal scripts/ci -newer marker` listed `web/locales/{en,fa,tr,ar}.json`.
- **After the fix:** all six tests, the new guard and its test pass, and the same `find` prints nothing.
- **Reviewer's re-check of the guard:** reverting only `guard-i18n_verbcheck_test.sh` brought the real-tree writes back, and the new guard failed and named that file.
- **Reviewer's re-check of the drain test:** removing `stopBg()` from Run's deferred cleanup makes `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError` fail on the log signal ("background services still running 10s after cancel").
- **Shellcheck:** 0.9.0 (CI's pin) reports 0 issues on `scripts/ci/*.sh`. 0.11.0 locally, with SC2329 excluded, also reports 0.
- **Lint and guards:** `golangci-lint run ./...` reports 0 issues. Every one-line `scripts/ci` step of `ci.yml`'s build job passed locally, except the two shellcheck steps, which fail only because the local version is 0.11 rather than 0.9.0.

## Deferred
- Other non-i18n guard tests also plant fixtures in the real tree. `guard-demo-env_test.sh` was seen planting `internal/pages/zz_guard_demo_env_environ_allow.go`. Filed as ut-docs#4311.

Verdict: safe to merge.
