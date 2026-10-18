# Review: line discount above the line total is refused on entry (ut-docs#4627)

## What shipped

Before this change the basket's discount box (`POST /api/pos/line`) accepted a
line discount larger than the line itself (150 on a £1.20 item). The tender
then failed inside `computeSaleTotals` ("invalid line discount"), and the
cashier saw only the generic "sale could not be completed" banner, with no
line and no reason.

- `pos.LineDiscountExceeds(unitPrice, qty, discount)` is now the one rule.
  `computeSaleTotals` uses it, and its server check stays.
- `Service.UpdateLine` / `UpdateLineByKey` refuse an update whose discount
  would be more than the line total at the requested qty. They return
  `*pos.LineDiscountTooLargeError` (line key + limit) and leave the line
  unchanged. This covers two cases:
  - a too-big discount typed into the box;
  - a − step or a qty edit that drops below what the discount covers.
  A qty of 0 (void) and gift voucher lines are never refused.
- `/api/pos/line` puts the refusal on the rendered basket copy
  (`Basket.DiscountErrorKey`/`DiscountErrorLimit`, per render, `json:"-"`).
  `basket.html` shows `basket.discount_too_large` ("The discount can't be
  more than this line's total (£1.20).") under the item name, with
  `role="alert"`.
- `/api/pos/tender` pre-checks every line before any payment and names the
  line in an error toast (`pos.toast.line_discount_too_large`). This is the
  same shape as the `pos.toast.tax_unavailable` precedent. Only a held
  basket or an older snapshot can still reach it.
- New keys in en/tr/fa/ar and in `en.meta.json`. The de/es/pt packs follow
  in their own PRs.
- The help topic `sell.md` (en/de/tr/fa/ar) documented the old bug. It now
  describes the new behaviour.

**Design choice:** refuse, don't clamp. The box takes minor units, so a
cashier who types `150` meaning £1.50 off a £1.20 item would, with clamping,
silently give the item away. Refusing keeps the old value and says what the
most is.

## Review

- **Reviewer:** Sonnet 5.5, an independent pass. The author model was Opus
  5.5. Fable was the mapped reviewer, but its call was refused (out of usage
  credits), so per MODEL-ROUTING the review fell back to Sonnet.
- **TDD re-verified by the reviewer.** With the `pos_api.go` change reverted,
  the three new pages tests failed for the right reasons:
  - discount applied (150);
  - qty step applied (qty 2);
  - no named toast.
  All three passed again once the change was restored.
- **Findings:**
  - **minor, fixed:** the first version checked in the handler by reading
    `Engine.Basket()` and then called `UpdateLineByKey` separately. That is a
    read-then-write race between two near-simultaneous requests on one line.
    The tender check still caught it, but the check now runs inside the
    Service methods under `s.mu`. New test:
    `TestUpdateLineByKey_RefusesDiscountAboveLineTotal`.
  - **minor, fixed:** `Basket()` was read a second time in the refused
    branch. That branch is gone, and the error fields come from the engine's
    refusal.
  - **nits, no change:**
    - The order-type split/merge can't produce an oversize discount.
      Floored per-unit discount: D − ⌊D/q⌋ ≤ (q−1)·p.
    - Weighed lines use the same `MulQty` rounding as `computeSaleTotals`.
    - A request with no `discount` parameter resets the discount to 0. This
      is pre-existing behaviour.
- **Also checked:**
  - Translations: one `%s` each, and the terminology matches neighbouring
    keys.
  - CSS uses logical properties only.
  - No modal, no network dependency.
  - No file writes or paths, so the `os.MkdirAll` / `paths.Data` checks
    don't apply.

## Verified beyond unit tests

- e2e `e2e/tests/line-discount-cap-4627.spec.ts` against a real till at
  1024×600 (the kiosk floor):
  - `99999` is refused;
  - the note shows the line total and fits in the viewport;
  - the box stays empty;
  - a fitting discount (`10`) applies and clears the note.
- I looked at the screenshot. The note sits under the item name in red
  bold text, and the words carry the meaning, not the colour alone. It
  doesn't overlap the qty/discount column.
- Full gate run once, after the last edit (results are in the PR):
  - `go test -timeout 20m ./...`;
  - `golangci-lint run ./...` (0 issues);
  - guards: i18n, i18n-meta, help-drift, help-topics, rtl-logical-css,
    data-access, page-http-error, core-neutral.
- `guard-deadcode-baseline.sh` can't run in this container: the local
  `deadcode` was built with go1.26 and the module needs go1.27. It fails
  the same way on `main`, so CI's run is the check.

## CI follow-ups

- **`build`:** the docs-shots guard flagged the changed basket surface and
  `sell.md`. I ran `make docs-shots`: every PNG came out byte-identical,
  because the note only renders on an error. Only `manifest.json`'s hashes
  moved.
- **`playwright`:** `basket-no-horizontal-scroll-391.spec.ts` typed a
  deliberately wide `1234` discount on a £1.20 line, which this change now
  correctly refuses. The spec now raises that line's qty to 11 first, so
  1234 fits, and the qty box gets a two-digit value as well. 13 tests
  failed for this one reason.
- **5 flaky specs:** these passed on retry and touch nothing here
  (locations dialog, money comma, settings two-pane, translate mode,
  tree-pane zoom).

## Verdict

Safe to merge. Language packs de/es/pt follow in the same cycle.

## Deferred

None.
