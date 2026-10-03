# Code review: `set_net_quantity` directive (ut-docs#3402)

- **Date:** 2026-10-02
- **Card:** universaltill/ut-docs#3402 — the cloud-managed catalog sets an
  item's net quantity (follow-up to #3391, which made it settable only on
  the till). Spec: ut-docs `reference/manage-shop-catalog-api.md` §3.12
  (written on the same branch, `ut-docs-3402-set-net-quantity`).
- **Branches:** `ut-docs-3402-set-net-quantity` in `universal-till`
  (Dev commit `bef5464`) and in `ut-docs` (Dev commit `858ae04`).
- **Author:** Opus (Dev subagent). Tester: separate subagent, reported
  "ready for reviewer".
- **Reviewer:** Fable, independent, fresh context; re-derived from the two
  diffs, not from the Dev's or Tester's reports.

## What shipped

- A new main-till-only, catalog-type cloud directive `set_net_quantity`:
  `{id, net_quantity_value, net_quantity_unit}` or `{id, clear: true}`.
  §0 rule 10 of the contract forbids a new field on an existing directive
  type (an older till would silently drop it), so it is a new type — but it
  adds **no new `cloudsync.Hooks` field**: `decodeSetNetQuantity` produces a
  `data.ItemPatch` carrying only the id and the pair (or `ClearNetQuantity`)
  and `apply` sends it through the existing `SaveItem` hook, so it shares
  `save_item`'s repository path, transaction, `cloud_item_saved` audit row
  and idempotency.
- `data.ItemPatch` gains `NetQuantityValue *int64`, `NetQuantityUnit
  *string` and `ClearNetQuantity bool` (a third state, because nil/nil
  already means "keep"). `CatalogRepo.SaveItem` validates the pair before
  `BeginTx` (`catalogtypes.ValidNetQuantity`, the same rule the till's own
  editor and `updateItemExec` apply), refuses clear+pair, and writes both
  columns together or clears both.
- Tests: decoder table (13 refusals, set, clear, repository refusal
  surfacing as the directive text), satellite skip, repo set/update/keep/
  clear/replay/create/seven refusals with rollback, and the pages wire test
  (audit row names `net_quantity`, invalid pair writes nothing).
- Contract: new §3.12 (field table, failure texts, result text, audit,
  snapshot gap, `DirectiveMinTillVersion` deferral), and the §3 intro's
  main-till-only list.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | must-fix | **Both branches no longer merged with `origin/main`.** On the till, #3319 (`save_option_set`/`delete_option_set`) landed after the branch base and touched the same header comment, the same `catalogTypes` literal and the same satellite-skip test list. In ut-docs, #3477 landed **§3.11 `save_item_variant`**, so the Dev's "§3.11 `set_net_quantity`" collided on the section number — and that number is cited in six till code comments. `git merge-tree` reported conflicts in both repos; the same trap #3391 fell into. | **Fixed.** A rebase was refused by this session's permission policy, so each branch got a non-destructive merge of `origin/main` (the reviewer skill's alternative for a branch that must not be force-pushed), resolved by hand: both option-set entries kept beside `set_net_quantity`; the satellite-skip test lists `set_net_quantity` as `d10` after #3319's `d8`/`d9`; `set_net_quantity` is **§3.12** in the contract (placed after `save_item_variant`, §3 intro updated) and in every till citation (`catalog_directives.go` ×2, `cloudsync.go`, `catalog_directives_test.go` ×2, `cloudsync_catalog_wire_test.go`). Full gate re-run on the merged trees (below). **For the orchestrator:** both branches now contain `origin/main`, so the pre-PR `git rebase origin/main` is unnecessary — and would drop the merge commits and re-raise the conflicts; push as-is (no force needed). |
| 2 | nit | `clear: true` + `net_quantity_unit` fails with "net_quantity_value and clear cannot both be set", naming the wrong field. | Accepted. Same shape as `set_catalog_image`'s "sha256 and clear cannot both be set"; the contract states the exact text for "either field". |
| 3 | nit | `clear: true` with `net_quantity_unit: ""` is refused (a present empty string is non-nil), whereas `set_catalog_image` reads `sha256: ""` beside `clear` as absent. | Accepted. The contract says "`clear` with either field → fail", and no my. client exists yet to send that shape; stricter is the safer default for a new type. |
| 4 | info | `res.Changed` names `net_quantity` on a replay (same pair) and on clearing an item that has none, so the audit row says "changed" when the value did not. | Accepted, by precedent: `SaveItem`'s `Changed` is "fields the patch set" for every other field too (`name`, `price_minor`, …), and the Dev's unrelated-save test pins that a patch *without* the pair never reports it. |
| 5 | info | §4.4 `claims.DirectiveMinTillVersion` is not extended. | Correct as deferred: #3139's placeholder was wrong by six patch releases; the ut-cloud follow-up sets it from the real release. |
| 6 | process | No review record yet. | This file. |

## Verified

The Tester's claims were a hypothesis, re-run and extended here:

- **TDD, reverted and restored, three times:** removing the decoder's
  clear+pair refusal → `TestApplySetNetQuantity` fails ("applied", hook ran
  2 times for refused payloads); removing the repo's
  `p.ClearNetQuantity ||` guard → `TestSaveItem_NetQuantityInvalidPairRefused`
  fails ("clear and set together: err = <nil>" and the row changed);
  making a nil pair clear the stored one → `TestSaveItem_NetQuantitySetUpdateKeepClear`
  fails ("unrelated save: net quantity = -|-"). All green again after
  restore, tree clean.
- **Half-populated pair / clear + pair:** refused twice over — decoder
  ("missing net_quantity_unit" / "… cannot both be set") and repository
  (`ValidNetQuantity` returns false for value-only or unit-only;
  `setNetQuantity && ClearNetQuantity` → `ErrInvalidNetQuantity`), both
  before `BeginTx`, so nothing is written and the write lock is never taken.
- **Malformed input:** `1.5`, `"lots"`, `> 2^53`, a numeric unit, `"maybe"`
  for `clear`, and JSON `null` all fail as "bad <field>" (the same
  `optInt`/`optStr`/`optBool` readers `save_item` uses); `0`, negative and
  `"kg"` reach the repository and fail "invalid net quantity"; `"G"` is
  refused (strict case, as documented). All SQL is parameterised through
  `updateItemExec`; no new query text.
- **Concurrency:** the new validation is pure and pre-transaction; the write
  is the existing single-tx read-modify-write; directives apply
  sequentially on the main till's own tick; no new shared state. Nothing
  new to race.
- **Idempotency under a lost-result replay:** the same payload re-runs the
  same `UPDATE`; `recordPriceChangeExec` no-ops when old == new price, so a
  net-quantity-only save appends **no** price-history row; clear-on-clear
  succeeds. Pinned by the repo test's replay step and the wire test.
- **Create path:** `CreateItemTx` does not take the pair; the following
  `updateItemExec` writes it (`TestSaveItem_NetQuantityCreate`).
- **Satellite:** in `mainTillOnlyTypes` and `catalogTypes`;
  `TestTickSatelliteSkipsMainTillOnlyTypes` covers the skip, no result post.
- **Doc/code parity:** all eight failure texts and the result text match
  the contract word for word; `delete_item` is in `mainTillOnlyTypes` as
  the amended §3 intro now claims; §2.10's filter is worded generically
  ("every catalog/config type in §3"), so no second list was missed.
- **Not applicable, checked anyway:** no money field, no i18n key (no
  user-facing string beyond the existing directive-failure pattern), no UI
  surface (the my. editor is the follow-up), so no help topic; no plugin
  surface; no file I/O; no secrets or real shop names in fixtures. No
  CHANGELOG entry, matching #3075/#3139/#3317.
- **Gate on the merged trees:** `gofmt -l` clean, `go build ./...`,
  `go vet` on the three packages, `golangci-lint run ./...` 0 issues,
  `go test` green for `internal/cloudsync`, `internal/data` and the
  `internal/pages` cloud wire tests on the merged tree (the full
  `go test ./...` run was cut short by the session harness and is left to
  CI, which gates the merge anyway), `guard-data-access`, `guard-price-history-sync`, `guard-core-neutral`,
  `guard-i18n`, `guard-migration-version-collision`,
  `guard-pipefail-grep-q`, `guard-card-data-schema` all green.
- **Not verified:** a live ut-cloud → till run (no cloud endpoint queues
  this type yet); the ADR-0011 admin sync carrying the two columns to a
  real satellite (relies on #3391's column discovery, untouched here).

## Cross-repo

- ut-docs: same branch name, contract §3.12, merged with `origin/main`
  the same way (finding 1).
- ut-cloud (follow-up card, named in §3.12): the `…/items/{id}/net-quantity`
  endpoint, the my. editor control, `claims.DirectiveMinTillVersion`
  from the real till release, and the snapshot (§3.7) reporting the pair.

## Verdict

Safe to merge once CI is green on the merged heads. Merge with
`merge_method: "merge"` (#250); no force-push is needed.
