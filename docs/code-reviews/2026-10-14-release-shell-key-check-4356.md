# Review: release key check is for unitill-pos, not the desktop shells (ut-docs#4356)

**Date:** 2026-10-14 · **Lane:** lane:cloud-54 · **Reviewer:** Fable (independent; author Opus 5.5)

## What shipped
- `release.yml` `verify-versions`: `check()` takes an optional third argument,
  `key`. Only the three `unitill-pos` checks pass it. The Linux and Windows
  `unitill-desktop` checks still test the version.
- `release.yml` `.dmg` check: its key test now applies only to `unitill-pos`.
- New `scripts/ci/release-marketplace-key_test.sh`, wired into the `ci.yml`
  build job. It pins the call sites and the `.dmg` gate. It runs the real
  `check()`, extracted from release.yml, on fixtures (a shell with no key
  passes; a server with no key fails; a server with the key passes). It also
  pins the premise: on linux/windows/darwin, the shell links no
  `internal/app` or `internal/pages`, and `cmd/unitill-desktop` never names
  the key.
- `web/release-notes/*/v0.34.3.md`: new entry for "Renew or change plan"
  (#1926, merged after the notes), in all 5 locales.

## Root cause (verified)
`cmd/unitill-desktop` starts the sibling `unitill-pos` and hosts the WebView.
It links the packages that read the key (`internal/config`, `plugins`,
`enroll`) only as dead code. Under `-trimpath`, the linker drops an
unreferenced `-X` string. Probe binary: 0 hits for the key, while
`unitill-pos` has 1. The macOS `.app` shell "passed" only because
`packaging/macos/build-app.sh` builds without `-trimpath`: Go's build info
then records the `-ldflags` text, and the grep matched that.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The premise check only grepped `cmd/unitill-desktop` for the identifier. A shell that started running server code (e.g. `app.Run`) in-process would not have tripped it. | Fixed: a `go list -tags desktop -deps` assertion for `internal/app`/`internal/pages` on 3 OSes. The regex was confirmed to match on `unitill-pos`'s deps. |
| 2 | nit | The comment blamed DWARF for the macOS false pass; the real cause is no `-trimpath` (build-info blob). | Fixed |
| 3 | nit | ADR wording claimed iOS verification | Fixed in the ut-docs PR |
| 4 | nit | Review records missing | This file + ut-docs `code-reviews/2026-10-14-adr-0158-shell-key-check-4356.md` |

The reviewer confirmed the security premise: no in-process server or plugin
verification in the shell on any OS, so ADR-0006/0158 are not weakened. They
also checked the bash under `set -euo pipefail` and found no false-pass path
in the self-test.

## Verified beyond unit tests
- TDD: with release.yml reverted, the new test fails 5 checks, including
  "a shell with the version and no key fails (fail=1)", which is v0.34.3's
  exact failure. With the fix, all checks pass.
- Every `bash scripts/ci/*` step in ci.yml was run locally. Two failures
  are environment-only and identical on `main`: `guard-deadcode-baseline`
  (local deadcode built with go1.26) and `guard-shellcheck-version` (pip
  shellcheck 0.11). `shellcheck` is clean on the new script. YAML parses.
  `guard-release-notes.sh v0.34.3` passes, and so do the
  releasenotes/updates Go tests.
- Not reproducible here: the real CGO shell build (no GTK/WebKit headers).
  The release re-dispatch is the end-to-end check.

## Verdict
Safe to merge.
