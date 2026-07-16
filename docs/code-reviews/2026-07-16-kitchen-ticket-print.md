# Code review — kitchen-ticket printing + kitchen-printer routing

- Date: 2026-07-16
- Branch: `feat/kitchen-ticket-print`
- Scope: restaurant print foundation — render a kitchen ticket and route it to a
  SEPARATE printer from the customer receipt (docs:
  `docs/arch/restaurant-phone-orders.md`).

## What changed

### `internal/print`
- `kitchen.go` — new `KitchenTicket` / `KitchenItem` document model and two
  renderers:
  - `RenderKitchenTicket(KitchenTicket) []byte` — kitchen-focused ESC/POS: bold
    centred station header + `ORDER <no>`, optional order type / table /
    timestamp, then each item in **double-width** type with its modifiers/notes
    indented beneath, and a cut. **No prices, no barcode, no cash drawer** — the
    kitchen cooks food, it doesn't handle money.
  - `RenderKitchenTicketText(KitchenTicket) string` — the plain-text twin for
    on-screen/preview, mirroring the existing `RenderText` pattern.
- `transport.go` — added `Config.KitchenAddress`, `Config.KitchenEnabled()`, and
  `TransportForAddress(addr)` which auto-detects device (leading `/`) vs network
  (`host[:port]`) and reuses the existing `NewTransport`. Empty address →
  `(nil, nil)` so kitchen printing is simply off.
- `kitchen_test.go` — structure (init/cut, order no, `2 x` qty rows, modifiers,
  order type), a no-prices assertion, and the text-twin mirror + no-control-byte
  check.

### `internal/pages`
- `kitchen_print.go` (new file, to minimise merge conflicts):
  - `buildKitchenTicket` loads a sale via the existing
    `data.POSRepo.GetSaleDetail` (read-only; no repo changes) and maps lines to
    kitchen items. Table / order type are left blank — the sale model does not
    carry them yet (optional fields, ready for the hospitality order model).
  - `printKitchen` / `printKitchenAsync` — best-effort send mirroring
    `printReceiptAsync`; failures are audited (`kitchen_print_failed`), never
    block.
  - `POST /api/print/kitchen` — any operator (matches the labels endpoint gate),
    takes `receipt_no`, renders + sends, audits `kitchen_printed`.
- `print_api.go` — `printer.kitchen_addr` setting: new `keyPrinterKitchen`
  constant, read in `printerConfig`, saved by the existing
  `POST /api/settings/printer` handler (`kitchenAddr` form field).
- `pos_api.go` — one line after `printReceiptAsync` at tender: `printKitchenAsync`
  so a completed sale also prints a kitchen ticket when a kitchen printer is set
  (no-op otherwise).
- `init.go` — registers `registerKitchenPrintAPI`.

### UI + i18n
- `web/ui/pages/settings.html` — kitchen-printer address input in the printer
  card (`settings.printer.kitchen_addr` + `.kitchen_help`).
- `web/locales/{en,tr,fa,ar}.json` — 6 new keys each (settings labels +
  `kitchen.print.done/failed/off/no_receipt`). All locales match en.json.

## Notes / decisions
- Printed-paper labels (`KITCHEN`, `ORDER`) stay latin constants, consistent
  with the receipt renderer's `Receipt`/`TOTAL`/`*** REFUND ***` (receipts print
  latin; RTL bitmap mode is a separate spec item). i18n applies to the UI
  strings, which are all translated.
- Kitchen printing is off until an address is configured; the sale flow and the
  manual endpoint both no-op safely when unconfigured — offline-first / never
  blocks checkout is preserved.
- Untouched per hard constraints: `internal/catimport`, `import_page.go`,
  `reports_page.go`, `internal/data/pos_repo.go` (read-only use only), `eod*`.

## Verification
- `go build ./...` — clean.
- `go test ./...` — all packages pass (new kitchen tests included).
- `bash scripts/ci/guard-i18n.sh` — pass (all locales match en.json).
- `bash scripts/ci/guard-data-access.sh` — pass (no inline SQL outside data/db).
