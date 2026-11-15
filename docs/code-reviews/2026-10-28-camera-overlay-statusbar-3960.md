# Review: sell-screen camera overlays clear of the status bar (ut-docs#3960)

## What shipped

- `web/public/app.css`, `.ai-identify-overlay` / `.ai-identify-panel` (the
  barcode-scan and plugin-identify overlays on the sale screen): the panel
  was capped at `90dvh` and centred, so at the 1024×600 kiosk floor its
  bottom edge sat ~3px under the 33px status bar. The overlay now pads its
  end by the measured status row (`--phone-sb-h`, set by app.js
  `measureBar` on every layout, 0 while the bar is hidden; `4rem` fallback
  before the first measure), and the panel is capped at
  `100dvh − 2rem − --phone-sb-h`. That leaves 1rem around the centred panel,
  so it never reaches the bar. At 1024×600 the panel keeps its natural
  529px height and does not scroll.
- Phone sale screen: the fixed status row (z 39) sat under the overlay
  (z 60). While the camera was open it was dimmed and could not be tapped.
  A rule beside the existing bug-report-panel lift raises it to z 61 while
  an `.ai-identify-overlay` is open. Status stays reachable (CLAUDE.md
  offline-first rule).
- New e2e spec `e2e/tests/camera-overlay-clear-of-statusbar-3960.spec.ts`:
  1024×600 and 360×740, each in LTR and `?lang=fa` (asserts `dir=rtl`).
  - On phones it injects a problem chip so the status row is really shown.
  - It asserts the bar is visible, the panel top is ≥ 0, and the panel
    bottom is at least 8px above the bar. It checks this both at natural
    height and with a long status text that pushes the panel to its cap.
  - At 1024×600 the panel must not scroll.
  - With the overlay open, `elementFromPoint` at the bar's centre must be
    inside `.statusbar`.

## Review

Built by Sonnet; independent review by Opus 5.5 in a separate worktree.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 0 | (Dev, before review) The first design used a plain `100dvh − 8rem` cap, copying `#modifier-modal`. The 360px status row wraps to 81px, more than the 4rem margin, so the panel still ran under it. A version that kept the 8rem cap and also reserved the bar double-reserved on desktop: the panel shrank to 439px and scrolled at 1024×600. | should-fix | Fixed: reserve `--phone-sb-h` plus 1rem each side |
| 1 | On the phone sale screen the overlay painted over the fixed status row, so status could not be tapped while the camera was open. This predates the change, but the new reservation made it look solved. | medium | Fixed (z 61 lift while the overlay is open, plus a reachability assertion) |
| 2 | The spec's phone legs only tested something when a problem chip happened to show. On a healthy CI runner the bar is hidden there. | low | Fixed (chip injected, bar visibility asserted in every leg) |
| 3 | Short landscape viewports (740×360, 640×320): the 4:3 video fills the capped panel, and Close is only reachable by scrolling inside it. This predates the change and is not a regression. | low | Deferred → ut-docs#5122 |
| 4 | The capped-height spec step's 120× status text overlaps Close inside the panel. Real strings are one line. | info | Accepted (test-only content) |

## Verification

- **TDD, re-verified by the reviewer in its own worktree:**
  - With `main`'s app.css, the new spec failed 4/4:
    - 1024×600: the panel ends 1.8px above the bar (564.8 vs 566.6), short of the 8px it needs.
    - 360×740 LTR: the panel ends at 703, below the bar top at 659.2.
    - 360×740 fa: the panel ends at 703, below the bar top at 682.3.
  - With the fix, the new spec and `sale-screen-camera-barcode-scan-548.spec.ts` gave 17/17.
- **Reachability assertion:** it failed on the 360 legs without the z-61 rule (`div#barcode-scan-overlay` was the hit) and passed with it. The Dev ran this.
- **Final gate:** I re-ran both specs on the final tree, 17 passed. The `build` job guards all pass. Two exceptions come from this environment and do not touch CSS or e2e: `guard-deadcode-baseline` (the local deadcode binary is built with go1.26, but the repo needs go1.27) and `guard-shellcheck-version` (no shellcheck installed). CI runs both.
- **Looked at screenshots:**
  - 1024×600 LTR: the panel spans 18–548 and the bar starts at 567. Title, video, hint and Close are all visible, and the bar is not dimmed.
  - 360×740 LTR and fa: nothing is clipped, Close is the hit target, and fa is fully mirrored.
  - 740×360 and 640×320: see finding 3.
- **Not checked:**
  - the plugin-identify overlay (it needs an installed plugin, but uses the same CSS)
  - a real device or WebKit `dvh`
  - notch safe-area
  - a status row of three or more rows
  - the help manual: no wording changed, and no help topic shows this panel's geometry

## Verdict

Safe to merge.
