# Review: `apply_item_option_sets` directive + `option_set_ids` in the snapshot (ut-docs#4163)

Slice A of the my. "variants from option sets" work (ut-docs#3932, split into
#4163 till / #4164 ut-cloud / #3932 my.). Contract: ut-docs
`reference/manage-shop-catalog-api.md` §3.7 (`option_set_ids`) and §3.13.

## What shipped

- **New directive `apply_item_option_sets`** `{item_id, option_set_ids: JSON
  []string, generate?}`:
  - `cloudsync.decodeApplyItemOptionSets`: wrong shapes fail before the hook.
  - Hook `ApplyItemOptionSets` → `cloudApplyItemOptionSets` → one
    `OptionSetRepo.ApplyOptionSetsAndGenerate` call.
  - Main-till only (`mainTillOnlyTypes`, `requirePrimaryDirective`) and a
    catalog type, so the snapshot re-pushes after it applies.
  - Audit row `cloud_item_option_sets_applied`.
  - Generation uses the till's own `GenerateVariants`. A variant generated
    from my. therefore carries its `item_variant_options` links and a
    till-minted `VAR-` SKU, and a later Generate on the till never duplicates
    it. (Generating client-side through `save_item_variant` would have
    produced unlinked variants that the till's Generate duplicates.)
- **`ApplyOptionSetsAndGenerate`** checks every refusal before writing
  anything:
  - missing item;
  - unknown set;
  - newly applied inactive set (one already applied may stay);
  - more than 2 sets;
  - duplicate or blank id;
  - variant cap.
  It then applies, and generates when asked.
- **Variant cap in `GenerateVariants`:** the existing variants plus the new
  combinations may not exceed `MaxItemVariants` (100). This is the cloud's
  per-item cap; `SaveVariant` already refuses the 101st.
  - The refusal is a typed error, `*TooManyVariantsError`, which matches
    `ErrTooManyVariants` with `errors.Is` and carries the counts.
  - The till's Generate button now shows `catalog.variants.generate_too_many`
    with the counts in the panel instead of a generic "invalid request".
- **`GenerateVariants` refactor:** the cartesian product moved into
  `missingCombinations`, shared with the pre-check, so both compute the same
  set. List validation is factored into `validateOptionSetIDs`, shared with
  `ApplyOptionSetsToItem`.
- **Snapshot:** `option_set_ids` on every item, in axis order, `[]` for none.
  It comes from one batched `item_option_sets` query, so no N+1.
- **Help:** `web/help/{en,de,tr,ar,fa}/option-sets.md` gained a bullet on the
  100-variant cap. The help-drift baseline (fa/tr `top_bullets` 8→9) was
  updated in lockstep, and the docs-shots for the topic were regenerated.
- **i18n:** one new key, `catalog.variants.generate_too_many`, in en/ar/fa/tr.
  The de/es/pt pack PRs follow the core merge in the same cycle.

## Review (independent, Fable; the author was Opus 5.5)

| # | severity | finding | outcome |
|---|---|---|---|
| 1 | blocker | `guard-help-drift.sh` red: the fa/tr baseline still recorded 8 top bullets | **fixed**: baseline bumped with a reason note |
| 2 | major | The till's Generate button showed "Invalid request" for the cap, contradicting the contract and the help text | **fixed**: typed error + panel message with the counts (new locale key, TDD: `TestCatalogVariantsPanel_GenerateTooManyShowsTheCap` failed with 400 first) |
| 3 | minor | An inactive set that is still applied was refused, so re-sending the item's current list, or replaying after a deactivation, failed while the till's own Generate worked | **fixed**: only *newly* applying an inactive set is refused (`TestApplyOptionSetsAndGenerate_KeepsAnAlreadyAppliedInactiveSet`); contract updated |
| 4 | minor | The cap pre-check, the apply and the generate are not one transaction. A concurrent till-side variant add can make the inner check refuse after the sets were applied | **accepted, documented** in contract §3.13 rule 3. Nothing is corrupted; a full fix needs a tx-taking generator, out of proportion here |
| 5 | minor | List validation duplicated between two repo methods | **fixed**: `validateOptionSetIDs` |
| 6 | info | The new snapshot key changes every till's snapshot hash, so each upgraded till re-pushes once | accepted (same as the #3584/#3015 precedents; `snapshotSchema` unchanged) |
| 7 | nit | fa help bullet used Persian digits while the file uses ASCII | **fixed** |
| 8 | nit | `×` (non-ASCII) in a directive result string | accepted: matches the contract; my. renders the text verbatim |
| 9 | nit | 3–200 ids fail in the repo, >200 fail in the decoder (two messages) | accepted: both are refusals |

The reviewer also confirmed:
- the in-place `combos[:0]` filter is safe and keeps axis-major order;
- `GetOptionSet` and `attachValues` sort values the same way, so the
  pre-check and the generator agree when nothing runs concurrently;
- the decoder distinguishes absent, `null` and `[]`;
- no file writes, no cwd-relative paths, no raw SQL outside `internal/data`,
  and only generic test names.

## Verified beyond the unit tests

- **TDD re-verified by the orchestrator in a separate worktree.** With the cap
  check removed from `GenerateVariants`, `TestApplyOptionSetsAndGenerate_VariantCapRefusesBeforeWriting`
  fails ("GenerateVariants err = <nil>, want ErrTooManyVariants"). With the
  snapshot field blanked, `TestCatalogSnapshotItemsOptionSetIDs` and
  `TestSnapshotReportsOptionSetIDs` fail. All pass once restored.
- **Full gate on the final tree:**
  - `gofmt -l`, `go build ./...`, `go vet ./...`, `golangci-lint run ./...`
    (0 issues), `go test ./...`;
  - every `scripts/ci/*.sh` the `ci.yml` build job runs, plus their
    self-tests.
  - `shellcheck` is not installed in this cloud container, but no shell
    script changed.
- **Visual:** the only rendered change is an error line in the variants
  panel. It appears only on a refused Generate, so no docs-shot covers it.
  The topic's screenshots were regenerated because its text changed. Not
  looked at: the refusal line itself in a browser (it reuses the existing
  `.option-set-pick-msg` style of the no-sets hint, with `role="alert"`).

## Verdict

Safe to merge once CI is green. The till ships first: the cloud ignores the
new snapshot field until #4164, and no cloud queues the directive until #4164
sets its `min_till_version`.
