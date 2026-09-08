# Code review — sell without tracking stock (ut-docs#1843)

- **Branch:** `fix/1843-sell-without-stock`
- **Date:** 2026-09-08
- **Lane:** `lane:local`
- **Reviewer:** independent subagent on a different model (Sonnet), given the
  staged diff, told to build, test and mutation-test it itself.

## What was reviewed

The new `POST /api/settings/allow-negative-inventory` endpoint and its
Settings card, `common.KeyAllowNegativeInventory`, the `catimport`
`Track inventory? (Yes/No)` column support (`TracksStock`/`HasTracksStock`,
`parseYesNo`), the `import_page.go` switch arm that refuses to record an
opening stock movement for an item the source says is untracked, and the
i18n/help-topic edits across en/ar/fa/tr (+ de/es packs, reviewed in their
own repos).

## Findings

**None.** Every category checked out against the precedent it was modelled
on. The specific things confirmed rather than assumed:

- **SaveState-then-SetState is required here, and is present.** Unlike
  `catalog-import-barcode-default` — which correctly skips `SetState`
  because its key is never reflected in `RuntimeState` — this key is read
  live at sale time (`pos_api.go:1411`, `self_order_shop.go:343`), so
  without `SetState` a merchant would tick the box and still be refused at
  the till until the next restart. ut-docs#157's persist-before-commit
  ordering is respected.
- **Nothing else can clobber the key.** `common.SaveState`'s `kv` map does
  persist it, `/api/settings/save` explicitly excludes
  `AllowNegativeInventory` from its round-trip (the ut-docs#178 class of
  bug its own comment warns about), and the raw key/value upsert only ever
  writes the one key an operator submits.
- **No header-synonym collision.** `headerIndex` matches on exact equality
  plus a `" ["` prefix rule — never substring — and the new `track_stock`
  synonyms share no literal with `stock`'s list. `Track stock`, `Stock`,
  `Stock control` and `In stock` each map to exactly one field.
  `stripTrailingParen` reduces `"Track inventory? (Yes/No)"` to
  `"track inventory?"`, which is listed explicitly, so pass 2 matches it by
  design rather than by luck.
- **Files without the column are provably unaffected** — `HasTracksStock`
  stays false, the new case never matches.
- **Both elevation summary keys exist in all four shipped locales**, and
  `import.status.stock_not_tracked` carries exactly one `%g` in every
  translation (checked programmatically, not by eye).
- Repo CI guards run clean: `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `gofmt -l`, `go vet`.

## Test-quality verification (not taken on trust)

The reviewer mutation-tested in a scratch copy, reverting four independent
pieces of the implementation:

| mutation | caught by |
|---|---|
| `parseYesNo`'s "No" case collapsed to `true` | `TestParse_TrackInventoryColumn` |
| the new `import_page.go` switch arm deleted | `TestImport_TrackInventoryNoDoesNotCarryStock` |
| `d.SetState(st)` removed after `SaveState` | `TestAllowNegativeInventoryEndpoint` |
| the elevation gate short-circuited | `TestAllowNegativeInventoryEndpoint` (cashier assertion) |

All four are load-bearing. The working tree was confirmed unchanged
afterwards.

## Driven run (Tester gate) — the merchant's actual failure, reproduced then fixed

A green test suite would have passed on the broken product too: the defect
was a complete backend with no way to reach it. So this was driven end to
end against a real built binary on `127.0.0.1:8097` with a throwaway DB:

1. Imported a SumUp-shaped CSV (`Track inventory? (Yes/No)` = `No`) through
   `POST /api/import` — 2 items created, **0 stock movements**, inventory
   rows at 0.
2. Scanned one into the basket and tendered → **"Not enough stock to
   complete this sale"**, `sales = 0`. Server log:
   `tender rejected: insufficient stock for item … (have 0.00, need 1.00)`.
   This is the pilot merchant's exact reported symptom.
3. `POST /api/settings/allow-negative-inventory enabled=true` → `204`,
   `pos.allow_negative_inventory = true` in the DB, and `GET /settings`
   renders the checkbox `checked`.
4. Tendered again → sale completed, receipt `000000001`, `sales = 1`.

The test server was stopped and the port confirmed free afterwards.

## Decisions recorded (acceptance criterion: "state what was decided")

- **Low-stock warnings need no change.** `GetLowStockItems`
  (`pos_repo.go:663`) filters on `reorder_level > 0`, and imports never set
  a reorder level — so the "116 items running out" alarm the card feared
  cannot fire. That is true today by construction, not by tuning.
- **The Inventory screen does get ugly, and that is why #1850 exists.**
  With the shop-wide switch on, sales still record stock movements, so an
  item the merchant never stocked drifts negative — verified live at
  `-2.0`. Step 1 is shipped anyway because it unblocks him now; per-item
  tracking (`ut-docs#1850`, filed with this card) is the correct model and
  says so explicitly.
- **`AllowNegativeInventory` was deliberately NOT overloaded to mean "do
  not create a stock row".** Four existing call sites pass it `true` for
  reasons unrelated to stock policy (`sync_sales.go:309`,
  `refund_page.go:871`, `inventory_api.go:590`, and the replica rule at
  `pos_api.go:1415`). Keying row-creation off it would silently change
  behaviour for shops that *do* track stock.
- **The card's premise that untracked items have "no inventory row at all"
  is not true on the import path** — `CreateItemTx` calls
  `ensureInventoryRowExec` for every item, so they get a row at 0. The
  symptom is identical either way (`0 - 1 < 0` fails the same guard), and
  the test now asserts the *quantity*, which is what actually encodes the
  promise. Both shapes are covered:
  `TestCompleteSale_NegativeInventoryGuardSemanticsPreserved` gained an
  absent-row case, since absent and zero are different `CurrentQtyBatch`
  paths and only one was ever asserted.
