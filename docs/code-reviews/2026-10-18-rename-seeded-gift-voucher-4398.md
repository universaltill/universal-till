# Review: rename the seeded gift voucher item when its language pack arrives (ut-docs#4398)

Date: 2026-10-18 · Branch `fix/4398-rename-seeded-gift-voucher` · Card complexity: medium
Built by the lane:cloud-41 session (Opus 5.5). Reviewed independently by a Sonnet subagent (fresh context): the Fable review was refused (usage limit), so MODEL-ROUTING's Sonnet fallback was used.

## What shipped
- `internal/data/gift_voucher_item.go`: `CatalogRepo.RenameSeededGiftVoucherItem(ctx, from, to)`.
  It runs in one write transaction and renames only while all of these hold:
  - the #4399 marker is set and the seeder wrote `from`;
  - the item still carries `from`;
  - it is the shop's only gift voucher item.

  Otherwise it does nothing. The seeded-name marker follows the rename, so
  `RetireSeededGiftVoucherItem` (import / cloud pull) still sees the item as
  untouched.
- `internal/pages/gift_voucher_rename.go`: `seededGiftVoucherRenamer` decorates
  the `plugins.Localizer`. `Manager.syncLocales` publishes pack strings through
  it at boot and on every plugin Reload, including the base-plugin background
  retry. After forwarding, it renames from `T("en")` to `T(shop locale)`. It
  skips an additional till (`sync.primary_url` set), whose catalogue comes from
  its main till. It is wired in `internal/pages/init.go`.
- `setup_page.go`: shares the `giftVoucherItemNameI18n` key and its comment
  points at the rename.
- Help `web/help/{en,de,tr,ar,fa}/vouchers.md`: the item takes the shop's
  language once it is installed, unless the owner renamed it. The `make
  docs-shots` re-run changed only the manifest topic hashes; the screenshots
  are byte-identical.

## Findings
- **Major, fixed:** the const `giftVoucherItemNameKey` ended in `Key`, so
  `TestSettingScope_EveryUsedKeyIsClassified` read the i18n key as an
  unclassified settings key. Renamed to `giftVoucherItemNameI18n`, now shared
  with the seeder. `go test ./internal/data ./internal/pages` is green.
- **Minor, accepted:** no test covers the `init.go` wiring itself. The
  decorator is tested directly, and the driven run below exercises the real
  boot path.
- **Minor, accepted:** the rename follows the shop's current language. An
  English shop that switches to an installed language gets the untouched item
  renamed at the next boot or plugin reload. That is consistent with "the
  untouched seed speaks the shop's language".
- **Minor, accepted:** the rename runs inside `Reload` under `PluginMu`.
  `Reload` already reads and writes the same DB (`SyncPluginPaymentMethods`),
  no caller holds a transaction, and a 5 s context bounds it.
- **Nit, accepted:** the help doesn't spell out the skip cases (a second
  voucher item, an additional till).

## Verified beyond unit tests
- TDD: the data tests failed first (method undefined) and the pages test
  failed first (decorator undefined). The reviewer re-verified this
  independently: with the rename stubbed, `RenamesWhenThePackArrives` fails
  with `"Gift voucher"`.
- Driven run on the real binary, with a DB holding an English-seeded item and
  `store.locale=de`:
  - no pack: the name is kept;
  - with a German overlay (`UT_TEST_I18N_OVERLAY_DIR`): renamed to
    "Geschenkgutschein", the marker updated, and the log line written;
  - after an owner rename: left alone.
- e2e: the gift-voucher-item-4214, vouchers-page-4251 and voucher-top-up-4252
  specs pass (10/10).
- Gate: gofmt, `go vet ./...`, golangci-lint (0 issues), the CI build-job
  guards and docs-shots guard pass. `go test`: data and pages are green.
  `internal/plugins` hit the 600 s default timeout in this container under
  load; the change doesn't touch it, and CI runs it in its own job.

## Verdict
Safe to merge.
