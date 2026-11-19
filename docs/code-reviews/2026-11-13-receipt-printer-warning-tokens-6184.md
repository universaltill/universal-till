# Review — receipt "Printer unavailable" block follows the theme (ut-docs#6184)

Date: 2026-11-13 · Lane: lane:cloud-41 · Complexity: easy · Built by Sonnet 5.5, reviewed by Opus 5.5 (fresh context, separate worktree).

## What shipped

The on-screen receipt's printer warning (`.receipt-printer-warning`, `web/ui/partials/receipt.html`) hard-coded a light orange (`#fff7ed` / `#fdba74` / `#9a3412`), so on the dark theme it was a bright slab. It now uses the existing warning tokens, the same triple as `.notice-block-warn` and `.tag.warn`: `background: var(--warning-tint)`, `border: 1px solid var(--warning-tint-border)`, `color: var(--warning-text)`. The themes already retune those tokens (dark: amber text; monochrome: black on grey). No theme token changed; the `@media print` hide rule is untouched.

- Go: `internal/pages/receipt_printer_warning_tokens_6184_test.go` renders a printerless tender receipt and fails if the rule holds a hex/rgb/hsl literal or lacks the three tokens.
- e2e: `e2e/tests/receipt-printer-warning-theme-6184.spec.ts` (light and dark): computed colours equal the theme tokens and are not the old literals, the dark run proves the dark theme loaded, text contrast >= 4.5:1 against the tint composited over the card, and the block stays hidden in print media.
- `e2e/tests/receipt-theme-contrast-4419.spec.ts`: the receipt sweep no longer skips the printer warning (it follows the theme now).

Contrast of `--warning-text` on the tint over the card: default/amber/fresh/monarch/slate 6.24:1, dark 4.83:1, monochrome 17.55:1.

No new `en.json` keys; logical CSS only; no ADR; no help change (colour only, no flow or text change).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | low | The dark e2e run compared colours to token probes resolved under whatever theme was active, so a dark theme that silently failed to load would re-test light and pass. | Fixed: dark run asserts the text is the dark theme's amber `rgb(217, 119, 6)`. |
| F2 | low | Go test reads only the first `.receipt-printer-warning` rule and rejects hex/rgb/hsl, not named colours or a later override. | Accepted: the e2e token-equality check catches both in a browser. |
| F3 | nit | The 4.5:1 check alone would not catch the old literals (they have enough contrast); the not-old-literal and token-equality assertions are what turn it red. | Accepted; noted here so they are not dropped as redundant. |
| F4 | nit | The #4419 sweep still excluded the block. | Fixed: exclusion removed; sweep passes. |
| F5 | nit (pre-existing) | `pos_api.go` renders `receiptPrinterWarningHTML` without `receipt.html`'s styles when the receipt template fails, so the block shows as plain text. | Accepted: readable in every theme, not caused by this change. |

## Verification

- TDD re-verified by the Tester and independently by the reviewer in a separate worktree: with the old `receipt.html`, the Go test fails on `#fff7ed` and the missing tokens, and the e2e spec fails in light and dark (`Expected: not "rgb(255, 247, 237)"`); restored, both pass.
- After F1/F4: the 6184 spec plus the 4419 spec, 14 passed (Chromium).
- Tester driven run on the e2e till, screenshots looked at: light and dark at 1024×600, dark at 360×800, dark `fa` (RTL mirrors: text on the right, Retry on the left). Light reads as a pale warm tint with brown text; dark as a dark amber tint with amber border and text. The Retry button is unchanged (about 128×51 px).
- `gofmt -l .` clean, `go vet ./internal/pages/`, `go test ./internal/pages/...` all ok; guards run clean: rtl-logical-css, fixed-dates, i18n, i18n-meta, compliance-claims, competitor-naming, no-inline-handlers, no-showmodal, no-native-confirm, htmx-loaded, mail-addresses, e2e-fixtures-import, e2e-no-browser, e2e-no-lan-discovery, e2e-no-update-check, help-topics, autofill-suppression, emoji-font, osk-loaded, page-http-error, core-neutral. Not run locally: full `go test ./...`, golangci-lint (no new non-test Go), shellcheck (no shell changes); CI covers them.

Pre-flight: help topic n/a (colour only, no flow/text change); reference/ n/a; README n/a; en.json/meta n/a (no keys); ADR n/a; mirrors n/a; review record this file; follow-ups none filed (F2/F5 accepted).

Verdict: approve.
