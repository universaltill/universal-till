# Review: `update_category` directive, till side (ut-docs#2354)

**Date:** 2026-09-24 · **Lane:** local · **Author model:** Opus 5.5 · **Reviewer:** Fable 5.1 (independent subagent)
**Branch:** `feat/2354-update-category` · **Pair:** ut-cloud `feat/2354-update-category` (queue-side validation + form forwarding)

## What shipped
- `cloudsync.Hooks.UpdateCategory` and the `update_category` dispatch. `id` is required. `name`, `color`, `modifier_group_ids` and `station_ids` are optional and presence-aware: absent → nil (keep); present → set (`color: ""` clears, `"[]"` clears a link set).
  - A present field with the wrong shape fails (`bad <field>`) instead of being read as absent.
  - An empty edit fails with `nothing to update`.
  - Each id list is capped at 200 (`maxCategoryLinkIDs`, the same cap the cloud applies when queuing).
- `CatalogRepo.UpdateCategoryPartial` and `CategoryPatch` / `CategoryPatchResult` write the row and both link sets in one BEGIN IMMEDIATE transaction.
  - Everything is validated before any write. A missing category, a blank name, an unknown station, or an unknown or **inactive** group refuses the whole edit.
  - Existing links to inactive groups are kept across a replace, the same rule as `saveCategoryLinks` (#2284).
  - The result reports the effective sets that were written.
- `pages.cloudUpdateCategory`: palette allowlist, `requirePrimaryDirective`, and a `cloud_category_updated` audit row recording the effective outcome.
- The docs-shots surface hash was re-stamped. The only `internal/pages` change is the directive hook, which renders nothing. No UI, locale or help change: the editor UI is #2526.
- Why a new type rather than new `upsert_category` keys: an older till would ignore the new keys and report success, and would read an absent colour as "clear". With a new type, an older till answers `unknown directive type`.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | WIP commit included `internal/pages/zz_guard_test_UnlockedMenuAmendments.go`, a fixture planted by `guard-plugin-menu-read_test.sh` while the gate ran, so HEAD didn't compile | Fixed: file removed from the commit |
| 2 | Should-fix | A submitted **inactive** group was accepted; the local dialog refuses it | Fixed: `is_active = 1` in the existence check, and a test covers it |
| 3 | Should-fix | No bound on the id lists. Each id costs a lookup plus an insert under the till's single write lock | Fixed: dispatcher cap of 200, and a test covers it |
| 4 | Should-fix | The audit recorded the raw submitted lists, not what was written | Fixed: the repo returns the effective sets, the audit uses them, and a test covers it |
| 5 | Nit | Doc comment didn't say the transaction is BEGIN IMMEDIATE | Fixed |
| 6 | Nit | The row UPDATE runs even for a link-only patch and bumps the sync version needlessly | Accepted: harmless |
| 7 | Nit | The not-found error echoes the id the cloud sent | Accepted: not secret |

## Verified beyond the unit tests
- Reviewer mutation run: 11 of 11 mutations killed (inactive-link keep, existence checks, nil-means-keep for colour and groups, blank name, three dispatcher shapes, primary gate, palette, audit).
- After the fixes I mutated each new rule myself: the inactive-group filter, the list cap and the effective-set audit were each reverted, their test failed, and the code was restored.
- Full gate: `go build ./...`, `go test ./...` and `golangci-lint` (0 issues) pass. Every CI guard passes, except three that also fail on a clean `origin/main` checkout on this Mac and are not caused by this change:
  - `mobile` `TestStart_ListensOnAllInterfaces…`
  - `guard-competitor-naming_test.sh`
  - `guard-shellcheck-version.sh` (local shellcheck is 0.11.0, CI pins 0.9.0)

## Verdict
Safe to merge.

## Deferred
- Editor UI: #2526, `blocked:dep` #2494.
- End-to-end run against a real till happens with #2526, since nothing sends this directive until then.
