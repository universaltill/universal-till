# Review: the ID-check sheet opens on a neutral focus (ut-docs#4077)

Date: 2026-10-17 · Branch `fix/4077-age-check-initial-focus` · Card complexity: easy
Built by the lane:cloud-54 session (Opus 5.5). Reviewed independently by a Fable subagent (fresh context).

## What shipped
- `web/ui/partials/basket.html`: the age-check sheet's prompt line gets
  `tabindex="-1" autofocus data-ut-focus-home`. Native `.show()` (the tender
  gate's self-opening script and the badge tap) now focuses the prompt, not
  the "?" help link. Accept is not autofocused, so a stray Enter accepts
  nothing.
- `web/ui/layouts/base.html`: the shared trap's `focusin` pull-back sends stray
  focus to a `[data-ut-focus-home]` element when the topmost trapped dialog
  has one, else to the first focusable as before. This is opt-in, so other
  trapped dialogs are unchanged (`#confirm-modal` keeps its `autofocus`
  Confirm).
- `web/public/app.css`: `.age-check-sheet-prompt:focus { outline: none; }`.
  The prompt is text, never in the Tab cycle (precedent:
  `.settings-cat-title:focus`).
- New e2e `e2e/tests/age-check-initial-focus-4077.spec.ts`:
  - Tender gate: the sheet starts on the prompt. Enter neither navigates nor
    POSTs `/api/pos/age-check`. Stray focus on the scan field is pulled back
    to the prompt. Tab reaches Accept, and Enter there records the check.
  - Badge tap: the sheet starts on the prompt, and Tab leaves it for a
    control.
- `web/help/img/manifest.json`: surface hash only (`Docs-Shots-Unchanged`).
  No manual screenshot shows this sheet, and the change paints nothing
  unless the sheet is open and focused.
- Wedge scanner: `app.js`'s scan buffer already calls `preventDefault()` on a
  recognised burst's Enter, so a scanner's Enter never reached the link. The
  risk was a real keyboard Enter.
- ut-docs `reference/list-and-dialog-pattern.md` documents
  `data-ut-focus-home`.

## Findings
- No blocker or major.
- **Minor, accepted:** a screen reader announces the prompt twice (the
  dialog's `aria-label` and the focused `<p>`). Harmless, and better than
  landing on the link. Switching to `aria-labelledby` needs a per-line id,
  so it is left as polish.
- **Nit, fixed:** the e2e Tab loop could press Enter on Refuse if Accept was
  never reached. It now asserts that focus is on Accept before Enter.
- **Nit, by design:** a deliberate Tab then Enter still opens the help topic.
  That is the keyboard path the card wanted kept.
- **Checked by the reviewer and fine:**
  - htmx 1.9.12 focuses `[autofocus]` after every swap. The prompt sits in a
    closed (`display:none`) dialog, so the scan field keeps focus across
    swaps (live probe, two scans).
  - `UT.dialogOpening` still exempts the native show→focus.
  - The status bar, Lock, OSK and bug-report exemptions run before the new
    branch.
  - No pull-back loop: `home.focus()` lands inside the dialog.

## Verified
- TDD: on `main` both tests fail with focus on
  `a[href=/help/age-restricted-sales]`. Reverting only `base.html` fails the
  pull-back assertion. With the fix, both pass.
- Related e2e: 38 tests pass (`showmodal-statusbar-reachable-2097.spec.ts`
  and every spec touching the age-check, elevation or confirm dialogs).
- `go build`, `go vet`, `go test ./...`, and the ci.yml build-job guards
  touched here: i18n, i18n-meta, RTL, no-showmodal, no-inline-handlers,
  docs-shots, help-topics/drift, compliance, competitor, core-neutral,
  data-access and mail.
- Looked at (driven till, screenshots): the sheet opened by the tender gate
  at 1024×600 in en, and by the badge at 1024×600 in en and fa (RTL). No
  focus ring on the prompt, layout unchanged, and Enter left the sheet open.
- Not looked at:
  - 360px: the basket is folded behind the Pay bar there, and this change
    alters no layout.
  - Dark theme.
  - A real touch device or a real wedge scanner.

## Verdict
Safe to merge.
