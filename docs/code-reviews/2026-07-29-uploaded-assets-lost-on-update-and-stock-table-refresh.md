# 2026-07-29 — Uploaded images lost on every update; stock table never refreshed

## Context
Follow-up to yesterday's catalog visibility fixes. Farshid reported three
more things live: "still no image upload, this was working previously,"
variant images "still" not working, and "inventory count is not updating."
Testing each hands-on (not just reading code) surfaced two real, separate
bugs — one of them a genuine data-loss bug, not a display glitch.

## Bug 1 (serious): uploaded images live inside the app bundle/release tree
`POST /api/catalog/item/image`, `/variant/image`, the barcode-autofill
image fetch, and the receipt logo upload all wrote to
`filepath.Join("web", "public", "assets", ...)` — a path relative to the
process's **current working directory**. For the packaged macOS `.app`,
that's `Contents/Resources/`, i.e. **inside the versioned app bundle
itself**. `applyMacApp` (self-update) replaces that whole bundle on every
update (`ditto "$NEW" "$APP"` over the old one); the archive self-update
path (`internal/selfupdate.Apply`) does the same to `web/` specifically
(`os.Rename(curWeb, webBak)` then moves the new archive's `web/` into
place). Either way, anything written under `web/public/...` is discarded
on the very next update — confirmed live: an item's uploaded photo,
present in the catalog list right after upload, was gone after installing
the release that was supposed to *fix* an unrelated display bug for it.

Root cause of "still no image upload, this was working previously": it
wasn't the upload that broke — it's that the update the user had just
installed (to get yesterday's fixes) silently deleted the previous
upload, and any new upload would meet the identical fate at the next
update.

**Fix**: `internal/paths` already exists specifically for "mutable data
that must survive version upgrades" (its own doc comment). All four write
sites now write to `paths.Data("public", "assets", ...)` instead. The
static file server (`internal/pages/static_page.go`'s `fallbackFS`) gains
a new, highest-priority tier for that stable directory, ahead of the
existing release-tree/embedded-default tiers — same fallback chain
`/public/` and theme resolution already shared, just with the missing
persistent tier added. `receiptLogoPath` changed from a `const` to a
`func()`: `paths.DataDir()` only resolves correctly after `paths.Init` has
run (during config load), which happens after Go package-level consts
would already have been evaluated.

**Migration, not just a fix going forward**: added
`paths.migrateLegacyUploadedAssets`, called from the existing
`MigrateLegacyData` (the same one-time migration hook that already moves
the DB and plugin bundles into the stable dir on first run after an
update). Copies `web/public/assets/items/**` and specifically
`web/public/assets/logo/receipt-logo.png` — **not** a blanket copy of
`web/public/assets`, which also holds built-in default icons/logo that
must stay resolvable from the embedded/release tiers so a future release
can still update them; copying those into the stable tier would freeze
them at whatever version happened to exist during this one migration.
Without this, fixing the write path would still have left every shop
needing to re-upload everything right after finally getting the fix.

## Bug 2: the stock-levels table never refreshed after a stock change
Checked the actual data first: a receive/adjust genuinely updated
`inventory.quantity` correctly (verified 10 + 15 = 25 in the real
database) — this was never a database bug. The `/inventory` page's
stock-levels table is server-rendered once, on page load, with no HTMX
trigger to look again; the receive/adjust/override/return forms all
target a small `#result` status div, never the table. A successful
submission was — correctly — invisible on screen until a full manual page
reload, reading as "inventory count is not updating."

**Fix**: extracted the table into its own partial (`stock_table.html`) and
a new `GET /ui/inventory/stock-table` endpoint (sharing the same
`stockLevelsForDisplay` helper the full page uses, so they can't drift).
The table's wrapping card gets `hx-trigger="stock-updated from:body"
hx-get="/ui/inventory/stock-table" hx-swap="innerHTML"` — same idiom the
adjacent low-stock list already used for its own `hx-trigger="load"`. The
three mutation endpoints' success responses now set `HX-Trigger:
stock-updated` (a new `writeHTMLStockChanged` wrapper around the existing
`writeHTML`), which htmx turns into a DOM event on `<body>` that both the
stock table and the low-stock list (also updated to listen) react to.

## Not a bug
Rechecked per-variant images (reported again as "no place to add it") —
the control exists (`catalog_variants.html`'s 📷 icon per row, wired to
`POST /api/catalog/variant/image`) and, once Bug 1's write-path fix
lands, writes to the correct persistent location. No separate issue found
beyond Bug 1 affecting it the same way item images were affected.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...`, both CI guard scripts,
`gofmt -l` all pass (one pre-existing, unrelated gofmt hit in
`internal/plugins/marketplace/client.go`, untouched by this change).

New tests:
- `TestMigrateLegacyUploadedAssets` (`internal/paths`) — migrates item/
  variant photos and the receipt logo, leaves built-in default assets
  alone, is idempotent.
- `TestInventoryReceiptTriggersStockTableRefresh` (`internal/pages`) —
  asserts the `HX-Trigger: stock-updated` header on a successful receipt,
  and that the new partial endpoint reflects the updated quantity.

`TestInventoryFormRender` failing when run in isolation (`-run
TestInventoryFormRender` alone) is a **pre-existing** test-isolation
artifact, not a regression from this change — confirmed by running it
both before and after this change, both times passing only as part of the
full package suite (an earlier test in the same binary happens to
initialize i18n as a side effect; run alone, template keys render as
literal `key.name` text instead of the English string the assertion
expects). Not fixed here — out of scope, and pre-dates this change.
