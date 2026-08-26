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

## Review findings

Self-review. **The standing rule is an independent different-model review
before commit; that was NOT run in this session** (subagents were disabled).
This should be reviewed by another model or a human before merge.

1. **Test file had no build constraint** — `startup_gate_test.go` compiled into
   untagged builds where the symbols it tests do not exist, failing
   `go vet`/`go test` on the per-PR CI path. **Found and fixed before push**
   by running the untagged path explicitly. Added `//go:build desktop && linux`.
2. **Malformed env must not disable the gate.** `UT_SHELL_MIN_UPTIME_SECONDS=abc`
   or a negative value falls back to the default rather than 0 — a typo must not
   silently reintroduce an unescapable white screen. Covered by a test.
3. **Unreadable `/proc/uptime` starts immediately** rather than hanging: a till
   that opens late is bad, one that never opens is worse. Covered by a test.
4. **Gate placement is load-bearing** — it must precede `webview.New`, because
   the defect is in the surface built at window construction. Stated in the
   comment so a later refactor does not move it.

## Gates

- `go build` / `go vet` / `go test` untagged (the per-PR CI path): **pass**
- `go vet -tags desktop`, native arm64 on the Pi: **pass**
- `go test -tags desktop`: one **pre-existing** failure,
  `TestShellAppliesWindowModeGatesTheAdvertise` — it asserts
  `shellAppliesWindowMode == false` while `window_mode_linux.go`'s `init()` sets
  it true under that same tag, and the test carries no build constraint.
  **Verified pre-existing by reproducing it on `main` with this change removed.**
  Filed separately.
- `-race` unavailable on this Pi (arm64 ThreadSanitizer VMA limitation, unrelated).

## Residual risk

A cold-booting till appears up to a minute later. Deliberate, and the product
owner's call — accepted before merge.
