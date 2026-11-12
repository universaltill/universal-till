# Review: quick tiles stop writing shortcut_buttons.image_path (ut-docs#6097)

**Date:** 2026-11-12 · **Branch:** `fix/6097-tile-image-path-writes` · **Lane:** `lane:cloud-54`
**Author model:** Sonnet (Dev) · **Reviewer:** Opus 5.5 (independent subagent, own worktree) · **Tester:** Sonnet (fresh subagent)

## What shipped

Since ut-docs#6076, `ShortcutsRepo.loadButtons` no longer reads
`shortcut_buttons.image_path`. The tile picture is the item's `item_images`
thumbnail, then `items.icon`. But seven paths still wrote the column. This
change stops all of them:

- `internal/data/shortcuts_repo.go`: `SaveButtons` and `MaterializeAndReorder`
  insert without the column. `AddButton` changes three statements:
  - its insert drops the column;
  - its existing-row update sets only the label;
  - its `ON CONFLICT` no longer sets `image_path = excluded.image_path`.

  `nullIfEmptyButton` was unused afterwards and is deleted. `ShortcutButton.ImageURL`
  is documented as read-only.
- `internal/ui/buttons.go`: `ButtonsHTTP.Add` no longer reads or normalises
  `imageUrl`. `ButtonStore.Save`/`Add` no longer pass it. `SearchResult.AddVals`
  no longer posts it.
- `internal/pages/buttons_api.go`: the `/api/buttons/add` closure no longer
  reads `imageUrl` or carries it as an elevation-retry hidden field.
- `internal/data/seeddata/demo_catalogue.sql`: the seed no longer writes
  `image_path`. It is not a migration, and the bundled photos still come from
  the seed's `item_images` rows.
- `docs/data-model.md`: `image_path` is marked "no longer written by this
  version, not read".

**Decisions (Architect).**
- `imageUrl` is ignored rather than refused with a 400. A mixed-version fleet
  forwards an older satellite's Designer form, which still carries `imageUrl`,
  through catalogsync. A Designer page rendered before the upgrade carries it
  too. The value has been invisible since #6076.
- The column is left out of writes rather than written as NULL. A legacy value
  on an existing row is left untouched, because nothing reads it.
- The column stays, because migrations are append-only (ADR-0100) and LAN sync
  copies it as plain data.

## Tests (TDD)

- New `TestShortcutWritesNeverStoreImagePath_6097`, with one subtest per write
  path. It covers SaveButtons, MaterializeAndReorder, the AddButton insert,
  ON CONFLICT with and without a legacy value, and the existing-row update.
  It also pins that a legacy value is left untouched.
- Flipped tests:
  - `TestAddButton_ReusesTheItemsExistingRow`
  - `TestSeedDemoCatalogue`: no seeded row has `image_path`.
  - `TestButtonsHTTPAdd_IgnoresImageURL_6097` (renamed)
  - `TestSearchResultAddVals_QuotesAndBackslashesSurvive`: no `imageUrl` key.
  - `TestButtonsAddValidatesPersistsAndIgnoresImage` (renamed)
- **Red before the fix.** The Tester reverted the four production files and
  kept the tests. Six tests failed across data, ui and pages, for example
  "seed image_path count 10" and "3 HTTP-added buttons storing a path". The
  Tester then restored the fix, and the diff was byte-identical to before.
- The #6076 render tests (`item_photo_sell_screen_6076_test.go`) are green and
  unchanged.

## Findings (independent review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Low | `docs/data-model.md` said "never written". LAN sync copies the column, and an older primary in a mixed fleet still writes it. | Fixed: "no longer written by this version". |
| 2 | Low | No test pins that the elevation-retry form has no `imageUrl` hidden field. | Accepted: the hidden-field list is a literal of three named fields, and the value is unread anyway. |
| 3 | Low | ON CONFLICT was only tested with a NULL `image_path`, and the single sequential test with `Fatalf` hid later paths' failures. | Fixed: one subtest per path, plus an ON CONFLICT legacy-value subtest. |
| 4 | Nit | The local `clear` shadowed Go's builtin. | Fixed: renamed `reset`. |
| 5 | Nit | Over-long comment line in `shortcuts_repo.go`. | Fixed: rewrapped. |
| 6 | Nit | `…NormalizesImage` test name was stale. | Fixed: renamed `…IgnoresImage`. |

The reviewer checked by running it:
- SQL placeholder and argument counts match on all four writes.
- No JS or template still posts `imageUrl` to `/api/buttons/add`. The
  `imageUrl` in `catalog.html` is the item editor's own field.
- Reads, LAN sync row copy and cloud reorder are unaffected.
- gofmt, vet and golangci-lint report 0 issues.

## Extra verification (Tester)

**Gate:**
- `go test ./...` passes in every package except `internal/plugins`, which
  hit the 10-minute timeout. That package is untouched by this diff, and the
  Tester did not re-run it on `main`.
- gofmt is clean and golangci-lint reports 0 issues.
- About 60 guards in the CI `build` job ran green. Four did not:
  - `guard-catimport-version` (no tags fetched), `guard-deadcode-baseline`
    (toolchain mismatch) and `guard-shellcheck-version` (no shellcheck) failed
    identically on the unchanged tree, so the sandbox is the cause.
  - `guard-makefile-version` hit the Tester's 60s timeout.

**Driven run:**
- The real till ran with the e2e seed.
- `POST /api/buttons/add` with `imageUrl=/evil/legacy.png` returned 200, and
  the new row has `image_path` NULL.
- The tile rendered the item's thumbnail; the screenshot was viewed at
  1024×700, light theme.

After the review fixes, the orchestrator re-ran these and they pass:
- the data `TestShortcut…6097`, `TestAddButton*` and `TestSeedDemo*` tests,
  and the pages `TestButtonsAdd*` tests;
- golangci-lint on data, ui and pages (0 issues);
- guard-docs-shots, guard-fixed-dates and guard-data-access.

**Pre-flight:**
- Till user-visible behaviour: n/a (tiles render exactly as after #6076; no help topic describes a tile-only picture). docs-shots: n/a (guard fresh, no screen changed).
- reference: updated (`docs/data-model.md`).
- README: n/a.
- `en.json`: n/a (no keys).
- ADR: n/a (column kept per ADR-0100).
- Follow-up: none filed. Removing the production-dead `ButtonStore.Save`/`SaveButtons` is optional and too small for a card; it is noted here.

## Verdict

Approved. Nothing blocking; every low finding is fixed or accepted with a reason.
