# Review: iOS/iPadOS signed build → TestFlight (internal) from GitHub Actions (ut-docs#3069)

**Date:** 2026-09-28 · **Lane:** lane:local · **Built by:** Claude Opus 5.5 (subagent) · **Reviewed by:** Claude Fable 5.1 (independent subagent)

## What shipped
- `ios/scripts/make-app-icon.sh`: 1024×1024 **opaque** App Store icon generated at build time from the hash-pinned canonical logo (white tile, mark at 70% height), same idea as `packaging/macos/build-app.sh`. Git-ignored output; `project.yml` uses it. ios-ci runs it (plus `scripts/ci/ios-app-icon_test.sh`) before xcodegen.
- `project.yml`: `ITSAppUsesNonExemptEncryption: false`; Info.plist versions come from `$(MARKETING_VERSION)` / `$(CURRENT_PROJECT_VERSION)` (XcodeGen wrote literal 1.0/1, so every upload after the first would have been a duplicate. Found by the builder).
- `.github/workflows/ios-testflight.yml`: `workflow_dispatch` only, repo-guarded, macos-15 hosted, `contents: read`, SHA-pinned actions, **unsigned archive**, then `-exportArchive` cloud-signs (method `app-store-connect`, destination `upload`) with the API key; key under `$RUNNER_TEMP` (umask 077, 600), removed in `always()`. Version = dispatch input / latest `v*` tag (validated); build = `run_number.run_attempt`.
- `release.yml` publish-release: `actions: write` + dispatches `ios-testflight.yml --ref $TAG -f version=…` (warning only on failure: never fails a published release).
- `scripts/ci/ios-testflight-workflow_test.sh` (in ci.yml) guards all of the above, self-tested against a broken fixture.

## Findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | High | `release: published` never fires: release.yml publishes with GITHUB_TOKEN | **Fixed**: release.yml dispatches the job; trigger removed; guard checks the dispatch |
| 2 | Medium | Signed archive on a fresh runner mints a new Apple Development cert every run (team limit) | **Fixed**: archive `CODE_SIGNING_ALLOWED=NO`, export cloud-signs; guard enforces. Fallback if Xcode refuses: archive with `CODE_SIGN_IDENTITY="Apple Distribution"` |
| 3 | Low | Actions not SHA-pinned in a job holding a signing key | **Fixed**: same SHAs as release.yml's pinned jobs; guard enforces |
| 4 | Low | Export compliance "exempt" (TLS, AES-GCM own data, HPKE PIN auth; public source) | Accepted for internal TestFlight; **BIS/NSA self-classification email before any public release** → v1.0 checklist (#3052) |

Reviewer confirmed clean: no fork/PR path to secrets, inputs regex-validated via env, no artifact upload, valid ExportOptions keys (Xcode 15+/16), icon RGB no-alpha, simulator path intact.

## Verified
- Guard + icon tests (both failed first); guard self-test trips every check; actionlint and shellcheck clean on the new files (release.yml's 2 shellcheck warnings pre-exist on main).
- Icon looked at: black U mark centred on white, 1024×1024, `hasAlpha: no`.
- **Not verifiable locally (no Xcode on this Mac):** compile → the PR's ios-ci run; signing/upload/role (App Manager vs Admin) → the first `workflow_dispatch` after merge.

**Verdict:** safe to merge; the first dispatch is the acceptance test.
