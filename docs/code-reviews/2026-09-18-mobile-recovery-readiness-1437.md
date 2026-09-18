# Code review — Android shell lands on the recovery page instead of a white screen: `mobile.Start` accepts recovery mode as "listening" (ut-docs#1437)

- **Date**: 2026-09-18
- **Card**: ut-docs#1437 (p1, `complexity:medium`, `lane:local`; child of the boot-recovery epic ut-docs#1415)
- **Branch / PR**: `fix/1437-mobile-recovery-ready`
- **Author model**: Sonnet (Dev subagent); **Reviewer model**: Opus, fresh context, own worktree

## What was wrong

`mobile/mobile.go` `waitUntilReady` (the gomobile `Start` path the Android `TillService` calls) only accepted `/healthz` 200. `internal/recovery/page.go`'s `healthHandler` deliberately answers 503 for the whole time boot-failure recovery mode (ADR-0075) is serving, so shells never treat a recovering till as healthy. Net effect on a device: `Start` timed out after 30 s ("mobile: server did not become ready within 30s"), `TillService` reported an error, `MainActivity` never navigated the WebView — a white page with the only explanation in the status bar, while the server's recovery page (ref code, Retry, safe mode) was up on the same port. Seen on the TECLAST tablet on 2026-09-18 during ut-docs#2395.

Rescope against current code: the card's other half — "never engage kiosk lock before healthy" — was already satisfied by ut-docs#1508 (Lock Task engages only on `/self-order` page loads; cold launch is unpinned). The desktop shell waits on TCP accept, so a Pi already lands on the recovery page. Only the mobile readiness poll was wrong.

## What shipped

- `internal/recovery/page.go`: exported `HeaderMode = "X-UT-Mode"` / `ModeRecovery = "recovery"`; `healthHandler` sets the header and keeps the 503, so every shell's healthy-vs-unhealthy logic is unchanged.
- `mobile/mobile.go` `waitUntilReady`: ready on 200, or on 503 carrying that header; anything else keeps polling until timeout. Doc comments on `waitUntilReady` and `Start` say plainly that "ready" ≠ "healthy" and that the returned address may be the recovery screen.
- No Kotlin change: `MainActivity`'s listener loads `http://<address>` on success, the recovery page is not `/self-order` so nothing pins, Home/Back keep working. The recovery page's own JS polls `GET /` and reloads once Retry succeeds, and `app.Run`'s loop rebinds the real server on the same port, so the WebView ends up on the till.

## Independent review — findings

No blockers, no should-fixes.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `Start`'s own doc comment didn't say the address may be the recovery screen | **Fixed** (comment) |
| 2 | nit | `TillService`'s persistent notification reads "running" while on the recovery screen | Accepted — cosmetic, Kotlin out of scope here; noted on the card |
| 3 | follow-up (pre-existing) | `cmd/unitill-desktop/desktop.go` `tillAlreadyRunning` accepts only 200, so a desktop shell relaunched while the till is in recovery mode spawns a second `unitill-pos`, which dies on `ErrDataDirLocked` | Filed as a Backlog card (see close-out) |

Reviewer reasoned through and cleared: Retry → real till on the same port (`recovery.Serve` shuts its listener before returning `Retry`); `Stop()` during recovery unblocks via ctx (E2E test does it in 0.18 s); second `Start(sameDir)` during recovery returns the same address (one `app.Run` lifecycle); loopback-only so a foreign 503 can't carry the header; tests clean up via `t.Cleanup(Stop)`, no leaked listeners under `-count=5 -race`; `web/help/*/recovery.md` already promised this behaviour.

## Verified

- Red→green by revert (Dev, Tester and Reviewer each independently): with `origin/main`'s `mobile.go`, `TestWaitUntilReady_RecoveryModeWithHeaderIsReady` fails in 2 s and `TestStart_BootFailureServesRecoveryModeInsteadOfTimingOut` fails after the full 30 s with the device's exact message; with the header removed from `page.go`, the recovery test and the mobile E2E test both fail — the E2E test exercises the real header, not just the status.
- `TestStart_BootFailureServesRecoveryModeInsteadOfTimingOut` corrupts the version-1 ledger checksum in a real data dir and drives the real `Start`: address returned in 0.17 s, `/healthz` 503 + header, `/` is the recovery page (`id="retry-btn"`), `Stop()` clean.
- `go test ./... -count=1` green (Dev, ports 8091–8093 free); `golangci-lint` 0 issues; `guard-data-access.sh` passes; reviewer added `-count=5 -race` on the new tests.
- Not verified: on the tablet itself — needs a release carrying this; recorded on the card as the remaining acceptance item.

## Verdict

**Safe to merge.**
