# 2026-10-15 — every fixture-planting guard test runs in a sandbox copy (ut-docs#4311)

## What shipped
- **Problem:** #4258 sandboxed the six `guard-i18n_*_test.sh` regression tests. Twelve other `scripts/ci/*_test.sh` still planted fixtures in the real tree, and two of them rewrote tracked files in place:
  - `check-brand-assets_test.sh` rewrote `web/ui/pages/login.html`.
  - `guard-price-history-sync_test.sh` rewrote `internal/data/sync_admin_repo.go`.

  A `go test ./...` running alongside them could compile a planted `.go` file or read a half-rewritten one, and a killed run left debris behind. The card listed eight tests. Four more turned up while building it: `guard-plugin-settings-bump`, `guard-price-history-sync`, `check-brand-assets` and `guard-deadcode-baseline`.
- **New `scripts/ci/guard-test-sandbox.sh`:** a sourced helper, the general form of `i18n-guard-sandbox.sh`.
  - `enter_guard_test_sandbox <git pathspecs>` copies the tracked plus untracked-but-not-ignored files under those pathspecs into a `mktemp -d`. It always copies `scripts/ci`, `go.mod`, `go.sum` and `.gitignore` too.
  - It then `git init`s the copy, stages everything, and `cd`s in, so a guard that lists files with `git ls-files` sees the same set as in the real tree.
  - Guards derive their root from their own path, so a guard run from the copy only ever sees the copy.
  - It installs a temporary EXIT/INT/TERM trap until the caller installs its own. `remove_guard_test_sandbox` deletes the copy.
- **12 tests converted.** Each passes exactly the paths its guard reads; for example `'internal/*.go'` for the data-access and netaccess guards, `'*.go'` for demo-env and price-history-sync, and `.` for deadcode. Every cleanup trap ends with `remove_guard_test_sandbox`.
- **New guard `scripts/ci/guard-ci-tests-sandboxed.sh`** (with `_test.sh`) plus a helper test `guard-test-sandbox_test.sh`. All three are wired into the `ci.yml` build job next to the #4258 steps.
  - The guard fails when a `*_test.sh` names a real-tree path under `internal/`, `web/`, `plugins/`, `cmd/`, `e2e/` or `tests/` but does not source a sandbox helper and enter it. A path counts if a variable or array is set to it, or if a redirect, `cp`, `mv`, `mkdir`, `touch`, `tee`, `sed -i` or `rm` targets it.
  - A read-only line takes the reviewed same-line marker `guard-sandbox:allow`. Two existing tests now carry it: `ios-screenshot-bridge_test.sh` only reads `bugreport_panel.html`, and `resolve-marketplace-key_test.sh` names a Go ldflags symbol, not a path.
- **`CLAUDE.md`:** one rule line under "Before committing".

## Review
Independent review by Fable, in a fresh context, in a separate detached worktree. Verdict: nothing blocking. Findings:
1. **Medium — fixed.** On a dirty tree, if the last path `git ls-files` lists was a deleted tracked file, the copy loop's `[[ -f ]] && printf` returned 1 under pipefail. `enter` then failed with no message. It now uses an `if` and prints an error on failure. A new helper-test case reproduces this in a scratch repo; it fails against the old helper and passes against the new one.
2. **Medium-low — fixed.** If the test failed or was killed between `enter` and the caller's `trap cleanup EXIT`, the copy was left behind (up to 171 MB for the deadcode copy). The helper now installs its own EXIT trap, and INT/TERM traps that exit 130 (which runs whichever EXIT trap is current). A helper-test case checks that an exit with no caller trap removes the copy.
3. **Medium-low — fixed.** The enforcement regex only matched the exact shape of today's tests. The reviewer's seven false-negative shapes are now regression cases, and all are caught: single-quoted paths, direct redirect/`cp`/`mkdir` writes, arrays, other root variables, `readonly` assignments, and `e2e/`/`tests/` targets. The header now says the check is static and best-effort.
4. **Low — fixed.** Stale comments in `check-brand-assets_test.sh`, `guard-deadcode-baseline_test.sh` and `guard-price-history-sync_test.sh` described real-tree writes. The `.gitignore` entry for the old deadcode fixture stays, because existing clones may still hold debris from a killed run.
5. **Low — fixed.** A tracked file matching an ignore pattern was copied but not staged. The helper now uses `git add -A -f`. The copy holds only files `ls-files` listed, so ignored build output still stays out.
6. **Low — accepted.** The helper test hard-codes `internal/pages/init.go` and `internal/data/plugin_repo.go`; a rename would need a one-line update.

## Verified beyond automated tests
- **TDD:** before converting any test, the new guard was run on the real `scripts/ci`. It failed and named exactly the 12 tests; the sandboxed i18n tests passed.
- **The card's proof:** `M=$(mktemp); sleep 1; <all 12 converted tests + the 3 new ones + the i18n ones>; find . -path ./.git -prune -o -newer $M -print` prints nothing, and `git status` shows no new files. The reviewer ran the same proof independently.
- **The race itself:** all 107 `bash scripts/` steps of the CI build job were run while a full `go test ./...` ran in parallel. Nothing was written to the tree.
- **Sandbox completeness:** the reviewer compared every guard's reads with its `enter` arguments, and ran the weakly-asserting tests (page-http-error, kiosk-engine) by hand. No case passes vacuously: guard output names the planted fixture, not a missing file.
- **shellcheck 0.9.0** (CI's pin) reports 0 issues on `scripts/ci/*.sh`, and `guard-pipefail-grep-q.sh` passes. It caught two `| grep -q` pipes in the first draft of the helper test.
- **Not verifiable in this container:**
  - `guard-deadcode-baseline_test.sh` fails identically on unmodified `main` here. There are no GTK headers, so `internal/logging/file.go`'s `Stderr`/`timestampWriter.Write` (used only by the desktop build) show as new dead code. The sandboxed and real-tree runs report the same two lines. CI, which has the headers, decides.
  - `go test ./internal/plugins` hits the 10-minute default timeout here even alone. This diff has no Go change, and `main`'s CI is green.

## Safe to merge
Yes, once CI is green on the rebased head.
