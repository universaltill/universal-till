# Review — till uploads is_weighed, age_restricted and net quantity (ut-docs#6202, till half of #6082)

**Date:** 2026-11-13 · **Lane:** `lane:cloud-41` · **Author:** Sonnet (Dev) · **Reviewer:** Fable 5.2, independent (data-integrity card, MODEL-ROUTING ¹)

## What shipped

- `internal/catalogpull`: `uploadFields[item]` and `readLocal` carry `is_weighed`, `age_restricted`, `net_quantity_value`, `net_quantity_unit` (contract `cloud-catalog-changes` 1.1.0, already on the cloud: ut-cloud#985 → `dc82d39`). `coupleNetQuantity` makes a change to either half upload both, and skips both if the base lacks either; the pull's pending-field strip uses the same coupling, so a half-stripped pair can't trip `ErrInvalidNetQuantity`. `ApplierVersion` 4 → 5 (re-pull so every base carries the four fields).
- `internal/data`: `OutboxItem` / `OutboxItemValues` read the four columns.
- Migration **102** `102_cloud_outbox_item_flags.sql` (append-only; 084 untouched; checksum pinned): recreates `trg_cloud_outbox_items_upd` with the four columns added to `UPDATE OF` (WHEN gating and body identical to 084), and backfills existing item bases from the local row (non-object / invalid JSON bases left alone).
- Fake cloud in tests: accepts the four fields, refuses a lone net-quantity half like the real cloud.
- `docs/data-model.md`: "What uploads" is 1.1.0 with the four fields and the pair rule; migration 102 and applier 5 named.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | high | Written as migration 103 while open PR #2546 holds 102. If 103 merged first, a till that booted it refuses to start once 102 lands (`verifyAppliedMigrations`: an on-disk version below the watermark with no ledger row). | **Fixed:** renumbered to 102 (checksum unchanged — header comment only). #2546 must renumber its migration to 103 when it rebases; commented there. |
| F2 | medium | `docs/data-model.md` upload section still said 1.0.0 / eight item fields / 084 only. | **Fixed.** |
| F3 | low | A delete's `before` copies base fields without pair coupling; a one-half base would send one half → 400 → outbox stalls. No path produces such a base today (backfill, `baseFromRow`, `seedChunk`, accepted answers all write both or neither). | **Deferred** → ut-docs#6203 (hardening + test). |
| F4 | nit | Test reads `../db/migrations/102_…sql` relative to cwd. Test-only, correct under `go test`. | Accepted. |

## Verification

- TDD: tests written first by Dev and seen failing. Tester reverted each production hunk (`coupleNetQuantity`, `uploadFields`, `readLocal`, backfill, trigger column list) and saw the matching tests fail; Reviewer independently re-verified `TestTillEditOfFlagsAndNetQtySurvivesCloudPriceEdit_6082` (6 subtests) fails with the `uploadFields` hunk reverted.
- Reviewer probed the backfill on the till's SQLite driver: `[]`, `"x"`, `5`, `null` bases unchanged; booleans as JSON true/false; NULL pair as null; idempotent.
- `gofmt -l .` clean, `go build ./...`, `go vet` (catalogpull, data, db), `golangci-lint` on those packages: 0 issues. `go test ./internal/catalogpull/ ./internal/data/ ./internal/db/` ok. `-race` only with `-run` on the new tests (full catalogpull `-race` exceeds 15 min in the cloud sandbox; CI runs it).
- Guards run locally: data-access, card-data-schema, migration-version-collision, fixed-dates, core-neutral, mail-addresses, ci-tests-sandboxed — all pass. `guard-deadcode-baseline` can't run locally (tool built with go1.26 vs go1.27 module; fails identically on `main`) — left to CI.
- No UI touched: no browser run; the driven run is the fake-cloud push/pull harness.

## Known limits (as designed)

- A till edit of these fields made before the upgrade and never marked is replaced by the cloud value on the applier-5 re-pull.
- A cloud still on contract 1.0.0 answers 400 to every item create / flag edit: ut-cloud `dc82d39` must be deployed first (built and pushed from `main` at `7f71321`, 2026-11-13 16:49Z; ArgoCD rolls `main` out). Tills only get this with a release.

Pre-flight: till user-visible behaviour — `web/help` n/a (help does not list uploaded fields; nothing on screen changes), `reference/` updated earlier (contract 1.1.0, ut-docs#6089), `docs/data-model.md` updated, `README.md` n/a; `en.json` n/a (no keys); review record — this file; follow-up F3 → deferred → ut-docs#6203.

**Verdict:** approve (after F1/F2 fixes).
