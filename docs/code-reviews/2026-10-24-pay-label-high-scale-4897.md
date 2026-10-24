# Review record: Pay label clipped at high UI scale (ut-docs#4897)

Branch `fix/4897-pay-label-high-scale`. Built by Sonnet 5.5; independently reviewed by Opus 5.5.

## What changed

- `web/public/app.css`: `.tender-default-footer` wraps; Pay's flex-basis is 7rem
  (`flex: 1 1 7rem`), `max-inline-size: 100%`. At scale 1.5/2 side by side the icons now
  drop under Pay instead of Pay ellipsising to "Pa…". At scale 1 nothing wraps.
- `e2e/tests/portrait-no-clip-sweep-3060.spec.ts`: new test, scale 1.5 and 2 at 1024x600,
  1280x800, 1920x1080: Pay not clipped, Pay and icons inside the tender column, column does
  not scroll sideways. It failed on main (Pay clipped 27-40px), passes now.

## Review findings

1. High: my first version used `min-inline-size: min-content`, which beats `max-inline-size`
   and let a long label overflow the column. Removed.
2. A first wrap-always version broke `compact-tender-panel-2702` (long label must stay one line)
   and `phone-width-layout-413`; the 7rem basis keeps both green.
3. The test could not see a Pay overflowing its column; added the column and Pay-box checks.

## Not in scope

At 1024x600 and scale 1.5 the whole sale grid is wider than the window on main
(scrollWidth 1167 vs 1024), unrelated to this row. Filed as a follow-up note on #4897.

## Verification

compact-tender-panel-2702, phone-width-layout-413, portrait-no-clip-sweep-3060,
tender-panel-reachable: 46 passed. `guard-rtl-logical-css.sh` clean. CSS and one e2e spec only.
