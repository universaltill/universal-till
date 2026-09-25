# Review — my. category icon change applied but never shown; one icon registry (ut-docs#2717, #2664)

- **Author:** Opus 5.5 (Dev subagent) + one review fix by the orchestrator (Opus 5.5). **Reviewer:** Fable 5.1, fresh context. Complexity:medium, one round.
- **Owner report:** a my. icon change for two categories logged `save_category applied` on the pilot tablet, but the sale screen kept the old picture.
- **Root cause:** the sale screen drew `image_path` (the #2500 library tile) before `icon`, and my. only writes `icon`. The till also knew only 5 of my.'s icon ids.

## Change
One picture per category, last writer wins (`SetCategoryPicture`; a directive icon clears `image_path` in the same tx and deletes a superseded upload). One registry in `internal/iconid/library.go` (73 tiles with real lucide/tabler ids; `lucide:leaf` added). Read-time `iconid.Resolve`, no migration. The snapshot reports `EffectiveIcon`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | New key `catalog.builtin_icon.leaf` missing from the de/es packs | Pack PRs in the same cycle |
| 2 | Low | my. *clearing* the icon on a legacy row (library tile in `image_path`, no icon) left the tile drawn and re-reported | **Fixed**: a cleared icon also clears a library-tile `image_path` (not photos). Test `TestSaveCategory_ClearedIconAlsoClearsLegacyLibraryTile`, red before the fix |
| 3 | Low | Narrow race: an editor upload landing between the directive's commit and its photo delete loses the new file (the row keeps the path, nothing drawn until re-saved) | Accepted, self-healing (cloud tick vs a human click) |

## Checked, no issue (reviewer)
The cloud-driven delete can't traverse paths (`safeCategoryID` + an exact `categoryThumbURL` match; the DB value is never used as a path). The precedence function is used by every reader (sale screen, /categories, Designer, snapshot, editor JS). No remaining `SetCategoryImage` callers. The `leaf.svg` ISC header and LICENSE are present. The my. picker fixture matches ut-my-shop and ut-cloud. Cache invalidation is proven. The e2e harness keeps `UT_OPEN_BROWSER` at 0. Help in 5 languages. TDD re-verified by reverting the precedence and clear-in-tx (tests fail).

## Verification
`go test -race ./...` (Makefile timeouts); e2e `category-icon-directive-2717` (never-restarted till: the tab changes beer → egg-fried → leaf) + `category-image-2500`, 2010, 2699 specs; gofmt; guards. Pilot tablet check after release.
