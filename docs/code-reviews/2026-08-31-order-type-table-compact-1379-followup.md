# Code review — shrink order-type segments + table button (ut-docs#1379 follow-up)

- **Date:** 2026-08-31
- **Branch:** `fix/1379-compact-order-type-and-table-buttons`
- **Reviewer:** self-reviewed inline (small, mechanical sizing change on
  the pattern already reviewed in the same day's prior commit; visually
  verified live)
- **Verdict:** Safe to merge.

## Context

Same-day follow-up to the two-segment order-type control just shipped.
Product owner, live: "These buttons are too big" / "The table button is
next to the dine in takeaway buttons, they should be small."

## What changed

- `.order-type-option`'s `min-height` dropped from `3rem` to `2.5rem`
  (matching `.btn.compact`'s own established floor), padding/font-size
  reduced to match visually.
- `table_picker.html`'s trigger button gets the actual `compact` class
  added (`class="btn secondary compact table-picker-trigger"`) instead of
  a hand-rolled height — reuses the tested rule rather than inventing a
  new number, and the redundant `min-height: 3rem` on `.table-picker-trigger`
  itself is removed (the class now supplies it).
- `make docs-shots` regenerated (sell screen changed again).

## Review notes

- **Why 2.5rem and not something smaller**: this is still a real touch
  target for a control that decides VAT (§12 UStG dine-in/takeaway) —
  `.btn.compact` is the app's own existing "smaller but still deliberate"
  floor (above WCAG 2.2 AA 2.5.5's 24px), used elsewhere for exactly this
  kind of judgment call. Not a new, unreviewed number.
- **Visual consistency, not independent shrinking**: the table button
  reuses `.compact` rather than getting its own bespoke smaller size, so
  both controls sharing `.order-type-row` end up the same height — the
  product owner's report was specifically about the mismatch/bulk of both
  together, not one in isolation.
- **Live-verified**: booted a real till, added a real table via
  `POST /api/tables` (the demo seed has none, so the table button doesn't
  render at all without one — ADR-0054's soft-gate), screenshotted both
  controls side by side before and after.

## Before committing checklist

- `gofmt -l .` / `go build ./...` — clean.
- `go test ./internal/pages/... ./internal/pos/...` — clean.
- `scripts/ci/guard-docs-shots.sh` — clean after `make docs-shots`.
