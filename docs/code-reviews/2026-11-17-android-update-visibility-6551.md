# Review: Android update visibility + install-permission pre-flight (ut-docs#6551, Phase 1)

Pre-flight: help topic `web/help/en/updates.md` updated; `en.json` + `en.meta.json` updated (English only, ADR-0160); README n/a (no capability claim changed); `make docs-shots` n/a (see PR); Phase 2 deferred → owner decision on #6551.

## What shipped
- Sale-only viewer: non-interactive "Update available vX, ask a manager" status-bar pill (no action, no 403 path, #3079).
- Manager: update banner at the top of Settings; status-bar chip opens the Software update section.
- Shell: `canRequestPackageInstalls()` pre-flight before the download; `needs_permission` makes the page offer Android's "Install unknown apps" screen for this app only; never continues the install by itself. `verifyThenInstall` (ADR-0179) untouched.

## Findings (Opus 5.5 review, Sonnet tester)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | `canRequestPackageInstalls()` is API 26, minSdk 24: crash on Android 7 at launch/update | fixed: sdkInt-aware pure logic, only evaluated while awaiting; unit tests for API 24/25 |
| 2 | Medium | Settings "check for updates" button stuck on "Downloading" after needs_permission | fixed + e2e |
| 3 | Med-low | Chip tap on another Settings section did nothing visible | fixed (hash set) + e2e |
| 4 | Low | Text/help named "Install update", button is "Download" | fixed |
| 5 | Low | needs_permission note persisted after "Not now"/grant outside app | fixed (clear + `canInstallPackages()` re-check) + e2e |
| T | Defect | Cashier pill 2.5:1 contrast in light theme | fixed: #451a03 on warning amber (~4.7:1) |
| 6 | Low | Permission screen drops the kiosk lock (same as existing installer path) | accepted |
| 7 | Nit | pill hidden at phone width on sale screen; Android lint (NewApi) not run offline | accepted / CI |

## Verification
go build/vet, httpx + pages tests, Kotlin `testDebugUnitTest` offline, Playwright spec 7/7, i18n/meta/help guards, gofmt. No device touched; not seen on a real tablet.

Verdict: approve after fixes. Phase 2 (nightly pre-download, silent install/Device Owner) not built.
