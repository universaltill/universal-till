# Review — ADR-0051 rows carry description, age restriction, net quantity (ut-docs#4542)

**Date:** 2026-10-17 · **Lane:** lane:cloud-41 · **Author model:** Opus 5.5 ·
**Reviewer model:** Sonnet 5.5. Fable was the routed reviewer, but the call
was refused (out of usage credits), so Sonnet stood in, per MODEL-ROUTING.md.

## What shipped (universal-till half)

- `catimport.Row` gains additive, `omitempty` fields under the ADR-0051 §3
  minor-version rule:
  - `description`
  - `age_restricted` (`*bool`)
  - `net_quantity_value` + `net_quantity_unit`
- `RowFor` fills these fields:
  - it copies the description, trimmed;
  - it emits `age_restricted` only as `true`, because a file without the
    column can't say "not restricted";
  - it copies the already-validated net-quantity pair.
- catsync validates the fields per row, adding the issues
  `description_invalid` and `net_quantity_invalid`:
  - descriptions are bounded at 1,000 runes; `\n\r\t` are allowed; other
    control and format characters are refused, except ZWNJ and ZWJ;
  - the net-quantity pair is checked with `catalogrules.ValidNetQuantity`.
- catsync applies the fields as "source wins", as it does for `name`, and
  leaves an item as it is when a field is absent. `SyncItemState` reads
  them (the SQL stays in `internal/data`).
- The import-from-system change labels and 5 new locale keys were added to
  en/ar/fa/tr and to `en.meta.json`. The de/es/pt pack PRs follow this
  merge.
- Takeaway VAT and images are split out to ut-docs#4572 and ut-docs#4573.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (ut-cloud) | A link created from a row without the new fields stored no baseline for them, so a later my. edit was overwritten. | Fixed in ut-cloud. The baseline now always records the created value; `NetQuantitySet` was added. |
| 2 | major (ut-cloud) | The cloud's `CheckDescription` accepted control characters; the till refuses them. | Fixed in ut-cloud with the same character rule as catsync. |
| 3 | minor | An explicit `age_restricted:false` from a plugin lifts an ID check. | Accepted: a present value is authoritative, the same as `active`. It is now documented in `reference/plugin-host-functions.md`. |
| 4 | minor | The till's net quantity has no upper bound; the cloud caps it at MaxInt32. | Accepted. This was already documented as one of the cloud's stricter rules. |
| 6 | low-medium (both repos, second-pass review) | Refusing every Unicode Cf character also refused the soft hyphen (German supplier text) and LRM/RLM (Arabic/Hebrew text), so the whole row was lost. | Fixed on both sides: U+00AD, U+200E and U+200F are allowed. Bidi overrides and zero-width spaces stay refused. A test case was added for each. |
| 5 | nit | The cloud's description refusal reuses `invalid_name`. | This predates this change and is left as it is. |

**Till side:** the reviewer found no defect in catsync. It checked:
- an update that doesn't touch the net quantity never clears it;
- the active/`tempActive` logic is unchanged;
- preview writes nothing;
- the Changes vocabulary matches the docs.

## Verified

- The reviewer reverted the fix in scratch worktrees, and each targeted
  test then failed:
  - `TestRun_UpdateDescriptionAgeAndNetQuantity` failed with age detection
    off, and again with the net-quantity copy removed;
  - `TestRun_ModuleRowsCarryTillExportColumns` failed with the
    net-quantity copy removed;
  - `TestRowFor_AgeRestrictedOnlyWhenTrue` failed when age was always
    emitted.
- Gates:
  - passed: gofmt, `go build ./...`, golangci-lint (0 issues, in the root
    and in `catimport`);
  - tests passed: `go test` on catsync, data and pages, and on `catimport`;
  - guards passed: data-access, i18n, i18n-meta, catimport-module and the
    other build-job guards.
- The full `go test ./...` passed. `internal/plugins` passed only with
  `-timeout 40m`: it is slow and unchanged here.
- `guard-deadcode-baseline` couldn't run locally: the local deadcode
  binary is too old for go1.27. CI runs it.
- Not driven in a real UI. There is no new screen, only change labels in
  the existing import-from-system result. They are covered by
  `TestImportSystemChangeKeys_CoverCatsyncChanges`.

## Verdict

Safe to merge. After merging, tag `catimport/v0.3.0` and bump ut-cloud's
`require`. Then land the de/es/pt pack PRs for the 5 new keys.

Follow-up: ADR-0161 §3's list of the fields an update writes is now
incomplete. That gap is additive and not contradictory; it is noted on the
card.
