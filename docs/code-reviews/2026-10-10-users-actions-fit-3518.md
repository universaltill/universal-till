# Review: /users row actions no longer clipped at till widths (ut-docs#3518)

**Date:** 2026-10-10 · **Lane:** lane:cloud-54 · **Author models:** Sonnet 5.5
(first draft), Opus 5.5 (revisions) · **Reviewer model:** Fable, fresh
context, one round plus a scoped re-check of the post-review changes.

## What shipped

- `web/public/app.css`, two rules scoped to `/users` (`#users-list` is only
  in `users.html`):
  - `#users-list td:first-child { overflow-wrap: anywhere; min-inline-size: 8rem; }`:
    a long unbreakable username or display name no longer takes the actions
    column's width. Table auto layout sizes columns from min-content.
    `anywhere` stays on the user cell only; on role/PIN/status it split
    "kasiyer" and "ayarlı" mid-word.
  - `@media (max-width: 1279px) { #users-list .users-inline { flex-wrap: wrap; … } }`:
    below 1280px a form's button wraps under its field instead of running
    past the card. From 1280 the forms stay `nowrap`, so the ut-docs#898
    regression can't return (the role select and Change role splitting
    onto two lines at desktop width). The guard for it is
    `fiscal-register-address-form-overflow-2420.spec.ts:111`, which still
    passes.
- `e2e/tests/users-actions-fit-3518.spec.ts` checks two users, one with a
  31-char unbreakable username and one with display name "Administrator":
  - en/tr/fa at 1024×600, 800×600, 1101×600 and 1280×600: every visible
    control in `.users-actions` sits inside the `#users-list .table` box, and
    the table has no hidden sideways overflow;
  - "Administrator" renders on one line at 1024;
  - the role select and Change role share a line at 1280×800.
- `web/help/img/{en,tr,fa,ar}/users.png` and `manifest.json` are regenerated
  (`make docs-shots`). Only the users screenshots changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| D1 | blocker (Dev) | The first draft wrapped `#users-list .users-inline` at every width. That broke the 2420 spec's #898 nowrap guard at 1280, and with a 14-char username it still split the role form at 1280. | Fixed: the wrap now applies only below 1280px. |
| D2 | blocker (Dev) | Long unbreakable usernames, such as the `swap2762_<ms>` users the 2762 spec leaves on a shared worker DB, starved the actions column. Wrapping can't fix that, so the spec failed when run alongside other specs. | Fixed: the user cell gets `overflow-wrap:anywhere`, and the spec now uses a long username on purpose. |
| O1 | should-fix (orchestrator, visual) | With `anywhere` on every text cell, words split mid-word ("kas\|iyer"). With a 5rem floor, the regenerated help shot showed "Administra\|tor". | Fixed: the user cell only, with an 8rem floor. A test pins "Administrator" to one line. |
| R1 | should-fix | The help screenshots were not yet in the branch. | Fixed: regenerated and committed. |
| R2 | should-fix | `POST /api/users` answers refusals and duplicates with 200, so `resp.ok()` proved nothing. Running the create in `beforeEach` also logged 6 spurious server errors. | Fixed: the spec skips the POST if the row already exists, and checks `X-UT-Response: ok`. |
| R3 | nit → real | The spec did not measure just above the old 1100px breakpoint. Adding 1101 showed tr clipping by 5–10px between 1101 and ~1140px. | Fixed: the breakpoint moved to <1280px, and 1101 and 1280 are in the spec. |
| R4 | nit | CSS 2.1 leaves `min-inline-size` on an auto-layout `td` undefined. All three engines honour it, and if one ignores it the name column only gets narrower; nothing is clipped. | Accepted. |

## Verified beyond automated tests

- Reproduced the bug first on a live till (`e2e/run-till.sh`). At 1024×600,
  tr lost "PIN ayarla", "Süper yöneticiye yükselt" and "Rolü değiştir" past
  the card edge, and en lost "Change role" by 3px.
- TDD: with the CSS stashed, 12 fit cases fail, 1280 among them because of
  the long username. With the CSS, 18/18 pass, together with the 2762 and
  2420 specs on one worker.
- Screenshots read for en/tr/fa/ar at 1024, 800, 1101–1366, 1920 and 360
  (the phone card tier). Test data: short names, "Administrator",
  "maria.schneider", `swap2762_…` and a 31-char username. Nothing is
  clipped, no words are split outside the user cell, and RTL mirrors
  correctly.
- Not checked: dark theme (the change touches no colours); real 10-inch
  hardware (only emulated viewports); the de pack (not installed on e2e
  tills; tr is the longest core locale here).

## Verdict

Safe to merge.
