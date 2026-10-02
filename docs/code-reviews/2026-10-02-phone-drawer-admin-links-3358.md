# Review — phone ☰ drawer shows the manager links on every screen (ut-docs#3358)

**Date:** 2026-10-02 · **Lane:** cloud-54 · **Branch:** `fix/3358-phone-drawer-admin-links`
**Author:** Sonnet dev subagent (drawer links, spec, help) + Opus 5.5 orchestrator (Lock clearance) · **Reviewer:** Fable, independent subagent (one round)

## What shipped

- **Bug:** on an iPhone (≤480px), the ☰ drawer on the sale screen and on
  Menu listed only the operator's name ("Administrator" → Change my PIN) and
  Lock in its admin block. `body.sale-screen` / `body.menu-screen
  .session-admin-link { display: none }` exist to keep the *tablet rail*
  short, but the phone drawer (a scrollable column since #3059) inherited
  them. Fix: `body .nav-drawer .session-admin-link { display: flex }`
  inside the phone media block. Same specificity as the hide rules and
  later in the file, so it wins. It doesn't apply above 480px, so the rail
  is unchanged. Cashiers still never get the links (server-gated
  `{{ if .isManager }}`).
- **Lock reachability:** the three extra rows push Lock to the drawer foot,
  under the status row that paints over the open drawer. On the sale screen
  `--phone-sb-h` is 0 while the row is hidden at rest. Elsewhere nothing
  reserved space for the sticky `.statusbar`. Fix: `measureBar()` (app.js)
  writes the row's measured height to `--phone-drawer-sb-h` while the
  drawer is open, on open and through the existing statusbar
  ResizeObserver/online/offline hooks. `body.nav-drawer-open .nav-drawer`
  pads its end by it on every screen.
- Help: the phone bullet in `web/help/{en,de,fa,ar,tr}/sell.md` names the
  manager links, using each locale's own `users/promotions/translations.title`.
  docs-shots regenerated (manifest only; no PNG changed).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major | `/menu` (and `/users`, pre-existing): Lock fully covered by the sticky status row at 375×553, 11px under it at 390×664. The padding only applied on the sale screen. | **Fixed.** The padding rule now covers every screen. The spec checks `/`, `/menu`, `/users` at both viewports with `lock.bottom <= statusbar.top`. |
| 2 | Minor | `--phone-drawer-sb-h` was measured once on open, so a status row that grows while open (main-till poll, late chips) covered Lock again. | **Fixed.** It is now measured in `measureBar()` (observer-driven). New test grows the row while open. |
| 3 | Nit | `flex-shrink: 0` on the drawer columns was inert (`min-height: auto`). | **Fixed.** Removed. |
| 4 | Nit | The Lock test checked only Lock's centre point, so a partial overlap passed. | **Fixed.** It is now a geometric bottom ≤ top assertion and requires the status row to be showing. |

Verified OK by the reviewer: cascade/specificity, no leak to the rail at
481px/1024px, RTL identical, rAF timing, cashier gating, and the help
wording against each locale's titles (de checked against
`ut-plugin-language-de`).

## Verification

- TDD: the visibility tests failed before the CSS fix ("Expected visible,
  Received hidden") and pass after. The Lock tests: 5 of 10 fail against
  the pre-review code (`/menu`, `/users` at both viewports, plus grow-while-
  open) and all pass now. The sale-screen Lock test failed before the JS
  fix ("covered by sb-item sb-enrol").
- e2e: `phone-*`, `nav-rail*`, `basket-item-name-phone*` → 171 passed.
- `go build ./...`, `go test ./...` green. Guards from `ci.yml`'s build job
  all pass, with two exceptions that are environmental here:
  `guard-shellcheck-version` (no shellcheck binary) and
  `guard-deadcode-baseline` (fails identically on `main` without GTK
  headers).
- Looked at: drawer screenshots at 390×664 and 390×844, sale screen and
  Menu, English. Not looked at: a real iPhone/WKWebView. The check was
  Chromium with iPhone viewports only.

## Verdict

Safe to merge after the fixes above.
