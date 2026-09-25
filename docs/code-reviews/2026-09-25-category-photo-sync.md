# 2026-09-25 — Category photos reach joined tills (ut-docs#2566)

## What shipped

- `internal/pages/sync_assets.go`: the LAN photo sync surface is now a
  table of scopes. `items` keeps its endpoints (`/api/sync/assets`,
  `/api/sync/assets/file`); `categories` (uploaded category photos, #2500)
  gets `/api/sync/assets/categories` and `/api/sync/assets/categories/file`.
  Separate endpoint pairs, not a query parameter: a newer replica asking an
  older primary for categories gets a 404 and skips, so it can never misfile
  item photos into the categories tree.
- Size cap (10 MiB, the upload cap) on both sides: the primary leaves bigger
  files out of the manifest; the replica skips over-cap entries, reads at
  most cap+1 bytes and keeps a body only when its length equals the manifest
  size (temp file + rename; `.sync-tmp` files are excluded from manifests).
- Change detector is size + mtime (`mod`, unix seconds, optional): the
  replica stamps each download with the primary's mtime, so a same-size
  replacement re-downloads. There's 2s of slack for FAT/exFAT rounding. A
  zero `mod` (older primary) falls back to size only.
- Manifest decode bounded to 4 MiB; `safePath` also refuses `:` (Windows
  drive-relative paths).
- The pull tick (`sync_admin.go`) calls `syncAssets` (all scopes; stops for
  the tick if the primary is unreachable). New paths added to the bearer-exempt
  list in `internal/auth/middleware.go` and `TestSyncPullPathsAreExempt`.
- Help: `web/help/{en,de}/multitill.md` says uploaded photos follow the main
  till. `web/help/img/manifest.json` refreshed (only the markdown hash
  changed). PNGs regenerated in this container differed only by renderer
  noise and were not committed.
- ut-docs `architecture/lan-sync.md` gains "Increment D3c".

## Review

Independent review by Fable (the author was Opus 5.5), run in an isolated
worktree. It ran build, vet, the pages and auth tests, golangci-lint,
help-drift and i18n, 25× `-race` on the asset tests, and an HTTP probe of the
file endpoint with odd paths (directories, `index.html`, NUL, `%2e%2e`,
double-encoded paths). Directories redirect to an unrouted path and get a
404, so nothing lists a directory. It re-verified the TDD claim that
dropping the categories scope fails
`TestSyncAssets_CategoryPhotoReachesTheReplica`.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `TestSyncAssets_UnchangedFileIsNotFetchedAgain` never counted fetches, so re-downloading every tick still passed | Fixed: `runPull` counts `/file` requests and the test asserts 0 on the second tick. Re-verified: the mutation `e.Mod+10` now fails it. |
| 2 | minor | Exact-second mtime compare plus an ignored `Chtimes` error would re-download forever on FAT (2s granularity), silently | Fixed: `sameMod` with ±2s and a table test; a `Chtimes` failure is logged |
| 3 | minor | `C:x` passes `safePath` on Windows (not exploitable via `Join`) | Fixed: `:` refused, with a test |
| 4 | minor | Unbounded manifest JSON decode on the replica | Fixed: `io.LimitReader` at 4 MiB |
| 5 | nit | A photo deleted on the primary stays on replicas' disks (pre-existing for items; the row change hides it) | Accepted, documented in lan-sync.md D3c |
| 6 | nit | Test-only shims `listItemAssets`/`safeAssetPath`/`syncItemAssets` | Fixed before the review finished: removed (the deadcode guard also flagged them) and the old tests moved to the scope API |

## Verified

- Author mutation checks: removing the categories scope, the mtime compare,
  or the `n == Size` check each fails its own test.
- Full gate after the fixes: `gofmt`, `go build ./...`, `go vet ./...`,
  `go test ./...` all green; `golangci-lint run ./...` 0 issues; deadcode,
  docs-shots, help-drift, help-topics, i18n, data-access, kiosk-engine and
  page-http-error guards green. `make docs-shots` ran (124 passed).
- Not verified: a real two-device run. No e2e spec pairs two tills. The
  handler tests run the real primary mux and the real replica pull over HTTP,
  which is the closest layer available here. No UI surface changed, so there
  was no visual pass.

## Deferred

- ut-docs#2724 — local backup doesn't include uploaded photos (the "check
  backup/export" part of the card: it doesn't, by v1 design).

Verdict: safe to merge.
