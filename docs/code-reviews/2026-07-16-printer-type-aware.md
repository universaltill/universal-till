# Code review — printer-type-aware printing (thermal vs regular)

Date: 2026-07-16
Branch: `feat/printer-type-system`

## Why
Farshid has no thermal printer and uses an HP; the till sent ESC/POS bytes, so
the HP printed garbage / each line separately.

## What
- New printer mode **"system"**: `print.PrintDoc(ctx, cfg, doc)` renders a plain-
  TEXT layout (`RenderText`, already existed) and pipes it to CUPS `lp` — right
  for a regular office/HP printer. Optional printer name via Address; empty =
  system default. Thermal (network/device) keeps the ESC/POS path.
- `PrintDoc` is now the single entry point; migrated the receipt, test-print,
  EOD (auto + reprint) and invoice call sites to it (was NewTransport+Render).
- Settings: "Regular printer (system / HP, plain text)" mode option + a hint;
  `mode` validation accepts "system"; `Config.Enabled()` includes it. i18n x4.

## Tests
`TestSystemRenderIsPlainText` (system output has no ESC/GS control bytes and
carries the content), `TestPrintDocOffIsNoop`. Existing print tests pass.
(`lp` delivery itself needs a real printer — not exercised in CI.)

## Note
Kitchen tickets still use the thermal path (kitchen printers are thermal).
`lp` is macOS/Linux (CUPS); Windows regular-printing is a follow-up.

## Checks
`go build ./...`, `go test ./...`, i18n + data-access guards — green.
