# Code review: fix main's build after PR #1317 (catalog handlers still called relocated ui.ChunkStrings/MergeMapInto)

**Date:** 2026-09-22
**Card:** universaltill/ut-docs#2453 (follow-up; PR #1317 itself already closed it)
**Author:** scrum-master pipeline (`lane:cloud-41`), on behalf of the pipeline owner
**Reviewer:** independent fresh-context Opus subagent, isolated worktree

## What broke and why

PR #1317 (this same lane, same cycle) relocated `ChunkStrings`/`MergeMapInto`/
the chunk-size constant from `internal/ui` to `internal/data`, and updated
the two call sites known at that PR's branch point
(`internal/ui/buttons.go`, `internal/pages/self_order_shop.go`). A third
call site, `internal/pages/catalog/handlers.go`, was added by PR #1316
(ut-docs#2452) — merged *after* #1317's branch point but *before* #1317
itself merged — and also called `ui.ChunkStrings`/`ui.AllActiveIDChunkSize`/
`ui.MergeMapInto`. Neither PR's own review (nor this session's own
supersession/CI-live-check before merging #1317) caught it, because #1317's
diff was green in isolation against its own branch point and the live CI
re-check ran on that same isolated branch — the interaction with #1316's
independently-merged content only surfaced on `main` itself, immediately
after the merge (`golangci-lint`'s typecheck: `undefined: ui.ChunkStrings`
etc. in `internal/pages/catalog`).

## What changed (this fix)

`internal/pages/catalog/handlers.go`, one commit, +2/−3:
- `ui.ChunkStrings(itemIDs, ui.AllActiveIDChunkSize)` → `data.ChunkStrings(itemIDs, data.IDChunkSize)`
- `ui.MergeMapInto(currentPrices, p)` → `data.MergeMapInto(currentPrices, p)`
- the now-unused `internal/ui` import removed (`internal/data` was already imported in this file for `data.TopLevelForFilterChips`)

Pure mechanical reference fix, zero behavior change — `data.IDChunkSize`
(500) is numerically identical to the old `ui.AllActiveIDChunkSize`, and
`ChunkStrings`/`MergeMapInto`'s bodies are unchanged from #1317's own
relocation.

## Independent review (fresh-context Opus, isolated worktree)

**Verdict: PASS, no blocking findings.** The reviewer independently:

- Verified the pushed branch's diff against `main`'s pre-merge parent is
  exactly the two-symbol/one-import change described above, nothing else.
- Confirmed the premise: at `main`'s broken commit, `git grep` found
  `ui.ChunkStrings`/`ui.AllActiveIDChunkSize`/`ui.MergeMapInto` referenced
  only from `internal/pages/catalog/handlers.go`, with the symbols
  themselves living only in `internal/data/chunk.go` — a genuine break, not
  a false alarm.
- Repo-wide grep for the old `ui.*` symbol names post-fix — zero remaining
  code references (only historical prose in `docs/code-reviews/*.md`).
- Read the full enclosing function in `handlers.go` and confirmed the
  local `data := map[string]any{...}` shadow (line 833) is declared after
  every package-`data` use in that function (lines 788/814/820) — no
  scoping/shadowing bug, and the chunk loop is straight-line code with no
  closure-capture concern.
- Confirmed `data.IDChunkSize == 500`, matching the old
  `ui.AllActiveIDChunkSize`, and that `ChunkStrings`/`MergeMapInto`'s
  bodies are byte-identical to what #1317 already relocated.
- Independently ran (not trusting this session's own report):
  `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`
  (`/usr/local/bin/golangci-lint`, v2.5.0, matching CI's version) — all
  clean, `0 issues`. `go test -count=1 ./internal/pages/catalog/...
  ./internal/data/... ./internal/ui/...` — all green.

**Two non-blocking notes** (not applied, out of scope for a build-fix):
1. The local `data` variable shadowing the `internal/data` package import
   inside the same function is a pre-existing, now slightly more
   load-bearing trap (a future edit moving a `data.*` call below line 833
   would fail confusingly). Renaming the local (e.g. `tmplData`, matching
   the file's own `pdata` at line 249) would remove it.
2. Flagged that this review record itself was still owed at the time of
   review — addressed by this file.

## Verification beyond the independent review

- This session: `go build ./...`, `gofmt -l internal/pages/catalog/handlers.go`
  clean; `go vet ./internal/pages/catalog/... ./internal/data/...
  ./internal/ui/...` clean; `go test ./internal/pages/catalog/...
  ./internal/data/... ./internal/ui/...` all green;
  `/usr/local/bin/golangci-lint run ./...` (full repo, matching CI) — 0
  issues, confirming the specific `main`-breaking typecheck error is gone
  and nothing else regressed.

## Root-cause note for future cycles

This is the concrete "green in isolation, red after merge" case
`PR-SWEEP.md` already warns about, but on a *same-cycle* self-authored PR
rather than an interaction with someone else's independent work. The gap:
before merging #1317, this session's own supersession/live-CI check ran
against #1317's own branch (which only had the two call sites #1317's own
branch point knew about) — it never re-scanned `main`'s current tree for
*new* call sites of the symbols being moved that landed on `main` after
#1317 branched. A relocation/rename PR's pre-merge check should grep
`main`'s current tip for the old symbol names, not just the PR's own diff,
when the PR's branch point is not `main`'s current tip.

Verdict: **SAFE TO MERGE.**
