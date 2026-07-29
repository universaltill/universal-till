# Test coverage batch 13: print API handlers

2026-07-29

`internal/pages/print_api.go` — receipt content assembly
(`buildReceiptDoc`), printer/receipt-design settings parsing, and the
four HTTP handlers (`POST /api/settings/printer`, `/api/print/test`,
`/api/print/labels`, `/api/print/receipt/{receiptNo}`).

## What's covered

- `printerConfig`/`receiptDesignFromSettings` defaults.
- `receiptLogoRaster`'s no-file-present degrade-to-nil path, specifically
  exercising the CURRENT `paths.Data(...)`-based `receiptLogoPath()` (the
  fix from earlier this session that stopped uploaded receipt logos being
  lost on every app self-update), not a stale cwd-relative assumption.
- `buildReceiptDoc`: normal sale, a refund correctly prepending a REFUND
  marker + the original receipt number, tip/change payment lines with
  their exact printed amounts, unknown-receipt error.
- All four handlers' manager-gating, validation, and clean-failure (502,
  not a hang/panic) paths when no printer is configured.

## Independent review (opus) — two real, cheap strengthenings applied

The review specifically scrutinized the money-adjacent receipt-content
tests and found two genuine weaknesses, both fixed before commit:

1. The tip/change test asserted only that a payment line with the right
   *label* existed, never the *amount* — a regression that printed the
   wrong minor-unit value (or swapped tip/change) would have passed.
   Strengthened to assert the exact printed amounts (`£1.50`/`£2.00`).
2. The copies-clamping test (`0`/`-5`/`999` → item-not-found) only proved
   nothing crashes before validation, not that the values actually clamp
   to `1`/`1`/`50` — the real clamped value was only observable on a
   print-success path unreachable without real transport hardware.
   Extracted the two `if` blocks into a standalone `clampCopies(n int)
   int` function and added a direct table test covering the boundaries
   (`0→1`, `-5→1`, `1→1`, `50→50`, `51→50`, `999→50`, `3→3`).

Everything else — the REFUND-marker/original-receipt logic, the
charset-fallback and mode-persistence logic, the no-printer 502 paths —
was verified line-for-line against production and confirmed correct as
originally written.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass (including after the
`clampCopies` extraction).
