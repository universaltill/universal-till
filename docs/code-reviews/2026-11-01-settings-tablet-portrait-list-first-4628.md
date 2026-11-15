# Review: Settings list-first up to 900px wide (ut-docs#4628)

Date: 2026-11-01 · Lane: lane:cloud-41 · Built by Sonnet 5.5 (Dev subagent), reviewed by Opus 5.5 (independent subagent) and the orchestrator.

## What shipped
- `web/public/app.css`: the Settings shell's list-first `@media` block (one column, list until a section is picked, then the section with **Back to sections**) moved from `max-width: 40rem` to `max-width: 56.25rem` (900px inclusive, the same edge as the existing `max-width: 900px` tier). A 10" tablet in portrait (800×1280) now gets the full width for the open section instead of a ~355px panel beside the nav. Other 40rem rules untouched.
- `web/ui/pages/settings.html`: comments only (the script never reads the width; it toggles `.has-selection`).
- `web/help/{en,de,fa,ar,tr}/display.md`: the "phone-sized screen" sentence now says a phone or an upright tablet, 900 pixels wide or less.
- `e2e/tests/settings-tablet-portrait-4628.spec.ts`: 800×1280 and 600×960 in de and fa, every section by deep link: nav hidden + back visible, panel ≥ 90% of the shell, nothing inside the card scrolls sideways or pokes past its inline end; 1024×600 keeps both panes side by side.

## TDD
Before the fix the 800×1280 cases failed (`nav still displayed beside the section`, `panel 355px < 90% of shell 681px`, and `form.fee-row scrolls sideways (scrollWidth 390 > clientWidth 322)` on Payments). Re-verified by the orchestrator: reverting the one media-query line makes `800x1280 fa` fail with the same messages; restored → 5/5 pass.

## Findings
1. Medium — fa and tr help still said "phone-sized screen" (only en/de/ar were edited). **Fixed.**
2. Low — wording said "under 900px" while `max-width` is inclusive. **Fixed** (text now says "900px or less" everywhere).
3. Low — spec: `?lang=de` renders English without `UT_TEST_I18N_OVERLAY_DIR`; 600×960 already passed before the change; (c2) skips boxes under an overflow-clipping ancestor and `.settings-wide` cards are exempt. **Accepted**, documented in the spec header; fa (RTL, core) runs for real and 800×1280 is the case that proves the fix.
4. Cosmetic — one over-long reflowed comment in settings.html. **Fixed.**

Checked with no issue: no other CSS/JS/template relies on the 40rem shell edge; category landing (#3090) and search-hit flows work between 640 and 900px; landscape phones (844–900 wide) now also get list-first (an improvement, no spec asserted otherwise); RTL logical properties only; no new strings; no `showModal`; status/lock/exit unaffected. No Go file writes or paths touched.

## Verified beyond automated tests
Looked at screenshots: 800×1280 fa Payments (fee rows and Save fully inside the card, RTL), 800×1280 en list view after Back (Back to categories, search, section list), 1024×600 en Payments (two panes side by side, unchanged). Not looked at: a real 10" device, dark theme.

## Gate
`go build ./...`, `go test ./internal/pages/...`, and 55 guards from ci.yml's build job pass locally. `guard-deadcode-baseline.sh` (local `deadcode` built with go1.26 vs the go1.27 module) and `guard-shellcheck-version.sh` (no shellcheck binary) could not run in this container — CI runs them; nothing here touches Go or shell. e2e: the new spec + settings-two-pane-1960, settings-categories-3090, settings-fee-row-251, settings-anchor-phone-topbar-3148 (29 passed) and portrait-no-clip-sweep-3060 Settings at 800×1280 (4 passed).

## Verdict
Safe to merge.
