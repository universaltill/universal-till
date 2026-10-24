# Review — text fields and selects meet the 46px touch floor (ut-docs#3882)

**Date:** 2026-10-24 · **Branch:** `fix/3882-input-touch-floor` · **Lane:** `lane:cloud-41`
**Author:** Sonnet subagent (Dev + Tester) · **Reviewer:** Opus, fresh context (different model)

## What shipped

- `web/public/app.css`: one zero-specificity floor,
  `:where(input:not(checkbox|radio|range|color|file|hidden|image|button|submit|reset), select) { min-block-size: 46px }`,
  the same 46px as `.btn-touch`. Because it is `:where()`, any class rule that
  sizes a field still wins.
- Removed the kiosk `body.kiosk input, body.kiosk select { min-height: 2.4rem }`
  rule. It made kiosk fields *smaller* than windowed ones, the same mistake
  ut-docs#161 fixed for buttons. Kiosk now inherits the floor.
- Removed class caps that sat under the floor and beat it on specificity:
  - `.settings-grid form …` 2.3rem
  - `.settings-search input` 44px
  - `.gift-voucher-field input` 44px
  - `.manual-search input` 44px
  - `.setup-language-select` 2.75rem
  - kiosk `.modifiers-item-search` / `.option-set-rename input` 2.75rem
- Named compact exceptions, each with a reason comment in the CSS:
  - **basket `.qty-input` / `.disc-input`:** a budgeted row with its own tier rules. Without the exception each line grew to about 159px and broke `sale-screen-213`.
  - **`.scan-row` barcode/qty:** 2.5rem by the product owner's explicit call (ut-docs#2702).
  - **`.option-set-value-edit input`:** borderless text inside a value chip.
- New e2e spec, `e2e/tests/input-touch-floor-3882.spec.ts`:
  - 24 sweeps over settings, the settings payments section, help, users, inventory and the item form, at 1024×600 and 360px, windowed and with `body.kiosk`.
  - Every visible text-like field and select must be at least 45.5px, and each sweep must measure at least one field.
  - A sale-screen test adds a basket line and asserts the qty field stays compact.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | `.manual-search input` (44px) and `.setup-language-select` (2.75rem) still capped below the floor (class beats `:where`). `/help` search measured 45.3px. | **Fixed:** both caps removed; `/help` added to the spec. |
| 2 | Medium | The barcode field stays 42.5px, which the card's AC doesn't allow. | **Accepted** as a named exception. The product owner made this field thinner on purpose in ut-docs#2702, after this card's tester-filed AC was written. The row's icon buttons (44px) are its touch targets. Question put to the owner on ut-docs#3882; growing it later is a one-line change. |
| 3 | Medium | Spec weaknesses: the sale test ran on an empty basket (passed on old CSS), the "no fields" guard counted excluded/hidden fields, settings sections and `/help` were not covered. | **Fixed:** counts measured fields; adds a basket line and asserts qty stays compact; adds settings payments and help. |
| 4 | Low | Stale `.scan-row` comment about the removed kiosk 2.4rem rule; tombstone comment at the old `.settings-grid` rule. | **Fixed.** |
| 5 | Low | `46px` is a fixed-px minimum (ux-guidelines prefers fluid scale). | **Accepted:** it is a floor only, matches `.btn-touch`, and fields still grow with the fluid root above it. |

Reviewer also confirmed:
- No hidden, absolutely-positioned or clipped field grew across 44 routes × 2 viewports × kiosk on and off.
- No new horizontal overflow at 360px; pages grow by at most about 40px.
- Themes and plugin CSS don't size inputs.
- The basket line height is unchanged: 118.5px windowed, 135.25px kiosk.

## Verified beyond unit tests

- **TDD, re-verified by the reviewer in a throwaway worktree:**
  - CSS reverted to `origin/main`: 16 of 17 tests fail, e.g. `input#item-name` 42.2px, `select#item-tax` 41.0px.
  - CSS restored: all pass.
  - After the finding fixes: 25/25 pass.
- **Screenshots** (1024×600 and 360×740; settings, item form, sale, kiosk sale/settings), before and after, were looked at: taller fields, same layout, no overflow or wrap.
  - Not looked at: dark theme, RTL/de/fa, the setup wizard, the join-notice form.
- **Related specs:** `phone-width-layout-413`, `portrait-tablet-sale-3050`, `sale-screen-213`, `sale-screen-search-strip-2173`, `settings-fee-row-251`, `settings-categories-3090`, `catalog-item-form-1956`, `modifiers-shop-wide-2399` and `osk-reserved-height-1998` all pass.
  - One `portrait-tablet-sale-3050` case failed once under parallel load, then passed alone and 3/3 with `--repeat-each=3`.
- **Guards pass:**
  - RTL logical CSS, e2e fixtures/no-browser/no-LAN
  - i18n, compliance, competitor naming, mail addresses
  - no-showModal, no-inline-handlers, autofill, OSK, emoji-font
- No user-facing strings or help topics change; this is styling only, so the manual needs no update.

## Verdict

Safe to merge once CI is green.
