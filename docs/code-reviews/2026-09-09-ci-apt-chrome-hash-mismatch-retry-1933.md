# CI resilience: retry apt-get/playwright-install against Google's Chrome apt-repo hash mismatch (ut-docs#1933)

## What shipped

`.github/workflows/ci.yml`'s `desktop-shell` and `e2e` jobs were both
failing on `main` (confirmed live: run `34382354687`, SHA `d8081d02`, both
jobs failed at the `apt-get update` / `playwright install --with-deps`
step with `E: ... Hash Sum mismatch` against
`dl.google.com/linux/chrome-stable`) — a runner-image-level, external
issue, not caused by any code change (identical failure on the pre-merge
SHA `d288dd83` too).

- New `scripts/ci/retry-with-backoff.sh`: a generic retry-with-exponential-
  backoff wrapper (`<max_attempts> <initial_delay> [--clear-apt-lists] --
  <command...>`). The optional `--clear-apt-lists` flag runs an apt-index-
  clearing hook between failed attempts (overridable via
  `RETRY_WITH_BACKOFF_APT_CLEAR_CMD` so the regression test never touches a
  real apt cache or needs root) — combining the issue's two suggested
  workarounds (retry + fresh index) rather than picking one.
- New `scripts/ci/retry-with-backoff_test.sh`: hermetic regression test
  (fake flaky commands via a counter file, no real network/apt/sudo).
- `ci.yml`: wraps `desktop-shell`'s `sudo apt-get update` and `e2e`'s
  `npx playwright install --with-deps chromium` with the new script
  (3 attempts, 5s initial delay, `--clear-apt-lists`).
- `ci.yml`'s `build` job: added a step running
  `retry-with-backoff_test.sh`, same "script isn't itself part of this
  build job, but its test is hermetic so runs here anyway" pattern already
  used for `prune-stale-worktrees_test.sh` just above it.

Neither job is skipped/quarantined — both still run for real, matching the
issue's acceptance criteria.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (per this card's
`complexity:easy` routing). Findings:

1. **Real, fixed before this commit**: the new regression test wasn't
   wired into `ci.yml` at all — a future regression in the retry logic
   would get zero CI signal. Fixed by adding the `build`-job step above.

Everything else checked out clean, independently verified (not taken on
trust):
- TDD claim re-verified: moved the script aside, reran the test → 7 real
  assertion failures (not vacuous); restored it → all 6 pass again.
- `set -e` + `if "$@"; then` interaction is correct (a failing command as
  an `if` condition doesn't trigger `set -e`'s abort) — demonstrated
  directly.
- `../../scripts/ci/retry-with-backoff.sh`'s relative path from the `e2e`
  job's `working-directory: tests/e2e` resolves correctly to the repo-root
  script (`realpath`-verified).
- `"$@"` quoting round-trips correctly for multi-word/spaced arguments.
- Attempt counting has no off-by-one (exactly `max_attempts` tries in both
  the always-fails and eventually-succeeds cases).
- The apt-clear hook fires only *between* failed attempts, never before
  the first or after a final failure/success.
- `desktop-shell`'s second line (`apt-get install -y libgtk-3-dev ...`)
  deliberately left unwrapped: the pasted failure log is specifically at
  `apt-get update`'s index fetch, not `install` — once `update` succeeds
  the local index is consistent.
- `eval "$RETRY_WITH_BACKOFF_APT_CLEAR_CMD"` is not an injection risk here:
  the var is only ever set by the fixed CI-config string or the test's own
  hermetic override, never by untrusted input.
- `shellcheck` (0.9.0): zero findings on either new script.
- YAML re-parses cleanly (`python3 -c "import yaml; yaml.safe_load(...)"`).
- No Go/HTML/JSON touched — no i18n/data-access/compliance-claim guard
  concerns apply; `guard-e2e-fixtures-import.sh` re-run anyway, passes.

## Verified beyond automated tests

- `gofmt -l .` clean (no Go files touched).
- `go build ./...` succeeds (unaffected, sanity-checked anyway).
- `bash scripts/ci/retry-with-backoff_test.sh`, `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-e2e-fixtures-import.sh` all pass.
- YAML validity of `ci.yml` confirmed via `python3`/`pyyaml`.
- Confirmed the underlying failure live before writing the fix: pulled
  `universal-till`'s actual `ci` workflow runs via the GitHub API and read
  the failing job/step logs directly (run `34382354687`) rather than
  taking the issue text on faith.

## Safe-to-merge verdict

Yes, after the missing CI-wiring fix above.

## Explicitly deferred / not in scope

- `.github/workflows/e2e.yml` (a separate top-level workflow, not `ci.yml`)
  has the same class of exposure for its own Playwright install step —
  tracked separately as ut-docs#1932, which additionally proposes reusing
  `e2e/scripts/resolve-chromium.sh`'s pre-installed-Chromium fallback
  rather than a retry loop. Not touched here; out of this card's scope.
- Whether Google's apt-repo outage has actually resolved could not be
  independently reproduced (transient upstream issue) — this fix is
  verified by design/logic and hermetic tests, not by reproducing the live
  outage; DevOps will confirm the real jobs go green on the next push.
