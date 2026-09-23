# Code review: relocate ChunkStrings/MergeMapInto/chunk-size constant from internal/ui to internal/data (ut-docs#2453)

**Date:** 2026-09-22
**Card:** universaltill/ut-docs#2453
**Author:** scrum-master pipeline (`lane:cloud-41`), on behalf of the pipeline owner
**Reviewer:** independent fresh-context Sonnet subagent, isolated worktree (`complexity:easy` → Sonnet review, per `scrum-master`'s model routing)

## What changed

Found during independent review of ut-docs#2451. `internal/ui/buttons.go`'s
`AllActiveIDChunkSize`/`ChunkStrings`/`MergeMapInto` were exported by
ut-docs#2451 so `internal/pages/self_order_shop.go`'s `loadShopItems` could
reuse them, but the constant encodes a SQLite bind-variable budget — a
concept that belongs next to `internal/data`'s `inPlaceholders` (where the
bind args it budgets for are actually constructed), not in a UI package.
Pure relocation, no behavior change:

1. **`internal/data/chunk.go`** (new) — `ChunkStrings`, `MergeMapInto`, and
   the constant (renamed `IDChunkSize`, since it's now shared by two
   unrelated callers and the old name was buttons/`LoadAllActive`-specific).
   `ChunkStrings`' implementation is byte-for-byte unchanged from the
   original.
2. **`internal/data/chunk_test.go`** (new) — `TestChunkStrings`/
   `TestMergeMapInto`, moved as-is from `internal/ui`.
3. **`internal/ui/buttons.go`** — the three symbols deleted, its one call
   site (`LoadAllActive`) updated to `data.ChunkStrings`/`data.IDChunkSize`/
   `data.MergeMapInto` (the file already imported `internal/data`).
4. **`internal/pages/self_order_shop.go`** — its four call sites updated the
   same way; the now-unused `internal/ui` import removed.
5. **`internal/ui/buttons_all_tab_chunking_test.go`** — `TestChunkStrings`/
   `TestMergeMapInto` removed (moved to `internal/data`); its
   `TestLoadAllActive_BeyondSQLiteBindLimit` regression test untouched.

## Independent review (fresh-context Sonnet, isolated worktree)

**Verdict: PASS, no blocking findings.** The reviewer independently:

- Diffed `ChunkStrings`' old vs. new body char-for-char and confirmed it's
  unchanged.
- Grepped the whole repo for `ui\.ChunkStrings|ui\.MergeMapInto|ui\.AllActiveIDChunkSize`
  — zero stale references.
- Ran `go build ./...` and `go vet ./...` itself (both clean) and confirmed
  no import cycle (both packages already imported `internal/data` before
  this change) and that the now-dead `internal/ui` import in
  `self_order_shop.go` was actually removed, not just claimed.
- Force re-ran (`-count=1 -v`) `TestChunkStrings`, `TestMergeMapInto`, and
  `TestLoadAllActive_BeyondSQLiteBindLimit` — all pass; confirmed the last
  one ran for real (0.18s, not `-short`-skipped).
- Diffed `buttons_all_tab_chunking_test.go` and confirmed exactly the two
  moved test functions were removed and nothing else (the file's remaining
  `strings` import is still legitimately used by the surviving test's
  `strings.Builder`).
- `gofmt -l` on all five touched files — clean. `golangci-lint run` on the
  three touched packages — 0 issues.
- Checked for a name collision on `IDChunkSize` (none) and read the new
  file's doc comment for staleness (correctly updated from "exported
  ut-docs#2451" to "moved here ut-docs#2453", keeping the original #2318
  provenance).

No findings to apply.

## Verification beyond the independent review

- `go build ./...`, `go vet ./internal/data/... ./internal/ui/... ./internal/pages/...`,
  `gofmt -l` on the five touched files, `golangci-lint run` on the three
  touched packages — all clean.
- `go test ./internal/data/...` (`TestChunkStrings`/`TestMergeMapInto`),
  `go test ./internal/ui/...` (including
  `TestLoadAllActive_BeyondSQLiteBindLimit`), `go test ./internal/pages/...`
  — all green.

Verdict: **SAFE TO MERGE.**
