# 2026-10-02 — Phone: tables become stacked cards (ut-docs#3359)

**Lane:** lane:cloud-24 · **Built by:** Opus 5.5 (dev subagent) · **Reviewed by:** Fable (independent subagent, separate worktree)

## What shipped
- `web/public/table-cards.js` (new, deferred in `base.html`, listed in `httpx.HeadAssets`): every `main table` with a `<thead>` gets `t-cards` and each body cell a `data-label` copied from its column header (colspan honoured). First labelled cell = card title; header-less cells = row actions. A row with more than 4 labelled cells keeps the rest behind a **More/Less** button (`aria-expanded`, caption via `data-caption`/`aria-label` so the list filter never matches it, `stopPropagation` so it never opens the record dialog). Re-runs on `htmx:load`, idempotent. Opt-outs: `data-cards="off"` (sale basket), `data-cards-collapse="off"` (permissions matrix).
- `app.css` ≤480px: the #3297 "table scrolls inside its box" rule is replaced by the card layout; wrappers that existed only to scroll a table sideways stop scrolling at that tier. Logical properties only; >480px unchanged (docs-shots PNGs byte-identical).
- `/translations` input and `/receipt-designer` preview fixed after the new guard flagged them. Tab strip / chip rows marked `data-hscroll-ok`.
- Guard: `phone-layout-sweep-3297.spec.ts` now fails on any box in `main` that scrolls sideways at 360/440px (not only the page), except `data-hscroll-ok`, and fails if a table sits under that exemption. New `phone-table-cards-3359.spec.ts`. `permissions-matrix-3132.spec.ts`: pinned-column scroll case moved to 520px; 360px card cases in en + fa.
- i18n `common.show_more` / `common.show_less` (en, ar, fa, tr; de/es packs follow-up PRs). Manual: `menu.md` (all 5 languages) explains the phone card view; `users.md` permissions paragraph updated.

## Review findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `users.md` (5 locales) still said the permissions grid scrolls sideways on a narrow screen | Fixed: tablet scrolls, phone shows one card per permission; docs-shots regenerated |
| 2 | minor | `de/menu.md` new sentence used "du" in a "Sie" topic | Fixed |
| 3 | minor | card cell `color: var(--text)` overrode `td.muted` | Fixed: colour only on the title cell |
| 4 | minor | guard ignores `overflow-x: hidden/clip` boxes that clip a table | Accepted — no such box today; noted as follow-up candidate |
| 5 | nit | `data-hscroll-ok` could silently exempt a table | Fixed: sweep fails on a table under the marker |
| 6 | nit | basket negative test passes without the script | Accepted — companion to 5 positive tests |

Reviewer hunted and cleared: colspan mapping, idempotency, single-`<tr>` htmx swaps (`/translations`), list filter + `[hidden]`, XSS (setAttribute + CSS `attr()` only), listener leaks across boosted navigation, desktop regressions, RTL, touch target (44px).

## Verified beyond automated tests
- TDD: Dev's new spec failed 4/5 before code. Reviewer neutralised `table-cards.js` → new spec 5 failed / 1 passed (the negative), restored → 6 passed. Reviewer also re-inserted the old #3297 overflow rule with the script off → the sweep failed on /audit, /country-settings, /translations, /users/permissions naming each table.
- Screenshots at 360px read by eye: /locations, /users/permissions, /translations, /inventory, /audit, /country-settings (More expanded), /registers, /catalog/tax-codes, /reports; 1280px /country-settings and /users/permissions unchanged.
- Gate after review fixes: `go build`, `gofmt`, `go test ./...` (76 packages ok), 19 CI guards green, e2e phone-table-cards + phone sweep + permissions + popup-fit + drawer + phone-sell → 116 passed.

## Not verified
Real iPhone/Safari (Chromium mobile emulation only); screen reader on the card view.

## Verdict
Safe to merge.
