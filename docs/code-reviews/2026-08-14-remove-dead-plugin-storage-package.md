# 2026-08-14 — Remove dead `internal/plugins/storage` package (ut-docs#28)

## What shipped

Deleted `internal/plugins/storage/cache_store.go` (`CacheStore` — `.part`
resumable-download tracking + disk quota) and its test. The package was
dead code: the live plugin download path
(`internal/plugins/download_manager.go` + `internal/plugins/installer_store.go`)
implements its own `.part`-file resumable-download and cleanup logic
independently, and nothing ever imported `CacheStore`.

No functional change — pure deletion, no other file touched.

## Independent review (fresh-context Sonnet subagent, per `complexity:easy` routing)

Verdict: **safe to merge as-is, no blockers.**

- Independently re-verified the dead-code claim: `grep` for both
  `plugins/storage` and `CacheStore` across all `*.go` files in the repo
  returns zero hits outside the deleted package.
- Confirmed the actual diff (against `origin/main`) is exactly the two
  file deletions — nothing else touched.
- Ran `go build ./...`, `go vet ./...`, `go test ./internal/plugins/... -count=1`,
  and the repo's guard scripts (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`)
  independently — all green.
- Confirmed no UI/i18n/help-manual impact (nothing under `web/` touched).
- Found this removal was already a pre-flagged, independently-corroborated
  backlog item: logged in `docs/QUEUE-ARCHIVE.md` (ut-docs repo) from the
  2026-07-30 coverage batch-4 pass, and the same "zero importers"
  conclusion was reached independently in
  `docs/code-reviews/2026-07-30-coverage-internal-ui.md` two weeks
  earlier — strong corroboration this isn't a fresh, unreviewed claim.
- Flagged two non-blocking items, both handled:
  1. Stale `specs/009-cloud-marketplace/tasks.md` (T006) and
     `quickstart.md` still described `cache_store.go` as a completed,
     current deliverable — annotated both in this branch to note the
     supersession rather than leaving a dangling reference to a deleted
     file.
  2. A **real but pre-existing, unrelated** gap: `installer_store.go`'s
     `DownloadToStore`/`InstallFromStore` flow has no disk quota or
     automatic eviction for staged bundles under
     `{pluginBaseDir}/downloads/` — cleanup today is only on successful
     install or a manual UI delete. `CacheStore` never actually closed
     this gap (it was never wired in), so deleting it changes nothing
     operationally, but the gap itself is real. Filed separately as
     ut-docs#749 rather than folded into this deletion.

## Verification beyond the automated suite

- Full gate run before the review: `go build ./...`, `go vet ./...`,
  `go test ./... -race` (full suite, all packages green), plus
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`.
- No real client/shop names, no secret-shaped literals — not applicable
  (pure deletion + two doc annotations).

## Safe-to-merge verdict

Yes. Dead-code removal, independently re-verified, zero behavior change,
full gate green.
