# Review — list-actions row borders + record-dialog touch targets (ut-docs#5576)

Date: 2026-11-05 · Branch: `fix/5576-list-actions-touch-targets` · Lane: `lane:cloud-54`

## What shipped
- `web/public/app.css`: `.list-actions` flex now applies only to non-cell
  containers (`.list-actions:not(td)`, the designer category list in
  `buttons.html`). On a `<td>` the old `display:flex` replaced
  `table-cell`, so the actions cell stopped stretching to the row and its
  bottom border sat above the row line on wrapped rows (/promotions,
  /locations, /fiscal-register, /country-settings, /registers).
  `td.list-actions` stays a table cell: `nowrap`, `text-align: end`,
  `vertical-align: middle`, logical `margin-inline-start` between buttons.
- Record-dialog status row (`.record-dialog-status-row .btn` / `.btn-icon`):
  minimum 44px (was 1.9rem ≈ 33×32), the touch floor in
  `reference/ux-guidelines.md`. Literal px so it holds below `--ui-scale` 1;
  still thinner than a full 3rem `.btn` (≥51px at the 17px fluid floor).
- New `e2e/tests/list-actions-touch-targets-5576.spec.ts`: every visible
  cell of a wrapped row is `table-cell` and shares one bottom (±1px) on
  /promotions and /locations (/categories as a regression guard — it has no
  actions cell since #2699); dialog exit + lock ≥44×44, inside the
  viewport, still pointing at `/settings#settings-display` and
  `/api/auth/logout` (#1999), at 1024×600 and 360×740.
- ut-docs `reference/list-and-dialog-pattern.md`: re-measured head height
  (status row ~51px, pinned head ~187px) in a companion ut-docs PR.

## Verification
- TDD re-verified twice, independently of the Dev agent (Tester, and the
  Opus review subagent in a detached worktree): with only `app.css` reverted
  4/5 tests fail (`displays …,flex`; width 33.4 < 43.5); restored → 5/5 pass.
- Regression: 5576 + 3661 + 2124 + 2010 + 2122 + 2000 specs 50 passed;
  2011 + 1956 (head < 30% budgets) + 2185/2186/2187 (registers,
  fiscal_register, country_settings) 37 passed; phone-promotions 3653 passed.
- Visual (Tester looked at every shot): before — the pencil cell's border
  floats above the row line; after — one row line, button centred. Dialog
  at 1024×600, 360×740 and in fa (RTL): 44×44, mirrored, nothing clipped.
  Not checked on a real touchscreen/kiosk Pi.
- Guards: every `ci.yml` build-job guard passes locally except
  `guard-deadcode-baseline` (container Go 1.26 < go.mod 1.27) and
  `guard-shellcheck-version` (no shellcheck binary) — toolchain, not this
  change; CI runs both.

## Findings (Opus 5.5 review of Sonnet-built code)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Leftover `console.log("MEASURE …")` in the spec | fixed |
| 2 | nit | `btnWrapped` computed, never asserted | fixed (removed) |
| 3 | nit | Fixed promo code `T5576<width>` would collide under `--repeat-each` | fixed (`Date.now()` suffix) |

The reviewer also confirmed: `:not(td)` touches only `buttons.html:1190`; no
`th.list-actions`, no JS/Go/plugin references; the phone `t-cards` rules
(0,2,2) still beat `td.list-actions` (0,1,1), so ≤480px is unchanged.

Pre-flight: review record updated; help prose n/a (layout fix only, no behaviour or text change); `make docs-shots` regenerated (country-settings ×4 locales changed; dialog-open shots don't exist yet → #5581); en.json n/a (no strings); reference updated (`list-and-dialog-pattern.md`, ut-docs PR); README n/a.

Verdict: **approve**. Deferred: in fa the dialog title "Edit promotion" stays English (seen by the Tester, outside this card) → Backlog card.
