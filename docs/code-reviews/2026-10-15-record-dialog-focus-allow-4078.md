# Review: record dialogs keep the status bar and Lock keyboard-reachable (ut-docs#4078)

Date: 2026-10-15 · Branch `fix/4078-record-dialog-focus-allow` · Card complexity: easy
Built by a Sonnet dev subagent; reviewed independently by an Opus subagent (fresh context).

## What shipped
- `web/ui/layouts/base.html`: the shared trap's `escAllowed` (status bar,
  `.session-lock`, `#osk`, `#bugreport-panel`) is exposed once as
  `UT.focusTrapExempt`.
- `web/public/record-dialog.js`: the `focusin` backstop uses that helper
  instead of exempting only `#osk` (OSK-only fallback if the helper is
  missing). `focusables()` now also drops `tabIndex < 0` elements, which
  matches base.html's `escFocusables`. Without it, the roving-tabindex tiles
  on /categories (`button tabindex=-1`) were the last list entry, so
  `trapTab` never wrapped. Once the backstop stopped pulling focus back from
  the status bar, Tab walked out to it. Existing case `(c4)` caught this.
- e2e case `(k)` in `categories-record-dialog-2010.spec.ts`: with a record
  dialog open, the status bar and Lock keep focus, and a focusable on the
  page behind is still pulled back. Lock only renders with auth on, so on
  the auth-off till the test plants a `.session-lock` stand-in. The
  exemption is selector-based, so a missing element can never silently pass.
- ut-docs `reference/list-and-dialog-pattern.md` "The focus trap" updated.

## Findings
- No blocker and no should-fix.
- Nits accepted, not changed:
  - `FOCUSABLE` lacks `summary`. No record dialog uses `<details>`.
  - An unchecked radio as the last entry would detour Tab once to the
    status bar. No record dialog has radios.
  - The OSK-only fallback branch is defensive code that never runs today.
  - The planted stand-in is not removed. Each test gets its own page.
- Pre-existing, out of scope: if a `data-ut-focus-trap` dialog and a record
  dialog are open together, the two backstops bounce focus a few times
  before the topmost one wins. The reviewer found no UI path that opens both.

## Verified
- TDD re-verified by the reviewer in a separate worktree:
  - With the fix reverted, `(k)` fails with `Expected "ok", Received "stolen"`.
  - With the fix restored, `(k)` passes.
  - With only the `tabIndex` filter reverted, `(c4)` fails ("Tab #8 landed
    on a.sb-item.sb-disk-low").
- e2e: the full categories spec, the 2097 status-bar spec and every
  `*record-dialog*` spec ran with the fix: 60 of 61 passed. The one failure
  is `locations-record-dialog-2124` "kiosk floor 1024x600". It fails the
  same way on `main` under parallel load and passes alone (`--repeat-each=4`).
  It is not caused by this change and is filed as its own card.
- Commands:
  - `go build ./...`, `go vet ./internal/pages/...` and
    `go test ./internal/pages/...` pass.
  - `guard-i18n.sh` and `guard-no-showmodal.sh` pass. The other ci.yml
    guards also pass, except two that cannot run in this container (not
    caused by this change; CI runs both):
    - `guard-deadcode-baseline`: the local deadcode binary was built with
      go1.26.
    - `guard-shellcheck-version`: no shellcheck binary is installed.
- Surfaces: the keyboard path on /categories at 1024×600. No visual change,
  so no screenshot was needed. Not checked: RTL or other locales, since no
  layout or text changed.

## Verdict
Safe to merge.
