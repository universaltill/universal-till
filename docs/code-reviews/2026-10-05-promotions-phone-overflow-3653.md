# 2026-10-05 — Phone cards: row edit forms ran off the card's start edge (ut-docs#3653)

**Lane:** lane:local · **Built by:** Opus 5.5 · **Reviewed by:** Fable (independent subagent, separate worktree)

## What shipped
- End-user report (bug-reports#4): on a phone-width till, Promotions "goes out of the display" once a discount exists. Root cause: at ≤480px a table is a card list (#3359); the header-less actions cell is a `flex-end` flex row, and the row's inline edit form (`.users-inline`: type, value, description, two dates, Save ≈ 715px, no wrap) spilled past the card's inline-START edge, where `table.table`'s `overflow: hidden` cut it off. Five controls were unreachable. `/kitchen-stations` had the same defect (name + destination select).
- `web/public/app.css`, ≤480px only: `table.t-cards .t-cards-actions .users-inline` wraps (flex-end, `max-inline-size: 100%`); its inputs/selects grow to fill the line; date inputs keep `min-inline-size: 9.75rem` (the shared 7.5rem width cut the year off at "31/12/202"). Scoped to the card tier, because a bare wrap on `.users-inline` regressed the desktop /users row (#898).
- Phone sweep (`phone-layout-sweep-3297.spec.ts`), the guard that missed this: `offenders()` checks both viewport edges, not only `right`; `reachable()` counts only `auto`/`scroll` ancestors, since `hidden`/`clip` cut content off (closes #3359 review finding 4); `beforeAll` seeds one promotion and one kitchen station so those list pages are swept with a row, checking the 303 `Location` so a drifted field name fails loudly.
- New `phone-promotions-3653.spec.ts`: 360/440 × en/fa, all 7 row controls inside the card on both edges, dates ≥150px wide.
- `fixtures.ts` comment updated (two default-project specs now use `beforeAll` for non-basket rows).
- Docs-shots surface hash refreshed without new PNGs: the harness renders at 1024×600 and the rule is ≤480px only (reviewer re-derived the hash and confirmed). Local macOS regeneration differed only in font rasterisation from the committed Linux set, so it was discarded.

## Review findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `/kitchen-stations` had the identical overflow, but the sweep visited it empty, so nothing locked the fix in | Fixed: sweep seeds a kitchen station; with the CSS reverted the sweep fails on /kitchen-stations (left=-230) and /promotions (left=-407) at 360 and 440 |
| 2 | minor | `status < 400` seed check could never fail (every outcome is a 303, followed to 200) | Fixed: `maxRedirects: 0`, assert 303 + exact `Location` |
| 3 | minor | `fixtures.ts` said no default-project spec uses `beforeAll` | Fixed |
| 4 | nit | CSS comment claimed the desktop /users row never carries `.t-cards` (table-cards.js adds it at every width) | Fixed: comment says the media query is what scopes it |
| 5 | nit | the both-edge check would flag a future `left:-9999px` sr-only element in `main` | Accepted: none exists; sweep green on all 40 routes × 2 widths |
| 6 | nit | card-tier selects are 41px (<44px); promotions edit dates have no label | Pre-existing → ut-docs#3662 |

Not seeded: a table. /tables already wraps via its own rule (reviewer looked at it seeded), and a floor-plan table on the shared worker till risks the tables designer/picker specs.

Also filed: ut-docs#3661. Above the phone tier, the promotions table is ~1437px wide (an inline form in every row), so Status and the actions start off-screen even at 1920px; reachable by scrolling the card, so not part of this bug.

## Verified beyond automated tests
- TDD: the new spec and the both-edge sweep failed on the pre-fix CSS (4/4 and 2/86 on /promotions; the date-width assertion failed before the date rule). The reviewer independently reverted only `app.css`: 6 failed / 84 passed, exactly the promotions cases; restored: all pass.
- Screenshots at 360px read by eye: /promotions en (light), fa (dark, RTL mirrored), tr (dark); 440px de; /users, /locations, /registers, /tables, /kitchen-stations; 1024×600 en + de dark unchanged. Reviewer separately looked at /promotions en/fa/de, /users, /tables, /kitchen-stations, /locations, /registers, /country-settings, /fiscal-register.
- Gate: `go build`, `go vet`, `go test ./...` green; 80 CI guards green. Local-only failures, all environmental: BSD sed in `guard-competitor-naming_test`, shellcheck 0.11 vs pinned 0.9, Go deadcode baseline (no Go in this change). Docs-shots and e2e-fixtures guards pass. e2e: phone sweep + promotions + table-cards + permissions matrix 101 passed. Every spec touching promotions/kitchen-stations, run on ONE worker after the seeding sweep: 126 passed. Reviewer's run: 154 passed.

## Not verified
Real phone hardware (Chromium mobile emulation only); screen reader.

## Verdict
Safe to merge.
