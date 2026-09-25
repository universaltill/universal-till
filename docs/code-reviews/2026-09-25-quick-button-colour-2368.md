# Review: heartbeat quick_buttons carry item_id + color (ut-docs#2368)

- **Date:** 2026-09-25 · **Lane:** lane:cloud-54
- **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, detached worktree)
- **Scope:** `internal/pages/cloudsync_wire.go` (`remoteQuickButtonsReport`),
  `internal/pages/cloudsync_wire_test.go`, `web/help/img/manifest.json`
  (surface hash only). Cloud half + full findings: ut-cloud
  `docs/code-reviews/2026-09-25-quick-button-colour-2368.md`.

## What shipped

Each `quick_buttons` heartbeat entry now also reports the tile's `item_id`
and its item's `color`. `color` is always present, and `""` means no colour,
so the cloud can tell an uncoloured item from an older till. The data comes
from what `ShortcutsRepo.LoadButtons` already joins; there's no new SQL, no
migration and no UI change on the till. The cloud panel uses it to show each
tile's applied colour and to recolour a tile's item with the existing
`update_item_details` directive (primary-only, `cloudUpdateItemDetails`).

## Findings

None on this half. The reviewer ran build, vet and the `internal/pages` and
`internal/cloudsync` tests (green) and `guard-docs-shots` (fresh).

## TDD

`TestRemoteQuickButtonsReport_CarriesItemIDAndColor` failed first
(`item_id = <nil>`). The reviewer re-verified it independently: it fails
with `cloudsync_wire.go` reverted and passes once restored.

## Gate

The author ran `go build ./...`, `go test ./...` (all green) and
`golangci-lint` (0 issues), and every `ci.yml` guard passed except
`guard-shellcheck-version`: shellcheck isn't installed in this container,
and no shell script changed. `guard-docs-shots` needed a surface-hash
refresh (`update-docs-shots-surface-hash.sh`), because the Go file sits
under `internal/pages/` but changes no rendered pixel. The commit carries
`Docs-Shots-Unchanged: true`.

**Verdict:** safe to merge.
