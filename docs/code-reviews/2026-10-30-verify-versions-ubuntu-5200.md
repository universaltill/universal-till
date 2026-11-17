# Review: verify-versions off macOS so a starved runner can't stop a release (ut-docs#5200)

Date: 2026-10-30 · Branch: `ci/5200-verify-versions-ubuntu` · Built by
Sonnet 5.5, reviewed by Opus 5.5 (fresh context, own worktree,
mutation-tested).

## What happened

In release run 40882053145 GitHub's hosted macOS arm64 pool never gave
`verify-versions` (`macos-14`) a runner ("The job was not acquired by Runner
of type hosted even after multiple attempts"). `publish-release` needs it, so
it was skipped, `cleanup-orphaned-tag` deleted the tag, and the release had to
be dispatched again. `release-runner-retry.sh` (#5175) deliberately does not
cover this, because the tag is already gone by then.

## What shipped

- `.github/workflows/release.yml`: `verify-versions` runs on `ubuntu-latest`.
  It only ever used gh, tar, unzip, `strings -a`, grep/sed, curl and
  `go version -m`. The macOS runner was left over from when it mounted the
  `.dmg`, and those checks moved to `macos-app` / `macos-dmg-attach` in
  ut-docs#2917. Two step comments that described the macOS runner now say it
  is history.
- `scripts/ci/release-macos-nonblocking_test.sh` (already in ci.yml's
  `build` job) has a new check. It walks the transitive `needs:` closure of
  `publish-release` and fails if any job in it runs on a macOS runner:
  - It reads the `runs-on:` value with trailing comments stripped.
  - For `${{ matrix.<key> }}` it also checks that key's values in the
    matrix.
  - It fails on a multi-line `runs-on:` rather than passing blind.
  - It asserts the closure contains `verify-versions`, `checksums`,
    `goreleaser` and `prepare`, so a broken parser can't pass vacuously.
- `scripts/ci/release-runner-retry_test.sh` case (d2): the comment no longer
  says verify-versions is on `macos-14`. The case itself is unchanged.
- `docs/arch/desktop-app.md`: the release-retry paragraph now says every
  publish-gating job runs on Linux, and names the test that pins it.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A matrix job using a key other than `runner:` (e.g. `matrix.os`) passed the check vacuously. | **Fixed.** The key now comes from the `runs-on:` line. A probe with `- os: macos-14` now fails. |
| 2 | minor | A multi-line `runs-on:` (a list, or `labels:`) passed vacuously. | **Fixed.** The check now fails with a "keep it on one line" message. Probe confirmed. |
| 3 | minor | `needs_of` (existing helper) misreads `needs: [a] # comment` and YAML block lists. | **Accepted.** These shapes fail loudly ("no runs-on: line") rather than passing, and none exist in release.yml today. |
| 4 | minor | There was no review record yet. | **Fixed.** This file. |
| 5 | nit | `check()`'s `grep -qx "dev"` never fires: "dev" is 3 characters, and both GNU and cctools `strings` drop runs shorter than 4. This predates the change. | **Deferred** to ut-docs#5205. The version check still fails an unstamped build. |
| 6 | nit | `linux-shells` on `ubuntu-22.04-arm` (a hosted arm pool) still gates publishing through goreleaser. | **Deferred** to ut-docs#5205. Not macOS, so outside this card. |

## Verified beyond the unit tests

- The reviewer cross-built `unitill-pos` with goreleaser's flags
  (`CGO_ENABLED=0 -trimpath -ldflags "-s -w -X …Version=9.8.7 …"`) for
  linux/amd64 (ELF), windows/amd64 (PE32+) and darwin/arm64 (Mach-O).
  Running the job's real `check()` with GNU strings 2.42:
  - `VERSION=9.8.7` passed on all three binaries, marketplace key included.
  - `9.8` and `9.8.70` failed on all three, so there is no prefix or
    substring false pass.
- The desktop shells need cgo, so they were not built. They use the same
  linker `-X` path.
- TDD, re-verified by the reviewer in a separate worktree: with
  `runs-on: macos-14` restored, the test fails with "verify-versions gates
  publish-release but runs on macOS". After the restore it passes.
- No hidden gate: `checksums`' and `publish-release`'s `if:` lines only read
  `needs.X` for jobs already in their `needs:` (GitHub allows nothing else).
- Gates: `release-macos-nonblocking_test.sh`, `release-runner-retry_test.sh`
  (12/12), `release-marketplace-key_test.sh` and
  `release-job-timeouts_test.sh` all pass. Shellcheck 0.11 on the changed
  scripts reports 0 issues. `release.yml` parses as YAML.

## Verdict

Safe to merge. This removes the macOS pool from the publish path. The next
real release run is the live check: `verify-versions` should start on an
ubuntu runner.
