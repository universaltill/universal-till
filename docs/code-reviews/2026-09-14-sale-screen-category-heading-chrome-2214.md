# Sale screen: category heading font size + divider line (ut-docs#2214)

**Card:** universaltill/ut-docs#2214 — "Sale screen: quick-button category
headings want a smaller font and no divider line above." Reported by the
product owner looking at the real sale screen on-device.
**Complexity:** easy. **Branch:** `fix/2214-category-heading-chrome`.

## What shipped

Two small visual changes to the category heading above the sale screen's
quick-button grid (`web/public/app.css`), no template/Go changes:

- `.category-header` font-size `1rem` → `.85rem` — matches the size this
  file already uses elsewhere for secondary/label text (`.field-error-msg`,
  `.option-set-chip`, `label`, etc.), not a new invented value.
- `.products-finder .tab-bar` gains `border-block-end: 0`, removing the
  generic `.tab-bar`'s divider line where it sits directly above the
  category heading/quick-button grid on the sale screen specifically —
  same precedent `.catalog-form-head .tab-bar` (ut-docs#2024) already set
  by zeroing the same border for its own context. The generic shared
  `.tab-bar` class (tender panel, `setup.html`, `tills.html`) is untouched.
- `.products-strip > input[type="search"]`'s height-matching padding
  constant updated `calc(.5rem + 4px)` → `calc(.5rem + 3px)`: removing the
  tab bar's 2px border-bottom shifts the documented height-matching
  arithmetic between the tab bar and the search `<input>` that swaps in
  for it (ut-docs#2173's "same row, same height" invariant, pinned by
  `e2e/tests/sale-screen-search-strip-2173.spec.ts`). Caught by that exact
  test failing (`|Δheight|` 2px instead of <1px) on the first pass before
  this constant was updated — not a hypothetical.
- `make docs-shots` re-run (`web/public/app.css` is part of
  `guard-docs-shots.sh`'s hashed surface): regenerated
  `web/help/img/{en,fa,ar,tr}/sell.png` + `manifest.json`. No other topic's
  surface changed, so no other screenshot moved.

## Verification (beyond reading the diff)

- Booted a real till (`e2e/run-till.sh`) and screenshotted the sale screen
  at 1024×600 with Playwright before/after: divider line gone, heading
  legible at the smaller size, confirmed in both LTR and RTL (`?lang=fa`).
- Ran the targeted e2e regression set:
  `sale-screen-category-tabs-search-418`, `sale-screen-search-strip-2173`
  (including the exact strip-height invariant test), `product-tile-
  category-color-1325`, `tab-bar-overflow-aria-424`,
  `category-switch-stale-tile-add-1433` — all green.
- `scripts/ci/guard-docs-shots.sh` green after regeneration.

## Independent review

Fresh-context Sonnet subagent, isolated worktree, read-only + its own live
verification (not a rubber stamp — instructed to find real problems).

Re-derived the height-matching constant from first principles rather than
trusting the comment's arithmetic, and independently confirmed it lands on
the same `+3px` value now committed. Grepped every `.tab-bar` rule in
`app.css` plus every template using the class to confirm `.products-finder
.tab-bar { border-block-end: 0 }` cannot leak into the generic `.tab-bar`
(tender panel, `setup.html`, `tills.html`) or the already-separately-zeroed
`.catalog-form-head .tab-bar`. Independently booted the pre-change commit
on a second port as a true side-by-side baseline (not just diffing my
screenshots), re-ran the full e2e regression set itself (22 passed) and
`guard-docs-shots.sh` itself, and visually inspected all four regenerated
locale screenshots (not just en/fa) for corruption or a wrong crop.

**Found:** no bugs or regressions. `.85rem` confirmed as an existing
convention via grep (15+ other uses in this file), not an invented value.
No new user-facing strings introduced (i18n check clean). All changed/new
CSS properties are logical (`border-block-end`), consistent with this
repo's RTL rules — no `left`/`right` introduced. No money/tax/security/
data-loss surface touched.

**Process note (not a code defect):** the reviewer flagged that this
review record itself didn't exist yet at review time — filed here,
closing that gap before merge.

No blockers found. No deferred/Backlog-worthy items.

## Verdict

Safe to merge.
