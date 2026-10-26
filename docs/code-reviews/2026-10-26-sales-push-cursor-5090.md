# Review — sales push cursor loses same-second sales at a batch edge (ut-docs#5090)

**Date:** 2026-10-26 · **Lane:** `lane:cloud-41` · **Author:** Sonnet (Dev subagent) + orchestrator fix for finding 1 · **Reviewer:** Opus 5.5, fresh context, separate worktree

## What shipped

- `sync.push_cursor` is now `<created_at>|<id>` (the no-sale/cleared-basket
  precedent). `POSRepo.LocalSalesSince` / `CountLocalSalesSince` take
  `(afterCreatedAt, afterID)` and page by `(created_at, id)`, so a 50-sale
  batch ending inside one second no longer strands the rest of that second.
- A bare legacy cursor (join seed in `db/replica.go`, or a pre-upgrade value)
  parses as `(at, "")` and re-sends that one second once; the primary skips
  sales it has by id (`SaleExists`).
- Sale ids are random UUIDs and `created_at` is stamped before commit, so a
  later sale in the cursor's own second could sort before it. The push still
  sends the whole batch, but the cursor only advances to the last sale older
  than `salesPushSettle` (5 s); a held-back push schedules one follow-up kick
  of the push loop (`salesPushRekick`, 6 s) so the sync chip, the D3b stock
  gate and the fleet report don't read a just-sent sale as queued for a
  whole 30 s tick.
- All readers converted: `syncPushSales`, `replayUnsentJournal`,
  `countUnsentSales`, sync chip, D3b gate, fleet report (`countQueuedSales`).
- Contract `ut-docs/reference/contracts/pos-lan-sync-journal.md` → 1.12.1.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A clock stepped back behind already-stamped sales keeps every sale inside the window; each held push re-kicked 6 s later, re-POSTing up to 50 sales every 6 s until the clock caught up (no data loss, `SaleExists` skips). | **Fixed**: no re-kick when the newest batch sale is stamped in the future; the normal 30 s tick carries it. Test extended (fails without the guard: "no follow-up kick expected for a future-stamped sale, got [6s]"). |
| 2 | minor | `replica.go` still seeds a bare `nowUTC()`; a primary's own sale stamped in the join second is pushed back once and skipped by id. | Accepted — same as the no-sale `now|` seed; documented in `parseSalesPushCursor` and the contract. |
| 3 | nit | In normal running each sale is POSTed twice (immediate push + settle re-kick); one `SaleExists` per resend; each held push starts its own `AfterFunc` (non-blocking send, no leak). | Accepted — cheap, side effects all sit behind `SaleExists`. |
| 4 | nit | `SameSecondSmallerIDLaterIsNotLost` also passes on the old bare cursor (it re-includes the whole second); it guards the "composite without settle" mistake. | Accepted — the batch-edge and settle tests cover the rest. |

Also checked: RFC3339 `Z` second-precision stamps on local sales (journaled-in
rows are excluded by `till_id = ''`), so string compare is chronological;
`BEGIN IMMEDIATE` means commit follows the stamp well inside 5 s; the cursor
never moves backwards; no `t.Parallel` in `internal/pages`, so the package
clock/rekick seams are safe; no file writes or cwd-relative paths.

## TDD re-verified (reviewer, worktree)

- Old SQL: `TestSyncPushSales_BatchEdgeInsideOneSecondLosesNothing` fails
  "primary has 50 of 51 sales"; `TestLocalSalesSince_CompositeCursorPagesSameSecond`
  pages only two sales.
- Composite cursor without settle/re-kick: `SameSecondSmallerIDLaterIsNotLost`
  fails "primary has 1 of 2"; `CursorHeldInsideSettleWindow` fails (cursor
  advanced inside the window).
- All pass on the branch.

## Gate

`gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues),
`guard-data-access.sh`, every `ci.yml` guard (except `guard-deadcode-baseline`,
whose pinned x/tools can't load this container's go1.27 toolchain, and
`guard-shellcheck-version`, no shellcheck here — both environment, CI runs
them), `go test ./...` (all packages ok except `internal/plugins`, which hit
the default 10 m timeout under a concurrent review run — untouched package),
`-race` on every sync/rejoin/replaced-till/chip test file: no races.

## Verdict

Safe to merge. Not a UI change (no help topic affected). Sales an older
replica already skipped stay skipped unless they share its cursor's own
second; there are no real shops yet, so no recovery step is shipped.
