# Code review — open sale screen refreshes on catalog changes elsewhere (ut-docs#2765)

Date: 2026-09-26 · Lane: cloud-54 · Built by Opus 5.5 (dev subagent) · Reviewed by Fable (independent subagent, worktree)

## What shipped
- Migration `047_sell_screen_version_catalog.sql`: INSERT/UPDATE/DELETE triggers on the tile grid's
  catalog tables (items, categories, shortcut_buttons, item_barcodes, item_variants, variant_barcodes,
  item_modifier_groups, item_modifier_group_links, category_modifier_group_links,
  item_modifier_group_opt_outs, translation_overrides) bump `sell_screen_version.generation`, which
  042 already bumps for price_history/item_images. The counter is now catalog-only: a completed sale,
  a settings write or inventory never moves it. All listed tables were already admin tables, so the
  #2501 tile cache invalidates exactly as before.
- `GET /ui/buttons/version` → `{"data":{"version":N},"error":null}`, one single-row query, `no-store`,
  same session gating as `/ui/buttons`, on the demo allow-list.
- `/ui/buttons` sends `X-UT-Sell-Version` on cached and fresh renders (set before the cache lookup,
  so a racing write can only make the header older than the content → one redundant refresh, never a
  missed one).
- `web/public/sell-screen-watch.js` (shell-head script, `httpx.HeadAssets`): polls every 5 s while
  visible, fires `buttons-changed` when the version differs from the grid's; waits while editing /
  jiggle / drag / any open `<dialog>` / search / pointer down. Local-only, failures silent, never
  blocks checkout.
- Help `sell.md` (en/de/fa/ar/tr): one sentence on the automatic refresh.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | minor | Replica tills: a non-catalog admin write on the primary makes `ApplyAdmin` re-upsert every admin row unconditionally, so triggers fire on unchanged rows and each replica's open grid refreshes once. | Follow-up card (skip no-op upserts in `execUpsertBatch`). |
| F2 | minor | Expired session: the probe followed 303 → /login and fetched HTML every 5 s. | Fixed: `redirect: 'manual'`. |
| F3 | minor | A refresh resets the grid scroll position. | Follow-up card. |
| F4 | minor | A persistently failing `/ui/buttons` render was retried every 5 s. | Fixed: a failed render keeps `refreshAt`, retry waits 15 s. |
| F5 | nit | `web/help/img/manifest.json` hashes changed with no PNG change. | `make docs-shots` was run; the regenerated PNGs differed only by container fonts / environment, so PNGs were restored and only the manifest kept. The JS fix after review changes no pixel → `update-docs-shots-surface-hash.sh`. |
| F6 | nit | Settings that change the grid (browsing mode, barcode symbologies) do not live-refresh. | Accepted, documented in 047's header; out of scope. |

## Verified
- TDD re-verified by the reviewer in a worktree: 047 as no-op → `TestSellScreenVersion_CatalogWritesBump`
  and `TestButtonsVersion_ShapeAndMovesOnCatalogDeactivate` fail; route removed → 404 failures; restored → pass.
- `TestButtonsVersion_CompletedSaleDoesNotMoveIt` drives the real scan + tender flow.
- gofmt, build, vet, `go test ./...`, golangci-lint (0), shellcheck, all ci.yml build-job guards.
- Playwright: new `sell-screen-live-refresh-2765` (two browser contexts: deactivate on B → tile gone on A
  within 12 s with no navigation; a sale on B causes no refetch; refresh waits for an open search) and
  12 neighbouring sale-screen/designer specs (47/47); re-run after the review fixes (4/4).
- Not looked at: RTL/dark theme (no new visual surface); real touch hardware (pointer-down deferral
  emulated only).

## Verdict
Safe to merge.
