# Review: popup close shrink has its own duration and curve (ut-docs#2987)

- **Date:** 2026-09-27
- **Branch:** `fix/2987-popup-close-shrink`
- **Design:** ADR-0123 (ut-docs), amends ADR-0122 §1 for the close only
- **Author:** Sonnet dev subagent (complexity:easy). **Reviewer:** Opus 5.5, fresh context.

## What shipped

- `web/public/app.css`: new `--ut-zoom-close-ms`, 400 ms on `:root` and
  240 ms under `html.fx-balanced`. `--ut-zoom-small-ms` (the open) is
  unchanged.
- `web/ui/layouts/base.html`: `POPUP_CLOSE_EASE = cubic-bezier(.4, 0, .6, 1)`
  and `closeMs()`, which falls back to `smallMs()`. `popupClose` uses both
  for the animation and for the backstop (`ms + 250`). Its keyframes hold
  opacity at 1 to offset .55, then fade. `popupOpen`, the pane and page
  zooms, and the Light / reduced-motion paths are untouched.
- Tests:
  - `TestPopupCloseUsesCloseMsAndEase` (new).
  - `TestAppCSSHasNoPersistentPanelEase` (extended with the close tokens).
  - `popup-zoom-2944.spec.ts`: the spec now slows the close through
    `--ut-zoom-close-ms`. It asserts the close duration (Full, and 240 at
    Balanced), the easing string, and that opacity is still 1 at 40 % of
    the close.

## Root cause

The close re-used the open's arrival ease-out at 300 ms, with opacity on
the same curve. I measured it frame by frame in headless Chromium at
60 fps (a rAF sampler that was not committed):

| | frames painted | frames visibly shrinking at opacity ≥ .5 |
|---|---|---|
| before | 19 | **2** (≈ 30 ms) |
| after | 24 | **13** (≈ 200 ms) |

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| minor | `closeMs()` falls back to 300 ms if someone sets `--ut-zoom-close-ms: 0ms`. | Accepted. Motion is switched off through the effects level, not the token. |
| minor | A closing popup can overlap a newly opened non-modal popup for up to 400 ms (was 300 ms). | Accepted. It is inert and takes no pointer events, as before. |
| nit | The ADR-0123 rationale was repeated in three comments. | Fixed: the header comment is now one line. |
| nit | The app.css note says "at most --ut-zoom-close-ms"; the backstop is +250 ms. | Accepted. The wording was inherited, and `finish` ends it at close-ms. |
| nit | The guard regex pins the exact keyframe literal. | Accepted, intentionally strict. |

The reviewer grepped for anything else assuming "close ≤ 300 ms" or
reading `--ut-zoom-small-ms`. Only open-only readers remain (the pane zoom
and the specs that slow an open). The e2e suite runs with reduced motion
by default, and the opt-in specs wait with `toBeHidden` / `poll`, never a
fixed 300 ms. The scrim treats `[data-ut-closing]` as closed.

WAAPI: a missing `transform` in an intermediate keyframe is valid. The
two end keyframes are explicit, so no engine is left with an implicit
start or end keyframe. If an engine did throw, the `catch` runs `done()`,
which means no shrink, same as motion off.

## Verification

- The reviewer re-ran the TDD red/green itself. With the web/ changes
  reverted, both Go tests fail with the expected messages. Restored, they
  pass.
- `go build ./...`, `go vet`, `gofmt -l .` (clean), `go test ./...` (all
  ok) and `golangci-lint run ./...` (0 issues).
- CI guards: i18n, data-access, kiosk-engine, core-neutral, compliance,
  competitor naming, help topics/drift, htmx/osk loaded, e2e fixtures and
  docs-shots all pass.
- The docs-shots surface hash was refreshed without regenerating
  screenshots. The change is motion only: open popups and the settled
  screens render the same pixels, and the docs-shots harness runs with
  reduced motion.
- e2e: `popup-zoom-2944`, `in-panel-dialog-transitions-2338` and
  `tree-pane-zoom-2943` all pass (17 tests).

**Not verified:** the real tablet (`adb screenrecord` frame count) and
the Windows VM. Both are out of reach for a cloud lane and are filed as a
`blocked:env` device-check follow-up.

**Verdict:** safe to merge.
