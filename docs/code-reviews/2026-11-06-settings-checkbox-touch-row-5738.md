# Review: Settings checkbox rows reach the 44px touch floor (ut-docs#5738)

Date: 2026-11-06 · Lane: `lane:cloud-41` · Complexity: easy
Author: Sonnet (Dev subagent) · Reviewer: Opus 5.5 (independent subagent) ·
Tester: Sonnet (separate subagent)

## What shipped

- `web/public/app.css`: one shared rule,
  `.settings-panel label:not(.staff-lang-opt):not(.ps-switch):has(> input[type="checkbox"])`,
  turns every label-wrapped Settings checkbox into a flex row
  (`flex-wrap: nowrap`, centred, `gap: .6rem`) at least `2.75rem` tall,
  with a `1.5rem` box (`flex: none`). Logical properties only. The rule
  applies under `body.kiosk` and under `@media (pointer: coarse)`, so the
  density of a desktop with a mouse stays as it was. `.staff-lang-opt` and
  `.ps-switch` already size themselves, so the rule leaves them out.
- The new `.set-block` class (`display: block; margin-block: .35rem`)
  replaces the inline `style="display:block; margin:.35rem 0"` on four
  checkbox labels in `web/ui/pages/settings.html`. An inline style would
  beat the shared rule; the class does not.
- `e2e/tests/settings-checkbox-touch-row-5738.spec.ts` covers:
  - two rows (the stock-tracking checkbox, which had an inline style, and
    the notices checkbox, a plain label) in kiosk mode at 1024×600, in en
    and fa: height ≥ 43.5px, box > 20px, box at inline-start in LTR and
    RTL, and an `elementFromPoint` hit-test;
  - the `pointer: coarse` branch, using a touch context without the kiosk
    class;
  - a desktop mouse context, where the row stays compact.
- `web/help/img/manifest.json`: only `surface_sha256` changes. Docs-shots
  render the non-kiosk desktop with a fine pointer, where the gated rule
  does not apply. The developer regenerated all shots with the change both
  stashed and applied, and got byte-identical PNGs. The 126-PNG
  difference from the committed set comes from the environment (fonts),
  not from this change, so no PNG is committed.

## Verification

- **After a rebase onto `main`:** #4247's voucher-slip checkbox merged
  during review. It is a label-wrapped Settings checkbox, so the rule
  covers it. It is now a third kiosk case in the spec (en and fa). The
  spec passes 8/8 on the rebased head.
- **TDD:** the reviewer re-verified this in a separate worktree. Without
  the CSS and markup fix, all 4 kiosk cases failed with
  `label border-box height Expected: >= 43.5 Received: 21.953125`
  (stock-tracking row) and `Received: 17` (notices row). With the fix,
  they pass (stable over 3 runs). After the review fixes, the full spec
  passes 6/6.
- **Tester driven run:** the till was started and all 35 Settings cards
  were opened.
  - 22 visible checkboxes, all label-wrapped.
  - At 1024×600 and 360×640, in en and fa, every row is 46.8px tall or
    taller, the box is 25.5px, the box sits at inline-start, and nothing
    overflows.
  - In desktop mode (1280×800 and 1024×600), x, width, height, display and
    margins are identical to `main` across 25 rows.
- **Screenshots** were taken at deviceScaleFactor 1 and looked at: kiosk
  en, kiosk fa (box on the right), and long German text at 360px.
- **Related e2e specs:**
  - Tester: 60/60 Settings-area specs and 123/123 layout sweeps, including
    `input-touch-floor-3882`, `phone-layout-sweep-3297` and
    `portrait-no-clip-sweep-3060`.
  - Reviewer: 151 passed.
- `gofmt -l .` is clean, `go build ./...` succeeds, and the build job's
  web guards pass: `guard-rtl-logical-css`, `guard-i18n`,
  `guard-fixed-dates`, `run-docs-shots-guard`, `guard-help-topics`,
  `guard-no-showmodal`, `guard-compliance-claims`,
  `guard-competitor-naming` and `guard-mail-addresses`.
- **Not verified:** real touch hardware (touch was emulated with the body
  class and a `hasTouch` context), and the dark theme.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | minor | The label inherited `.set-row`'s `flex-wrap: wrap`. At 360px under touch, the `*-accept-terms` rows dropped their text under the box. This was already the case on `main`. | **Fixed:** `flex-wrap: nowrap` in both blocks. |
| F2 | minor | The spec tested only the `body.kiosk` branch, so a typo in the `@media (pointer: coarse)` copy would pass CI. | **Fixed:** added a touch-context case and a desktop "stays compact" case. |
| F3 | minor | Gating conflicts with the ux-guidelines' "touchscreen till" stance. A windowed Pi whose primary pointer reports `fine`, for example because a mouse is attached, keeps the small rows. | **Accepted:** the card asks for kiosk/touch mode and asks to keep desktop density. Assumption: a till without kiosk mode whose primary pointer is fine is used with a mouse. If a windowed touch Pi reports `fine`, dropping the gate is a one-line change (merge the two blocks), plus a docs-shots regeneration. |
| F4 | nit | Two copies of the same declarations. | **Fixed:** added a keep-in-sync comment. The spec now tests both copies. |
| F5 | nit | The selector quoted in the spec header was missing `:not(...)`. | **Fixed.** |
| F6 | nit | `align-items: center` centres the box against a two-line label instead of aligning it with the first line. | **Accepted:** cosmetic, and consistent with `.set-row`. |
| T1 | minor (Tester) | A single unbreakable 36+ character word overflows its label at 360px. | **Accepted:** the old inline label overflowed the same way, and no shipped string has such a word. |

Pre-flight: help topic: n/a (no new behaviour or wording; only touch-row geometry; docs-shots are fine-pointer desktop and unchanged); `make docs-shots`: run, PNGs unchanged by this diff (environment drift reverted), surface hash refreshed; `reference/` / README: n/a; `en.json` / meta: n/a (no keys); mirrors / ADR / taxonomy: n/a; plan/price, platform claim, my./admin UI: n/a; review record: this file; follow-up: none.

## Verdict

Approve. No blockers or majors. All fixes are applied and re-verified
(spec 6/6, guards green).
