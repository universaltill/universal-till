# 2026-09-06 — Bluetooth panel: stop blaming Android's hardware for a platform gap

- **Card**: universaltill/ut-docs#1643 (`source:user`, `complexity:easy`)
- **PR**: universaltill/universal-till#836 (`fix/1643-android-bluetooth-platform-unsupported`)
- **Author model**: Sonnet (inline, `complexity:easy`)
- **Reviewer model**: Sonnet, fresh-context subagent, `isolation: "worktree"` (per the model-routing rubric: easy → "different model" relaxes to "different instance")

## What shipped

An end-user bug report (via the private `bug-reports` intake) said the Bluetooth
device-search page on an Android till doesn't work and the message shown is
wrong. Both reproduced. `internal/bluetooth` drives BlueZ exclusively over the
D-Bus system bus; Android has no D-Bus system bus and no `bluetoothd` at all,
so `NewDBusClient()` fell through to `ErrUnavailable` — the identical message
a genuinely-broken Linux till shows ("no Bluetooth adapter was found, or the
Bluetooth service is not running"). On the reporting device (TECLAST P50T)
the radio was present and healthy (`dumpsys bluetooth_manager` confirmed
`BLE_ON`), so the message actively misdirected the operator toward a
hardware/settings fix that could never help.

Fix, direction (1) from the card (ship now; an actual Android Bluetooth
bridge is direction (2), deliberately out of scope, no demand signal yet):

- New sentinel `bluetooth.ErrUnsupportedPlatform`, distinct from
  `ErrUnavailable`/`ErrAccessDenied`.
- `internal/bluetooth/dbus.go`: `NewDBusClient()` now delegates to
  `newDBusClientFor(runtime.GOOS)`, which returns `ErrUnsupportedPlatform`
  when `goos == "android"`, before ever calling `dbus.ConnectSystemBus()`.
  Split out for testability without faking `runtime.GOOS` — same shape as
  `internal/selfupdate`'s `supportedFor`/`Supported`.
- `internal/pages/bluetooth_devices_page.go`: the GET page handler and
  `apiFail` both carry a third, distinct state (`unsupported` /
  `bluetooth_unsupported_platform`, HTTP 503), never conflated with the
  existing `unavailable`/`accessDenied` cases.
- `web/ui/pages/bluetooth_devices.html`: a third notice branch
  (`data-testid="bluetooth-unsupported"`), scan button disabled in this
  state too, inline-JS `T`/`messageFor` kept in step with the Go-side error
  code (CLAUDE.md: inline-script status text is user-facing too).
- New locale key `bluetoothdevices.platform_unsupported` in core's
  `en/fa/tr/ar`, plus the two external language packs — landed ahead of this
  PR per the `lang-pack-drift` convention (`reference/coding-standards.md`,
  ut-docs#1576): universaltill/ut-plugin-language-de#164,
  universaltill/ut-plugin-language-es#163.
- `web/help/{en,fa,tr,ar}/bluetooth-devices.md` gained a bullet describing
  the Android case; screenshots regenerated (`make docs-shots`, 100/100
  Playwright specs).

## TDD

`internal/bluetooth/dbus_test.go` (new file) was written first:
`TestNewDBusClientFor_AndroidIsUnsupportedPlatform` and
`TestNewDBusClientFor_LinuxAttemptsRealConnection`. Confirmed failing before
the implementation (`undefined: newDBusClientFor` / `ErrUnsupportedPlatform`),
then implemented, then green. The reviewer independently re-verified this by
reverting the fix hunks in `dbus.go`/`client.go` (leaving the new test in
place) and re-running the platform-gate tests — confirmed the same real
build failure — then restored and re-confirmed both tests pass.

## Independent review — findings

**None.** No blocker, should-fix, or nit. Full pass summary (see the PR
thread / this cycle's transcript for the complete verification log):

- Platform gate is an exact `goos == "android"` string match, checked
  *before* any D-Bus call — cannot swallow a genuine Linux case
  (`TestNewDBusClientFor_LinuxAttemptsRealConnection` guards this directly).
  `ErrUnsupportedPlatform` returned unwrapped; every consumer uses
  `errors.Is`, consistent with the existing `ErrUnavailable`/`ErrAccessDenied`
  checks in the same switches.
- Page/API wiring: the new `case` is correctly ordered before `default` in
  both the page handler's switch and `apiFail`; `"unsupported"` is actually
  passed into the template data map (not just declared).
- Template: the three notice states are mutually exclusive by construction
  (the Go handler sets exactly one bool); the scan button's disabled
  condition was updated to include the new state; the inline `T`/
  `messageFor` pair matches the Go-side error-code string.
- Tests are non-tautological: the page test asserts the new notice appears
  **and** the old "unavailable" one does not; the API test asserts the
  specific new `error.code`, not just the status code.
- Full local run: `go build ./...`, `go vet ./...`, `gofmt -l .` (clean),
  `go test ./...` (whole repo, all green), `golangci-lint run ./...`
  (0 issues), and every CI-blocking guard in `ci.yml`'s `build` job —
  including a real `make docs-shots` run (100/100 specs) for
  `guard-docs-shots.sh`.
- No SQL introduced (repository-pattern rule N/A here); no file-write path
  added (`os.MkdirAll`/`paths.Data` concern N/A); new strings are plain
  factual capability statements with no certification/legal-outcome
  language (`guard-compliance-claims.sh` clean); all four locale files
  valid JSON with non-empty values for the new key; the two new help-doc
  bullets (existing "no Bluetooth" case vs. new Android case) don't
  overlap or contradict each other.

## Verified beyond automated tests

- `go test ./...` for the whole repo (not just the touched packages).
- Live CI on the PR: `commit-attribution` and `lang-pack-drift` green;
  `ci`/`UI E2E` were run and confirmed green before merge (see PR #836's
  check-run history).
- **Not verified**: acceptance criterion 5, the real-device confirmation on
  the TECLAST P50T (tap the panel, see the new notice, confirm the scan
  button is disabled) — no Android SDK/adb/physical device in this session.
  Tracked as the sole open item on ut-docs#1643, which this PR deliberately
  does not close.

## Safe-to-merge verdict

**Yes.** Merged via `merge_method: "merge"` (never squash/rebase — see the
`reviewer` skill's note on commit re-attribution) once CI was green.

## Explicitly deferred

- AC5 (real TECLAST P50T confirmation) — needs a local/interactive session
  with the physical device; ut-docs#1643 stays open, `status:in-review` +
  `blocked:env`, for it.
- Direction (2) from the card (an actual Android Bluetooth bridge, native
  D-Bus-equivalent) — separate scope, no demand signal yet.
- A follow-up backlog card: `ci.yml` never compiles the Android Gradle
  project (only `release.yml` does) — a different card (this cycle also
  found and filed this independently on ut-docs#1639's close-out,
  ut-docs#1658); not duplicated here.
