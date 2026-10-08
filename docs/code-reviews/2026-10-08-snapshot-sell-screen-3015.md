# 2026-10-08 — Catalog snapshot carries each item's sell-screen state (ut-docs#3015)

**Scope (scoped down by BA/Architect, lane:cloud-41):** the data path only.
The till's catalog snapshot (contract §3.7) reports `"sell_screen": "hidden" | "removed"`.
The field is omitted when the item is visible. ut-cloud stores it as `StoreCatalogItem.sell_screen`
(default `"visible"`) and returns it on `GET …/items` and `GET …/items/{id}`
(`ItemRow.sell_screen`, §2.2). The my. badge and the restore action are split to ut-docs#3916.

## What shipped
- **universal-till** (`feat/3015-snapshot-sell-screen`): `CatalogSnapshotItems` reads
  `sell_screen_hidden` and `sell_screen_removed` in the existing items query, so there is no extra query.
  When both are set, removed wins, as in `SellScreenStates`. `snapshotItemRow.SellScreen` is
  `omitempty`. Its tests are `TestCatalogSnapshotItemsSellScreen` and `TestSnapshotItemSellScreen`.
- **ut-cloud** (`feat/3015-catalog-sell-screen`): adds the ent field and regenerated code. The ingest
  (`reportedSellScreen`) is strict: anything other than hidden or removed reads as visible. The
  change also covers `rowFromEnt` (cloud rows re-saved keep the last reported state), `CatalogItem.SellScreen`
  and `appItemRow.sell_screen`. Its tests are `TestNormalizeSnapshotItemSellScreen`,
  `TestIngestCatalogSnapshotStoresSellScreen` and `TestAppItemRowSellScreen`.
- **ut-docs** (`docs/3015-sell-screen-contract`): `reference/manage-shop-catalog-api.md` §2.2, §2.3 and §3.7.

## Review
Independent review by Fable (the author was Opus 5.5).
- No blockers and no should-fix findings.
- nit, fixed: §2.3 did not name `sell_screen` alongside its sibling fields.
- nit, fixed: the schema-1 test now asserts that the early return leaves the state unset (`""`).
  Before, it accepted either `""` or `"visible"`.
- Hunt results: every `StoreCatalogItem` write goes through `catalogRow.create`: till ingest, cloud
  create (`saveCloudRow`/`rowFromEnt`), bulk and import. The `UpdateOne` paths do not touch the field.
  Because the field is omitempty, a till with no flagged items pushes byte-identical payloads, so there
  is no extra push. Schema-1 rows read visible. Removed-wins matches the till.

## Verification
- **TDD re-verified by the reviewer in detached worktrees.** Reverting each production line on its own
  (the cloudsync row field, the repo switch, the ingest line, the read mapping, `toAppItemRow`) made
  its test fail with an assertion error. Restoring the line made it pass.
- **universal-till:** `go build ./...` and the full `go test ./...` pass. Every guard in `ci.yml`
  passes except four that are environment-only:
  - deadcode, twice: the local x/tools build reports go1.26 against go1.27 sources. The diff adds no
    functions.
  - `guard-shellcheck-version`: shellcheck is not installed.
  - `retry-with-backoff.sh`: a helper that needs arguments, not a guard.
- **ut-cloud:** `scripts/ci/verify.sh` passes: gofmt, vet, golangci-lint with 0 issues, all tests,
  and the residency, card-data and contract guards. `go generate` leaves no diff.
- **Not done:** a live run of a till pushing to a real cloud. The httptest fake cloud drives the real
  `pushSnapshotIfChanged`, and the cloud test drives the real ent ingest and read. No UI surface is
  touched, so there was nothing to look at.

**Deploy order:** none required. An old cloud ignores the new key (the snapshot decode is not strict).
A new cloud reads an absent key as visible.

**Verdict: safe to merge.**
