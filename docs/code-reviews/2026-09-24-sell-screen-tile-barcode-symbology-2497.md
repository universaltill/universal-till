# Sell-screen/kiosk tile barcode-symbology fallback (ut-docs#2497)

## What shipped

Bug: on the sell screen's All tab and search, tapping some active,
listed items (e.g. "Banana bread") showed "item does not exist" instead
of adding them to the sale.

Root cause: `ui.ButtonStore.LoadAllActive` and `ui.ButtonStore.SearchSellable`
set a tile's `Code` to the item's raw catalog barcode whenever one existed,
with no check that the barcode actually decodes under the shop's
*currently enabled* barcode symbologies (a per-shop setting, ADR-0059).
Tapping the tile posts that `Code` to `/api/pos/scan`, whose resolver
(`data.POSRepo.ResolveShortcutLineDecoded` → `ResolveScanLine` →
`internal/barcode.Registry.Match`) only accepts a raw-barcode-shaped code
if it matches an enabled symbology. A barcode stored under a symbology the
shop doesn't currently have enabled (e.g. import predates a settings
change) matched none of the resolver's tiers, so the tap failed even
though the item was clearly active and listed.

Fix: both functions now use the raw barcode as the tile `Code` only if it
decodes under the shop's enabled symbologies (the same test
`/api/pos/scan` applies); otherwise they fall through to the item's SKU,
then to the existing synthesized `item:<id>` code — the same fallback that
already applied when there was no barcode at all. A new shared helper,
`resolvableTileCode`, centralizes this for both call sites.

`ui.ButtonStore.Load()` (Designer quick buttons) was deliberately left
untouched: its `Code` comes from the `shortcut_buttons.barcode` column,
which the resolver's tier-2 `resolveShortcut` matches unconditionally (no
symbology check), so it was never exposed to this bug.

**Independent review (round 1) found the self-order kiosk grid
(`internal/pages/self_order_shop.go`'s `loadShopItems`) had the identical
exposure** — same raw-barcode-preferred `Code` selection, same
`ResolveShortcutLineDecoded` resolver behind `/api/self-order/scan`. Fixed
in the same PR with the same pattern (only the kiosk has no `item:`
synthesized tier, so an unresolvable barcode with no SKU falls through to
the pre-existing "skip this tile" behavior, unchanged from before this fix
for that specific no-barcode-no-SKU case).

## Review findings (independent Opus review, isolated worktree)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Kiosk grid (`self_order_shop.go`) has the same bug | **Fixed** in this PR (same pattern, own regression test + real handler round-trip test) |
| 2 | should-fix | New comment claimed a settings-read error falls back to "no enabled symbologies"; it actually falls back to `DefaultEnabledBarcodeSymbologyIDs()`, matching what the resolver itself does on the same error | **Fixed** — comment corrected |
| 3 | nit | `resolvableTileCode`'s doc comment overclaimed the SKU fallback tier resolves "unconditionally" (pre-existing behavior for any item with no barcode; a SKU can collide with a customer/loyalty/voucher prefix or another item's own barcode) | **Fixed** — comment softened to describe the real behavior |
| 4 | nit | No test for the middle branch (unresolvable barcode + a SKU present → falls back to SKU, not to `item:<id>`); no `SearchSellable` control test | **Fixed** — added `TestButtonStoreLoadAllActive_UnresolvableBarcodeFallsBackToSKU` and `TestButtonStoreSearchSellable_ResolvableBarcodeSymbologyUnchanged` |
| 5 | nit | `ItemBarcodes` and `SearchItemsForShortcuts` order "first barcode" slightly differently (tie-break), so the All tab and search could pick a different barcode for a multi-barcode item — pre-existing, not a regression from this PR | **Accepted**, documented here, not fixed (out of scope) |
| 6 | nit | An item with a barcode but no SKU that now falls to `item:<id>` shows no code on its receipt/journal line (`PriceResolverAdapter.resolve` blanks the SKU for a synthesized code) | **Accepted** — strictly better than the pre-fix behavior (the tap used to fail outright), not a regression |

## Verified beyond automated tests

- **TDD claim re-verified personally**, twice (once for the cashier fix,
  once for the kiosk fix added during review): reverted each fix, ran its
  new tests, confirmed they fail with the exact predicted symptom (`Code =
  "PLU-CATALOG-9001", must NOT be the raw barcode`), restored the fix,
  confirmed all pass.
- Real handler-level round trip for the kiosk fix: `TestLoadShopItems_UnresolvableBarcodeSymbologyFallsBackToSKU`
  posts the tile's resolved `Code` through the real `/api/self-order/scan`
  mux handler (not just the repo resolver function) and asserts the item
  actually lands in the kiosk basket.
- No visual/client-side behavior changed (the `data-code` HTML attribute
  is a direct, untransformed passthrough of `.Code`, confirmed by template
  inspection) — a full browser-driven run was judged disproportionate for
  this pure server-side resolution fix; the round-trip tests against the
  real resolver function are the equivalent proof for this class of bug.
- Full `go test ./...`, `golangci-lint run ./...` (0 issues), `gofmt -l .`,
  `go vet ./...` all clean on the final diff. All CI guard scripts in
  `ci.yml`'s `build` job pass except `guard-shellcheck-version.sh`, which
  fails only on a missing `shellcheck` binary in this sandbox — no shell
  scripts were touched by this change.

## Not in scope / follow-ups

- Filed as ut-docs#2525: the sell-screen tile grid has no
  `buttons-changed`/`modifiers-changed` trigger on a catalog item
  edit/deactivate/re-import, so an already-rendered tile can go stale
  mid-session and 404 on tap for a different reason than this bug (found
  during BA investigation of this ticket, not reproduced/confirmed as the
  cause of the original report, but a real gap).
- Finding 5 above (barcode tie-break ordering divergence between
  `ItemBarcodes` and `SearchItemsForShortcuts`) — pre-existing, not
  reproduced as user-visible, not filed separately (would need its own
  repro first).

## Safe-to-merge verdict

Safe to merge. All should-fix findings addressed; nits either fixed or
explicitly accepted above.
