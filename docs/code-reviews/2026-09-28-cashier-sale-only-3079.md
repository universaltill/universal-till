# Review: the cashier is sale-only (ut-docs#3079)

**Date:** 2026-09-28 · **Branch:** `fix/3079-cashier-sale-only` · **Built by:** Opus 5.5 (Dev subagent + orchestrator) · **Reviewed by:** Fable (independent, security-focused)

## What shipped
Product owner rule (BINDING): a cashier is sale-only. Every edit, report or admin surface belongs to admin/manager or an explicitly granted permission, and is both hidden and refused by the server.

The cashier role already had no `role_permissions` rows. The bug was pages and links that never checked.

**Refused (403, with the rail kept so Sell stays reachable):**
- `/settings`
- `/reports` and `/ui/reports/tab/*`
- `/inventory` and `/ui/inventory/stock-table`
- `/plugins` and `/plugins/store`
- `POST /api/inventory/receipt` and `/override`
- Added after review: `GET /api/inventory/low-stock`, `POST /api/settings/theme`, `/api/plugins/check-updates`, `/api/plugins/marketplace`

**New permission:** migration 052 adds `stock_management` (admin/manager/super_admin), so a custom role can be granted stock alone. It's labelled in every locale.

**Hidden:**
- Menu tiles: Reports, Settings, Plugins (`VisibleIf`).
- Rail: Inventory. It's filtered per request (`InitRailVisibility`, bound in `withHelpHref`), with zero allocations when nothing is hidden.
- The sell screen's phone Inventory link.
- The sync chip's Tills link (now a non-link chip).
- Status-bar links: register this till, plugin and language-pack updates.
- The Android install button.
- The sell grid's "+ Add product" and pencil (`/designer`).

**Refusal page:** the heading reads "Not allowed" (`error.page.forbidden_title`), not "Something went wrong".

**Manual:** a "What a cashier can do" section in `web/help/*/users.md` (en/de/ar/fa/tr).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | `POST /api/settings/theme` had no gate at all | Fixed: `settings`, 403 |
| 2 | medium | `GET /api/inventory/low-stock` served the stock levels the card gates | Fixed: `stock_management` |
| 3 | low | `/api/plugins/check-updates` and `/marketplace` were ungated | Fixed: `plugin_management`. `TestPluginCatalogBrowsing_*` inverted, since the rule reverses the old "anyone may browse" |
| 4 | low | Basket-panel width, effects level, window-mode status are ungated UI preferences | Accepted: sell-screen conveniences |
| 5 | product | `POST /api/inventory/return` / `/refund` stay ungated | Accepted: returns are the sale flow (card non-goal); flagged on the card |
| 6 | nit | `requirePage` on htmx fragments returns the full 403 page | Accepted: htmx doesn't swap a 4xx |

The reviewer probed all 376 registered routes with real cashier and admin sessions through `pages.Init` and the auth middleware. Every admin surface is refused. Exports and backup download are refused; settings writes go through `checkOrElevate`. The rail checker fails closed after `Init`, and a nil checker only happens in tests. The template clone cache and the sell-screen cache don't leak per-user content (the grant is in the key).

## Deliberate behaviour changes
- **Settings forms:** a cashier no longer sees them with a PIN-elevation prompt (#866/#867 reversed by this rule).
- **Kiosk exit:** leaving self-order kiosk mode with a cashier PIN lands on the 403 page; a manager PIN is needed.
- **Denied redirects:** `/tills`, `/receipt-designer` and `/sync-quarantine` redirect a denied cashier to `/settings`, which now ends on the 403 page.

## Verified beyond automated tests
- **Driven with a real cashier PIN** on the auth till. Screenshots looked at, at 1024×600 and 360:
  - Menu: sale tiles only.
  - Rail: no Inventory.
  - Refused page: rail and Back to sale.
  - UX pass: removed "+ Add product" from the cashier's empty sell grid and retitled the refusal page.
- **TDD:**
  - The reviewer independently reverted the inventory gates and the rail filter; the tests failed, then passed once restored.
  - I reverted the grid-link and heading fixes; the tests failed, then passed once restored.
  - All four review-finding gates failed the table before the fix.
- **Commands:** `go build`, `go vet`, full `go test ./...` and gofmt are clean. The i18n, data-access, migration and docs-shots guards pass (docs-shots regenerated, 124). Playwright `auth` passes 29/29, including `sale-only-cashier-3079.spec.ts`; the grid/designer default specs pass 19/19.

## Verdict
Safe to merge.

## Deferred / follow-ups
- **Language packs:** `ut-plugin-language-de` / `-es` need `permissions.action.stock_management` and `error.page.forbidden_title` after this merges (same cycle).
- **Dynamic roles:** #2841 builds on this (Ready, `blocked:dep` on this card).
