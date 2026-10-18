# Code review — weekly docs-shots locale-drift report (ut-docs#4582)

**Date:** 2026-10-17 · **Lane:** lane:cloud-41 · **Card:** universaltill/ut-docs#4582

## What shipped

`guard-docs-shots.sh` deliberately leaves `web/locales/**` out of its surface
hash, so a translation-only change can leave a manual screenshot stale with no
guard tripping. This adds the non-blocking weekly catch-up the card asked for:

- `.github/workflows/docs-shots-locale-drift.yml`: Monday 05:30 UTC cron plus
  `workflow_dispatch`, with no push or PR trigger. Permissions are
  `contents: read` and `issues: write`, and `persist-credentials: false`.
  It runs `make docs-shots` on `main` with the same fonts and toolchain as
  `docs-shots-determinism.yml`. It never commits or pushes; a bot PR waits on
  the bot-identity decision in ut-docs#3738.
- `scripts/ci/docs-shots-locale-drift.sh`:
  - `collect` lists `changed|new|deleted <locale>/<id>.png` against HEAD,
    copies the regenerated files for the artifact, and writes the issue body.
  - `sync-issue` updates the one open `docs-shots-drift` issue, or opens one
    (creating the label if it doesn't exist). When there's no drift it closes
    an open issue; with no drift and no issue it does nothing.
- `scripts/ci/docs-shots-locale-drift_test.sh` (in the `ci.yml` build job)
  runs in a sandbox copy:
  - collect: a clean tree, a changed + new + deleted screenshot, the run
    link, and a failing git status;
  - sync-issue: all four cases, against a stub `gh` with exact flags;
  - the workflow's triggers, permissions and step wiring.
- Docs: `CONTRIBUTING.md`, `CLAUDE.md` (workflow list), and the accepted-gap
  comment in `guard-docs-shots.sh`.

## Review

The review was independent, on Sonnet. Fable was the mapped reviewer for a
medium card, but its call was refused (out of usage credits), so Sonnet was
used per MODEL-ROUTING. The reviewer ran the test, shellcheck (no new issues
vs. HEAD~1), the sandbox and pipefail guards, a YAML parse, and six mutations.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `git status` read via process substitution: a git failure became `count=0`, and `sync-issue` would then close a real issue | **Fixed**: porcelain goes to a temp file first, so `set -e` stops the step; new test (`GIT_DIR=/nonexistent`) |
| 2 | should-fix | Stub `gh` accepted any flags; 4 of 6 mutations (`--force`, `--state open`, `--jq`, `--label`) survived; wiring checks were substring-only | **Fixed**: exact `gh issue list …` line, `--force`, `--body-file`, `--comment` asserted; workflow checks for `GITHUB_OUTPUT`, the `count` condition, `GH_TOKEN`/`GH_REPO`, `persist-credentials: false`; the commit/push grep skips comments. All four mutations now fail the test |
| 3 | should-fix (low) | A failed artifact upload would skip the issue step | **Fixed**: `continue-on-error: true` on the upload, pinned by the test |
| 4 | note | A failure in `make docs-shots` or apt turns the job red | Accepted: only the scheduled run is red; main CI and PRs are unaffected, and the issue is never closed on that path |
| 5 | question | Auto-close goes beyond "no difference → do nothing" | Kept: a stale open issue is itself noise. Now documented in CONTRIBUTING; finding 1's fix removes the wrongful-close path |
| 6–8 | nit | `gh` usage, porcelain parsing, workflow parity | No change needed |

## Verified beyond the automated tests

- **Real run, no drift:** `e2e/scripts/docs-shots.sh` on current `main`
  (cloud container, reference fonts) regenerated all 120 PNGs
  byte-identical, and `collect` printed `count=0`. A clean `main` makes no
  noise.
- **Real run, locale-only drift:** changed only `fa.json`'s `nav.catalog`,
  then regenerated:
  - `collect` reported `count=3` (`changed fa/catalog.png`,
    `fa/option-sets.png`, `fa/tax-codes.png`) and copied the three files;
  - `manifest.json` was unchanged, which confirms the PR guard is blind to
    this case;
  - the probe was reverted afterwards.
- Mutation checks (author and reviewer), as listed above.
- Not run: the workflow itself on GitHub. It is schedule/dispatch-only, so
  the first real run is the Monday cron or a manual dispatch after merge.

## Verdict

Safe to merge: non-blocking, least privilege, and no write access to code.
