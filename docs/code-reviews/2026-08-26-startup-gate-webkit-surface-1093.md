# ut-docs#1093 — startup gate for the corrupt WebKitGTK surface

**Branch:** `fix/1093-startup-gate-webkit-surface` · **Date:** 2026-08-26

## What changed

`waitForSafeStartup()` (new `cmd/unitill-desktop/startup_gate_linux.go`) holds the
desktop shell until the machine is 60s into its boot, called immediately before
`webview.New`. No-op stub off `desktop && linux`. Tunable via
`UT_SHELL_MIN_UPTIME_SECONDS`; `0` disables.

## Why a mitigation and not a fix

WebKitGTK 2.52.6 on Wayland/vc4 builds a corrupt compositing surface when the
window is created early in a boot and never recovers. Five repairs were measured
on hardware and **all failed** — forcing a repaint, `gtk_window_resize` (a
literal no-op on a fullscreen window, proven by instrumenting the binary),
unfullscreen/refullscreen, `location.reload()`, hide/show to drop the Wayland
surface — plus `WEBKIT_DISABLE_DMABUF_RENDERER=1`. Chromium in the identical
early slot is always clean, which scopes the defect to WebKitGTK rather than the
compositor or driver. The real fix belongs upstream.

The mechanism is **still unknown**. An earlier "CPU load race" conclusion was
retracted: a gate that waited for genuine CPU idle cleared at uptime 10s with 98%
idle and still rendered corrupt.

## Evidence

Each boot's first frame, classified by RMSE against a known-good reference,
autologin, screen untouched:

| launch at | result |
|---|---|
| T+7s (as shipped) | CORRUPT |
| T+20s | CORRUPT |
| T+60s | **CLEAN 3/3** |

Final product build, refactored and tested, on real hardware: **CLEAN**.

## Self-review (superseded below)

The original self-review recorded here (subagents were disabled in that
session, so the standing independent-different-model-review rule was not
followed) claimed four findings fixed/covered. The independent review below
found that finding 1's fix inverted a repo convention (untagged code got no
CI coverage at all, worse than the build break it fixed) and finding 2's
claim was **factually wrong** — a units-confusion typo or a large-enough
value both defeated the "malformed env can't disable the gate" guarantee it
asserted. See "Independent review" and "Fixes applied" below for the
corrected picture; this section is kept for the record, not as the current
state.

1. Test file had no build constraint — fixed by adding
   `//go:build desktop && linux` to `startup_gate_test.go`. (This is the fix
   the independent review flagged as trading a build break for zero CI
   coverage — see F1 below.)
2. "Malformed env must not disable the gate... covered by a test." (Only
   the negative/garbage cases were tested — not the overflow case that
   actually breaks the guarantee. See F2 below.)
3. Unreadable `/proc/uptime` starts immediately. Still accurate, unchanged.
4. Gate placement precedes `webview.New`. Still accurate, unchanged.

## Independent review (Opus, isolated worktree, ut-docs#694 stale-PR sweep)

Ran independently of the implementing session — different model, fresh
context, no visibility into the self-review's reasoning. Full findings
posted to PR #564; summarized here.

**Gates it could actually run, real output:** `gofmt -l`, `go build ./...`,
`go vet ./...`, `go test ./cmd/unitill-desktop/...`, full untagged
`go test ./...`, `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
`guard-help-topics.sh`, `guard-i18n.sh` — all pass. Could **not** run
`-tags desktop` in this container (no GTK/WebKit dev headers, amd64 not the
Pi's arm64) — flagged as unverified-by-review rather than taken on faith.
The "pre-existing failure" claim was verified **statically**: traced
`shellAppliesWindowMode`'s `init()` under the `desktop && linux` tag against
the untagged assertion in `TestShellAppliesWindowModeGatesTheAdvertise`,
confirmed this PR touches neither file.

**Findings (severity, fixed vs. accepted):**

- **F1 — should-fix, fixed.** The new logic (`gateDuration`,
  `readUptimeFrom`) is pure/OS-independent and was put behind
  `desktop && linux` to resolve a build break, contradicting an explicit,
  four-times-repeated repo convention (`autostart.go`, `control.go`,
  `shell_poll.go`, `autostart_install_flag.go` all carry a comment to this
  effect) — and the only CI job with `-tags desktop` is a **build**, no
  vet, no test, so the new code and its tests ran under no automated gate
  at all. **Fix:** split into untagged `startup_gate.go` (constants,
  `gateDuration`, `readUptimeFrom`, and a new pure `holdFor` extracted from
  `waitForSafeStartup` for testability) + tagged `startup_gate_linux.go`
  (`waitForSafeStartup`, `procUptime` only). Test file untagged. All of it
  now runs under `go test ./...`, the same gate every PR's CI actually
  exercises.
- **F2 — should-fix, fixed.** `gateDuration()`'s env parsing had no upper
  bound. `UT_SHELL_MIN_UPTIME_SECONDS=60000` (a plausible seconds/
  milliseconds mixup) held the window for 16h40m; `>= 9223372037` overflowed
  the `time.Duration` multiplication into a **negative** duration, which
  made `up >= min` trivially true and **silently disabled the gate** —
  falsifying the self-review's finding 2 and the code's own comment.
  Reproduced both with the pre-fix code. **Fix:** added
  `maxGateSeconds = 600`; any value outside `0..600` now falls back to the
  60s default (same warning path as the existing malformed/negative case).
  New test cases cover the cap boundary, the units-confusion value, and the
  overflow value.
- **F3 — should-fix, fixed.** Shop-owner-visible behaviour change
  (cold-boot delay up to 60s) with no manual update, against the standing
  "manual ships with the feature" instruction (ut-docs#324) —
  `web/help/en/display.md`'s existing Pi-desktop-kiosk paragraph would read
  as materially incomplete. **Fix:** added a sentence there explaining the
  delay is expected, not a freeze, and not to restart during it (a restart
  just re-starts the wait).
- **F4 — should-fix, fixed.** The new `UT_SHELL_MIN_UPTIME_SECONDS` env var
  was documented only in a Go source comment. **Fix:** added a "Linux
  startup gate" section to `cmd/unitill-desktop/README.md` covering what it
  does, the tuning var, the `0..600` clamp, and that other platforms are
  unaffected. (`docs/arch/desktop-app.md` was considered but is
  macOS-specific end to end — not the right home.)
- **F5 — nit, accepted as-is.** The gate widens the window where
  `/exit-to-os` and `/apply-mode` answer 503 (no window exists yet), an
  invariant a prior review (`control.go`'s own comment) had kept
  deliberately narrow. Honest 503 given nothing exists to act on yet, not a
  regression — noted here rather than changed.
- **F6 — nit, accepted as-is.** Local var `min` in `gateDuration` shadows
  the Go 1.21+ builtin. No lint config flags it in this repo and `go vet` is
  clean; left as a possible future rename, not worth a diff on its own.
- **F7 — nit, accepted as-is.** `TestGateDurationHonoursEnv`'s "unset uses
  default" subtest calls `os.Unsetenv` with no explicit restore. Benign
  (first subtest in table-driven order; `t.Setenv` handles the rest) —
  logged, not changed.
- **F8 — folded into F1's fix.** `waitForSafeStartup` itself had no test
  because the compare-and-sleep decision was entangled with the real clock
  and `/proc/uptime`. The `holdFor` extraction in F1's fix makes it directly
  testable; `TestHoldFor` added.

## Fixes applied (this section supersedes the self-review above)

F1–F4 implemented as described; F5–F7 accepted as documented, non-blocking
follow-ups (F5/F6/F7 not tracked as separate cards — small enough to pick
up opportunistically if this file is touched again).

## Gates (re-run after the independent review's fixes)

- `gofmt -l .`: clean
- `go build ./...` / `go vet ./...`: pass
- `go test ./cmd/unitill-desktop/...` (now untagged — see F1): pass, including
  the new `TestHoldFor` and the F2 boundary/overflow cases
- Full untagged `go test ./...`: pass
- `guard-webkit-version.sh` / `guard-kiosk-launch-flags.sh` /
  `guard-help-topics.sh` / `guard-i18n.sh`: pass
- `go vet -tags desktop` / `go test -tags desktop` on real arm64 hardware:
  **not re-run** by the independent review (no GTK/WebKit toolchain, no Pi,
  in this environment) — the original session's hardware results stand,
  unverified by review, same as noted above. The pre-existing
  `TestShellAppliesWindowModeGatesTheAdvertise` failure was independently
  confirmed by static trace, not just taken on faith.

## Residual risk

A cold-booting till appears up to a minute later. Deliberate, and the
product owner's call — accepted before merge. Now documented for the shop
owner in `web/help/en/display.md` (F3) so the wait doesn't read as a
failure.
