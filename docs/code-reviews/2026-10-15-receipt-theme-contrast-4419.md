# Review: on-screen receipt reads in every theme (ut-docs#4419)

**Date:** 2026-10-15 · **Lane:** `lane:cloud-24` · **Complexity:** easy

## What shipped

- `web/ui/partials/receipt.html`: the receipt card took a fixed `#fff`
  background while its text partly inherited the theme's colours, so in the
  built-in dark theme (`--text: #e5e7eb`) the Payments heading, the payment
  method, totals, "Thank you!" and the voucher block were light grey on
  white (contrast 1.24:1). The card and its secondary text now use the
  theme's tokens (`--surface`, `--text`, `--border`, `--muted`) instead of
  fixed hex colours. Light themes are visually unchanged (their `--surface`
  is white). The barcodes keep their own white backing
  (`internal/httpx/barcode.go`), so they stay scannable on a dark card.
- `@media print` pins paper colours — white card, black text, transparent
  totals well — so a dark theme never prints light text.
- `e2e/tests/receipt-theme-contrast-4419.spec.ts`: after a real tender,
  measures the WCAG contrast of every text-owning element in the receipt
  card against the first opaque background behind it (≥ 4.5:1), at
  1024×600 and 360×800, `en` and `fa` (RTL), light, dark and a dark theme plugin
  that leaves app.css's `h2` colour alone, then again under print media.

## TDD

Spec written first: the 4 dark cases failed (every text at 1.24:1), the 4
light cases passed. With the fix: 8/8 pass. Adding the print assertion
caught the totals well keeping the dark `--surface-2` in print — fixed.

## Review

Independent review on a different model (Fable; author Opus 5.5), in a
throwaway worktree: 8/8 passed on the fix, the 4 dark cases failed with
`receipt.html` reverted (9 elements at 1.24:1), and every built-in theme
(monarch/slate/fresh/amber/monochrome/dark) measured >= 11.8:1.

1. **Medium — fixed.** The receipt `<h2>`s (store name, "Receipt #N") still
   took app.css's `h2 { color: var(--brand) }`; built-in `dark.css` happens
   to re-point `h2`, but a theme plugin with a dark `--surface` and dark
   `--brand` that doesn't would show them at 1.01:1. Before this change the
   white card hid that. Fix: `.receipt-card h2 { color: inherit; }`. The
   spec gained a `dark-plugin` case (dark.css plus app.css's `h2` rule
   re-applied): red without the fix, green with it — 12/12.
2. **Low — accepted.** Coverage is a plain cash sale: card details,
   change/tip, issued vouchers, legal/TSE, tracking caption and SKU lines
   aren't rendered by the spec. Their colours are tokens (`--text`,
   `--muted`; tightest is amber's `--muted` ~4.8:1 on white), checked by
   reading the CSS.
3. **Nit — accepted.** The spec swaps `#theme-css`'s href instead of the
   real setting (same technique as `popup-scrim-2873`); the theme-sync poll
   can't revert it.

Also checked OK: print `color:#000 !important` touches text only (barcodes
use SVG `fill`, QR images are opaque PNGs); printer warning keeps its own
fixed colours (7.5:1); RTL uses logical/vertical properties only; no new
strings, no `showModal`, no network dependency.

## Verified beyond automated tests

Driven run screenshots (1024×600 and 360×800, en/fa, light/dark) checked by
eye: the dark card reads as the rest of the dark sale screen, barcode on its
white strip, light theme unchanged, RTL mirrors correctly.

## Gates

`gofmt`, `go build ./...`, `go test ./internal/pages/ ./internal/httpx/`,
the `ci.yml` build-job guards; e2e: the new spec plus
`phone-sell-3059` and `gift-voucher-item-4214` (receipt flows) — 29/29.
Two guards could not run in this container and are unaffected by an
HTML/TS-only diff: `guard-deadcode-baseline.sh` (deadcode binary built with
an older Go) and `guard-shellcheck-version.sh` (no shellcheck installed);
CI runs both.

First CI run went red on `guard-docs-shots_test.sh`: `receipt.html` is on
the manual's screenshot surface. The post-tender receipt is rendered only
by `pos_api.go` after a tender, and no `docs-shots.spec.ts` shot takes a
tender (its only "receipt" is the designer's own preview), so no shot's
pixels change: refreshed `surface_sha256` with
`update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`). Every
`ci.yml` guard self-test then passed locally except the deadcode one
(container's Go, as above).

## Verdict

Safe to merge. No deferred items beyond the accepted low/nit above.
