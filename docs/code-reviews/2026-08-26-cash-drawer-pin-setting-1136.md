# Code review: cash-drawer kick pin becomes a setting (ut-docs#1136)

**Date:** 2026-08-26
**Author:** pipeline (Dev at Sonnet, complexity:easy)
**Reviewer:** independent fresh-context Sonnet subagent
**Branch:** fix/1136-drawer-pin-setting

## What shipped

`internal/print/escpos.go`'s ESC/POS drawer-kick command hard-coded
connector pin 2 (`cmdKickDrawer = {0x1b,0x70,0x00,0x19,0xfa}`) with no way
to change it. A drawer wired to pin 5 silently never opens — the receipt
prints fine, no error, nothing to diagnose.

- New setting `printer.drawer_pin` (`"2"` / `"5"`, default `"2"` —
  preserves every existing install's behaviour with no migration step).
- `print.Doc` gets a `DrawerPin int` field; `Render()` emits
  `cmdKickDrawerPin5` (`{0x1b,0x70,0x01,0x19,0xfa}`) when it's `5`, else
  the original pin-2 bytes — including the zero value, so every caller
  that predates this setting keeps today's behaviour unchanged.
- `print.Config` gets the matching `DrawerPin int`; `printerConfig`
  (`internal/pages/print_api.go`) parses it via `parseDrawerPin` (anything
  but `"5"` → 2), and `buildReceiptDoc` wires `cfg.DrawerPin` onto the
  printed `Doc`.
- `POST /api/settings/printer` gains the `drawerPin` form field: empty
  (pre-existing client) defaults to `"2"`, anything other than `"2"`/`"5"`
  is rejected with 400 — same treatment as the existing `mode` field.
  Included in the elevation-prompt hidden fields and the settings audit
  payload, same pattern as `mode`/`charset`/`auto_print`.
- Settings UI: a `<select>` next to the existing mode/address fields,
  following the identical pattern; i18n keys added to all four
  `web/locales/*.json` (en/ar/fa/tr).
- Help topic (`web/help/{en,ar,fa,tr}/printing.md`) updated with a
  troubleshooting line; `make docs-shots` re-run (only the surface hash in
  `web/help/img/manifest.json` moved — no screenshot content changed).
- Cross-repo architecture doc (`ut-docs/architecture/receipt-printing.md`)
  updated to describe the pin as configurable rather than fixed.
- Tests: `TestRenderKickDrawerPin` (byte-level, all four cases: zero
  value, explicit 2, explicit 5, unrecognised value falls back to 2),
  `TestPrinterConfig_Defaults` extended, `TestPostSettingsPrinter_DrawerPin`
  (persistence + validation), `TestSettingsPage_DrawerPinFieldRendersAndSelects`
  (template round-trip: GET /settings actually shows the right option
  `selected`).

## Real driven run

Built and ran the actual server (`go run .`, `UT_AUTH=off`) against a temp
data dir: confirmed `GET /settings` renders the new field defaulting to pin
2 selected, `POST /api/settings/printer` with `drawerPin=5` returns 204 and
persists, a follow-up `GET /settings` shows pin 5 selected, and
`drawerPin=7` is rejected with 400 — not just unit-tested in isolation.

## Independent review

Fresh-context Sonnet subagent, no visibility into the implementer's
reasoning.

**Commands run, all clean:** `gofmt -l .`, `go build ./...`,
`go vet ./...`, full `go test ./...`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh`, `guard-data-access.sh`,
`guard-kiosk-engine.sh`.

**TDD claim independently re-verified**: reverted only the `Render()`
pin-branching logic (kept the `DrawerPin` field), re-ran
`TestRenderKickDrawerPin` — failed exactly as expected on the pin-5
subtest, confirming the test isn't a false pass. Restored and confirmed
green again.

**Correctness:** byte sequences match the real ESC/POS spec (`m`=0→pin2,
`m`=1→pin5). Zero-value/backward-compatibility confirmed at every layer
(`Doc.DrawerPin`, the one `KickDrawer=true` call site, the pre-existing
unchanged `TestRenderKickDrawer`). Read-side leniency (`parseDrawerPin`)
and write-side strictness (400 on garbage) are each consistent with the
existing `mode`/`charset` handling they sit beside.

**CLAUDE.md conformance:** no raw SQL added (uses `d.Settings.Get/Set`,
the established pattern); every new user-facing string routed through
`{{ T "…" }}` with matching keys in all four locales; help topic updated
in the same branch; `README.md` unaffected (no setup/feature claim there
depends on this).

## Findings — two nits raised, both addressed before merge

1. Cross-repo architecture doc (`ut-docs/architecture/receipt-printing.md`)
   still described the kick as a fixed pin-2 pulse — **fixed**, doc now
   describes the setting.
2. No render-level test proved the `<select>`'s `selected` attribute
   actually flips in `GET /settings` output (only byte-level/handler tests
   existed) — **fixed**, added
   `TestSettingsPage_DrawerPinFieldRendersAndSelects`, following the exact
   precedent of `TestWindowModeEndpoint`/`TestSettingsPage_TillRegisterPickerRendersAndSelects`.

## Verified beyond automated tests

- Reviewer's own revert-then-restore TDD re-verification (above).
- Real server run (see "Real driven run" above) — not just unit tests in
  isolation.

## Safe-to-merge verdict

**Yes**, merged as-is after addressing both nits above. No blockers.

## Explicitly deferred

- Pulse-duration bytes (currently fixed at 25ms/250ms) — noted as
  out-of-scope on the ticket itself; some drawers may want a longer pulse
  to latch reliably. Left for a future card if a real report surfaces it.
