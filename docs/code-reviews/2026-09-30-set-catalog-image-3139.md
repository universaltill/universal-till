# Code review: `set_catalog_image` directive (ut-docs#3139)

- **Date:** 2026-09-30
- **Card:** universaltill/ut-docs#3139 (#3076 slice B). Spec: ut-docs
  `reference/manage-shop-catalog-api.md` §3.9, plus §2.13 "Till fetch".
- **Lane:** lane:cloud-24.
- **Author:** Opus 5.5 (Dev subagent).
- **Reviewer:** Fable, independent, fresh context, in a detached worktree.

## What shipped

- A new main-till-only cloud directive, `set_catalog_image`. Its payload is `{entity, id, sha256, size}` or `{entity, id, clear: true}`.
  - `internal/cloudsync`:
    - Presence-aware decode (rule 1).
    - A fetch of `GET /api/v1/stores/catalog-images/{sha}`. It uses the store Bearer credential, a cap of 11 MiB and a 60 s bound, and checks the SHA-256 (rules 2–3).
    - An `apply` case, with `mainTillOnlyTypes` and `catalogTypes` entries.
  - `internal/pages/cloudsync_catalog_wire.go` has the `cloudSetCatalogImage` hook. It does the existence check before any fetch. It then stores the image with the same `PrepareThumb` + `WriteThumbPNG` + `item_images` / `categories.image_path` path as the till's own upload (now shared through `catalog.StoreItemPhoto` / `ClearItemPicture` and `storeCategoryPhoto` / `clearCategoryPicture`). It writes a `cloud_catalog_image_set` / `_cleared` audit row and calls `NudgeLink(ScopeAdmin)` so satellites pull the file.
- Snapshot items carry `image_sha256`, and so do the config report's categories. `ServedImageSHA256` computes it and caches it per file on size and mtime.
- Help: one sentence each in `catalog.md` and `categories.md`, in en/de/tr/ar/fa. The docs-shots manifest was re-pinned; the PNGs are byte-identical.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A stalled image route (TCP accepted, no bytes) cost 60 s per directive. With up to 100 directives per sync, that could freeze the sync loop for ~100 min, and every image directive would be posted `failed`, so the owner would have to re-save each one by hand. | **Fixed.** A transport-level failure is now `errImageFetchUnreachable`. After the first one in a tick, no further fetch is tried in that tick. Each affected directive gets no result post: it stays pending, and the next tick retries it. A cloud *answer* (non-200, oversize, checksum) is still a real failure. Regression test `TestTickStalledImageRouteLeavesDirectivesPending`: I reverted the fix and it failed with `stalled=3 (want 1)`; restored, it passes. |
| 2 | nit | On a DB error the thumb file stays on disk while the row write fails. | Accepted. The order is the same as the till's own upload (#1189). A later clear or re-save repairs it. |
| 3 | nit | The hash cache is keyed on size + mtime. A same-length replacement within one mtime tick keeps the old hash. | Accepted. ext4 has ns mtime; only a vfat/exFAT data dir could hit it. |
| 4 | nit | The non-200 message shows only the status code, not the cloud's error code. | Accepted. Optional polish. |
| 5 | nit | `safeCategoryID` guards item ids too, so a legacy id containing `.` reads as "not on this till". | Accepted. Same as the till's own `/api/catalog/item/image` guard. |
| 6 | process | No review record yet. | This file. |
| 7 | info | `guard-deadcode-baseline.sh` fails locally on `internal/logging/file.go`. | Not caused by this diff: the file is untouched, and the only uses are in `cmd/unitill-desktop`, which needs GTK headers this container lacks. CI decides. |

## Verified

- The reviewer re-checked four TDD claims by breaking the code and watching the test fail, then restoring it:
  - checksum mismatch;
  - oversize body;
  - unknown id refused before any fetch;
  - a replaced photo re-pushes the snapshot.
- `-race` is clean on the new cloudsync and pages tests.
- Path traversal, `MkdirAll`, body close and bounds, and bearer-token leakage were all checked (details in the review hand-back).
- The refactored till upload and remove handlers keep their error behaviour.
- Full gate after the fix: `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues), `go test ./...`, and the ci.yml build-job guards.
- An earlier full run hit one failure in `internal/plugins` (untouched). It was a test bug that races its own deadline: filed as ut-docs#3299, and it passes 5/5 in isolation.
- **Not verified:** a real driven run against a live ut-cloud with a real my. upload, and an actual satellite pulling the file.

## Cross-repo

- ut-cloud `claims.DirectiveMinTillVersion[set_catalog_image]` was the placeholder `0.30.7`, but tills up to v0.30.12 don't have this directive. It is corrected to **0.30.13**, the next till patch release, in a separate ut-cloud PR.
- The cloud does not ingest the reported `image_sha256` yet. That is ut-docs#3298.

## Verdict

Safe to merge once CI is green.
