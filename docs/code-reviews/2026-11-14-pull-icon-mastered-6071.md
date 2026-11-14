# Review — the cloud masters item and category icon, till half (ut-docs#6071)

**Date:** 2026-11-14 · **Author:** Dev (Sonnet) · **Reviewer:** independent Opus review · **Tester:** separate agent

## What changed

- `internal/catalogpull/base.go`: `icon` joins `uploadFields` for item and category (changes contract 1.2.0); `readLocal` sends `iconOut(local)` (a malformed stored id reads as `""`, never uploads); `baseFromRow` turns a null icon into `""`; `seedChunk` (applier-3 links) sets base icon `""` and opens a mark for a row with a local icon. `tillOwnedFields` comment now describes the promotion recipe.
- `internal/data/cloud_outbox_repo.go`: `OutboxItem.Icon`, `OutboxCategory.Icon` read in the existing SELECTs.
- `internal/db/migrations/103_cloud_outbox_icon.sql` (checksum pinned): recreates the item and category update triggers with `icon`; marks linked rows with a local icon (only where the trigger would mark), then sets linked bases' icon to `""` (marks first, so a rerun is a no-op).
- Tests: `icon_mastered_6071_test.go`, `migration_103_cloud_outbox_icon_test.go`; `till_owned_6072_test.go` loses `TestPullAppliesIconAsServedUntil6071` and the "icon (interim)" set.
- `docs/data-model.md`: till-owned note, upload field set (1.2.0), migration 103.

## Known limits

- Ship order: the cloud (changes 1.2.0) must be live first; a 1.1.x cloud answers 400 to an icon change and stalls the outbox (#6088 is the handshake fix).
- A change with icon that loses a by-value conflict on another field is answered `conflict`; the till applies the cloud's row (icon `""`). ADR-0162 §5 logs the till's change in `CatalogConflict`. Rare (needs an unsealed conflicting edit at upgrade time); not special-cased.
- `seedChunk` marks an iconned applier-3 link without checking the catalogue mode: it only runs where the till pulls from the cloud; a replica never uploads.

## Review

Independent Opus review (different model from the Sonnet author): approve with changes.

- **F1 medium — fixed (on the cloud side).** The till uploads any `iconid.ValidFormat` id, but the cloud first accepted only registry ids: an unknown well-formed id would have conflicted a whole change, or tombstoned a new row. ut-cloud#1011 now accepts any well-formed id, the same rule as the till (`iconid.ValidFormat` is byte-identical in both repos), so the till side needs no change.
- **F2 medium — release gate, honoured.** On a 1.1.x cloud, migration 103's marks stall every iconned shop's outbox on the first tick. Merge order was ut-docs#6235 → ut-cloud#1011 → this PR, and this PR merges only after ut-cloud's `verify-live` reports a commit containing #1011.
- **F3 low — accepted.** Marks go stale after 30 days if uploads stay blocked; the stale conflict then wipes the icon. Same end state as `main` today.
- **F4 low — accepted, pinned.** A change already pending at upgrade carries the icon; if it loses a by-value conflict on another field, the icon falls back to the cloud's `""` (logged in `CatalogConflict`). This is the one clause of the BA's AC4 not met, by design (architect §6); `TestPushIconConflictAppliesCloudRow_6071` pins it.
- **F5 low — accepted.** A create sealed before the upgrade and answered after it loses its icon (one-off, upgrade window only).
- Nit (trigger-column test): covered by the Tester's mutation run (dropping `icon` from either trigger is killed), no separate test.

Checked: both recreated triggers keep every column migrations 102 (items) and 084 (categories) watched, plus `icon`, with the same WHEN conditions; marks use the trigger's own gating (no marks on till-master, unset or replica stores); no file writes, no cwd-relative paths.

**Tester: PASS.** TDD re-verified (each new test fails on reverted source for the stated reason); 6 migration mutants and 3 Go mutants killed (one survivor: seedChunk's warning count, log-only); added `icon_tester_6071_test.go` (migration-driven seed → pull → push → later cloud write keeps `lucide:beer` and the photo; till→cloud clear; the F4 pin). Driven run: origin/main binary to ledger 102 on a temp data dir, seeded linked/unlinked items and categories, then the branch binary: ledger 103, outbox holds exactly the two iconned linked rows, every base gained `"icon":""` with other keys kept, icon and photo intact, `integrity_check` ok; a restart re-marks nothing.

Pre-flight: review record → this file (+ ut-cloud `2026-11-14-catalog-icon-6071.md`, ut-docs `code-reviews/2026-11-14-icon-mastered-6071.md`); reference pages → ut-docs contracts changes 1.2.0 / feed 1.1.2, `docs/data-model.md` updated; help topic n/a (no user-visible behaviour beyond icons no longer disappearing); en.json n/a; ADR n/a (ADR-0162 §2/§3/§5; §2 wording is #6083); follow-ups → ut-docs#6232 (store merge carries the icon), ut-docs#6233 (uploadFields ⊆ kindFields contract test), #6088 (version handshake).
