# Review: catalogue-mode entry marks unsent icons (ut-docs#6243)

Date: 2026-11-28. Author: Sonnet dev subagent. Reviewer: Opus 5.5 independent
subagent (different model), plus orchestrator read of the fix round.
Pre-flight: walked (BA, Architect, Dev, Tester, Review, fix round; notes kept
in the cycle scratchpad).

## Change

`cacheEntitlement` now writes the entitlement block through
`SettingsRepo.SetManyMarkingCatalogModeEntry`. Inside one transaction it reads
the previous `entitlement.catalog_master` (missing or unknown = till), writes
the settings, and only on till -> cloud/adopting runs
`CloudOutboxRepo.MarkUnsentPromotedFieldsAtCloudDefault`. That marks a linked
item/category icon only when the cloud holds the field at its default (`""`,
null or missing key, which is then seeded to `""`) and no other upload field
of the row differs from its base. A replica never marks (`sync.primary_url`).
A marking error rolls back the whole cache write and raises the problem
`cloudsync.catalog_mode_entry_marks` until a cache write succeeds.
`MarkUnsentPromotedFields` / `PromoteReplica` are untouched (in-flight #6242).
No migration, no new user-facing string.

## Findings (Opus round 1, verdict "ship after fixes")

1. MEDIUM, fixed: a mark covers the whole row, so other till-mode edits would
   upload and win on rows with an icon (against ADR-0162 §6). Now a row is
   marked only when every non-promoted upload field equals its base; tests
   `TestCatalogModeEntry_OtherFieldEditedNotMarked_6243` and
   `..._OtherCategoryFieldEditedNotMarked_6243` failed first.
2. LOW-MEDIUM, fixed: the contract sentence (ut-docs
   `reference/contracts/cloud-catalog-changes.md`) now states the real rule.
3. LOW, fixed: a persistent marking failure froze the entitlement cache with
   only a Warnf; it now raises a problem (`TestSyncEntitlement_MarkFailureRaisesProblem_6243`, failed first).
4. LOW, accepted: the transition runs once; a row with a damaged base is left
   to `seedBases` (same as `MarkUnsentPromotedFields`).
5. LOW, accepted + follow-up: the mark SQL duplicates ~25 lines of
   `cloud_outbox_promote.go`; merge once #6242 lands.
6. NIT, fixed by 1's realistic full-base tests.
7. NIT, fixed: `setManyHooks` comment.
8. NIT, not done: no Debugf on a zero-mark entry.

Orchestrator note: `otherFieldsMatchBase` mirrors catalogpull's
`uploadFields` by hand ("KEEP IN SYNC"); no test pins the two lists yet —
recorded as a follow-up on the card.

## Verification

- Tester (independent): PASS. Six deliberate breaks each fail a new test;
  tree restored by checksum. `go test` data/cloudsync/catalogpull pass
  without -race; targeted -race passes. Guards data-access, core-neutral,
  fixed-dates, i18n pass.
- Pre-existing, not this branch: cloudsync
  `TestChannelOrderVendoredSchemaIsCurrent` fails on origin/main 198ca31ec too
  (ut-docs ADR-0215 changed `channel-order-v1.json`).
- After the fix round: gofmt clean, build, vet, golangci-lint 0 issues on the
  three packages, filtered tests pass with and without -race (re-run by the
  orchestrator).
- No UI surface touched; no driven run needed.
