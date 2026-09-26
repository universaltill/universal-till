# Review — whole-page reload audit + reload reasons (ut-docs#2788)

**Date:** 2026-09-26 · **Card:** universaltill/ut-docs#2788 (p1, complexity:medium)
**Author:** Opus 5.5 (Dev subagent) · **Reviewer:** Fable (independent, different model)
**Branch:** `fix/2788-reload-audit`

## What shipped

The tablet main till (v0.23.0, after #2763's receipt-timer fix) still
occasionally refreshed the whole page with no operator action. Cloud-lane
scope: audit every reload path, remove what is not needed, and make every
remaining reload name its cause so the next device capture is conclusive.

- **Most likely culprit fixed — Activity recreation.** `MainActivity`'s
  `configChanges` lacked `keyboard|navigation|uiMode`. A Bluetooth HID
  scanner/keyboard sleeping and reconnecting (or auto night mode / docking)
  destroyed and recreated the Activity, which loads `/` in a fresh WebView —
  a full refresh from a background event. Now handled in-process. New CI
  guard `scripts/ci/guard-android-config-changes.sh` (+ `_test.sh`) pins all
  eight values and refuses `uiMode` while the app ships `-night` resources.
- **Android logging:** every `webView.reload()` / `loadUrl()`, Activity
  recreate/destroy, locale-driven recreate and in-process config change logs
  `reload: …` under tag `MainActivity`.
- **`POST /api/diag/reload-reason`** (`internal/pages/reload_reason_api.go`):
  session tier (like input-heartbeat), 2 KiB body cap, reason charset
  `[a-z0-9:_./ -]` ≤120, path `/…` ≤200 with query stripped, nav_type
  whitelist, `%q` logging, 30 lines/min cap; 400 with the `{data,error}`
  envelope on bad input; demo-mode denied.
- **Client:** `UT.reload(reason)` / `UT.noteNav(reason)` in base.html's shell
  script stash a reason in sessionStorage; an `htmx:beforeOnLoad` hook names
  HX-Refresh/HX-Redirect responses (`:poll` suffix for `every` triggers); the
  next document load reports it, or `unattributed` for a reload nobody
  announced. Every base-rendered `location.reload()` site converted;
  shell-signature fallback and idle auto-lock annotated.
- **Auth:** sessionless htmx request → one `auth: htmx request … -> HX-Redirect`
  log line (path only).
- `android/README.md`: "Diagnosing a page that refreshes by itself".

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Native theme is `Theme.AppCompat.DayNight`; its night variant lives in the AAR, so "no -night resources" was not the whole story — after a day/night flip, native chrome inflated at onCreate keeps old colours. | Fixed (documented): manifest comment + README state the cosmetic limit and why a stable page wins. Theme left unchanged (a behaviour change beyond scope). |
| 2 | should-fix | README claimed `unattributed` covers native reloads; an Activity recreate / TillService restart loads a fresh WebView (`navigate`, empty sessionStorage) and leaves no till-log line. | Fixed: README says those need adb logcat. |
| 3 | nit | An HX-Redirect to `/login` stashed a reason nobody consumes; signing in within 30 s mislabelled the next load. | Fixed: no stash when the redirect target is `/login` (the server line already names it). |
| 4 | nit | The "no bare reload" test matched only `location.reload()`. | Fixed: regex also catches `location.reload(…)`, `history.go(0)`, `location.href = location.href`, `location.assign(location.href)`; verified it fails on an injected `history.go(0)`. Self-order kiosk skip documented in README. |
| 5 | nit | Guard scans only `src/main/res`. | Accepted (no other source set has resources). |
| 6 | nit | Auth HX-Redirect log line uncapped. | Accepted: bounded (page navigates away after the first), `%q`-safe. |
| 7 | nit | Demo mode denies the endpoint. | Accepted, consistent with input-heartbeat. |

## Verified

- TDD re-verified by the reviewer in a separate worktree: removing the
  manifest values fails the guard; unregistering the handler fails
  `TestReloadReason_RouteRegistered` (404 ≠ 204).
- `go build`, `go vet`, full `go test ./...`, `golangci-lint` 0 issues,
  `shellcheck` clean on new scripts, every build-job guard incl. i18n,
  data-access, all android guards, docs-shots (surface hash refreshed —
  no pixel change).
- Real browser (Playwright, default project): new
  `e2e/tests/reload-reason-2788.spec.ts` — an ordinary load and a boosted
  navigation report nothing; F5 reports `unattributed` (204); `UT.reload`
  reports its sanitised reason once and the stash is consumed. Full default
  e2e suite: 657 passed.
- Not verified: Kotlin/Gradle compile (no Android SDK here — `android-ci.yml`
  builds it on the PR) and an on-device capture (local lane, adb logcat).
- Visual: no rendered surface changed (docs-shots hash only); nothing to
  screenshot.

## Verdict

Safe to merge once CI (including `android-ci`) is green. Device
confirmation on the tablet stays with the local lane.
