# Code review: chunk LoadAllActive's batched lookups (ut-docs#2318)

**Date:** 2026-09-21
**Card:** ut-docs#2318 — follow-up finding from the independent review of
ut-docs#2294 (sell screen All tab)
**Complexity:** medium — Sonnet built, Opus reviewed (one round)

## What shipped

`ui.ButtonStore.LoadAllActive` (`internal/ui/buttons.go`) — the sell
screen's All-tab data source, added by ut-docs#2294 — called three repo
lookups (`ModifierRepo.ItemIDsWithModifiers`, `CatalogRepo.
ItemIDsWithVariants`, `CatalogRepo.ItemCurrentPrices`) with the WHOLE
active-item id set in one unchunked call each. Each builds a SQL `IN (...)`
clause with one bind arg per id (`ItemIDsWithModifiers` uses 2 args/id via
a UNION over two SELECTs). SQLite's bind-variable ceiling — empirically
confirmed against this repo's `modernc.org/sqlite` v1.58.0 build via a
binary search (`SELECT 1 WHERE 1 IN (...)` with N bound args): fails at
exactly 32767 args, i.e. `SQLITE_MAX_VARIABLE_NUMBER = 32766` — means
`ItemIDsWithModifiers` starts failing past ~16,383 active items. Worse,
its error was silently discarded (`hasMods, _ = s.modRepo.
ItemIDsWithModifiers(...)`), so an item needing a modifier prompt would
render as a plain add-to-basket tile with no prompt at all, silently, once
a shop's catalog crossed that size — a money/UX-correctness bug, not just
a performance one.

**Fix:** chunk all three lookups into batches of 500 ids
(`allActiveIDChunkSize`, via a new `chunkStrings` helper) and merge
per-chunk results with a new generic `mergeMapInto[K, V]` helper. A
chunk's error is now logged (previously silently swallowed for the
modifiers lookup) and that chunk's items fall back individually, rather
than the whole active catalog degrading on one failure. 500 × 2 args/id =
1,000 binds — comfortably under the ceiling with room for further catalog
growth.

## TDD

New test file `internal/ui/buttons_all_tab_chunking_test.go`:
- `TestChunkStrings` / `TestMergeMapInto` — pure unit tests pinning the two
  helpers' boundary behavior (empty input, exact multiple, remainder, size
  1) with no DB involved.
- `TestLoadAllActive_BeyondSQLiteBindLimit` — seeds 16,400 active items
  (past the empirically-confirmed 16,384 break point) with three marker
  items (first item / item 499 at the first chunk boundary / last item in
  a short final chunk) wired to a real modifier group, a real variant, and
  a real price-history override respectively, asserting `LoadAllActive`
  resolves all three correctly and a plain item still gets its base price
  with no false-positive flags.

Confirmed red→green myself before requesting review: reverted
`buttons.go` to `HEAD` (git stash, not the new helpers), ran an ad-hoc
version of the same repro directly against the original code —
`HasModifiers=false` on the first item (want `true`), with `LoadAllActive`
returning **no error** — matching the exact silent-degradation failure
mode ut-docs#2318 describes. `HasVariants`/`Price` resolved correctly at
n=16,400 even pre-fix (those two lookups bind only 1 arg/id, so they don't
break until ~32,766 items) — consistent with the issue's own reported
table. Restored the fix, confirmed all new + existing tests green.

## Independent review — round 1

Opus subagent, fresh context, isolated worktree, told to review the diff
adversarially with no prior discussion in context.

**Verdict: yes-with-fixes.**

Re-verified the TDD claim independently (its own repro against `HEAD`,
confirmed `HasModifiers=false`/no error, then confirmed green against the
fix) and ran the full toolchain itself (`gofmt`, `go vet`, `golangci-lint`,
`go build ./...`, `go test ./...` whole repo) rather than trusting the
dev pass's own run.

Findings:
1. **HIGH, real, out of this diff's scope but flagged as pre-existing** —
   `internal/pages/self_order_shop.go`'s `loadShopItems` (the self-order
   kiosk browse-grid loader) has the identical unchunked +
   error-silenced bug in `ItemIDsWithModifiers`, at the same ~16,384-item
   threshold — on the customer-facing kiosk instead of the cashier sell
   screen. Filed as ut-docs#2451 (p1) rather than folded into this PR: a
   different file/subsystem, pre-existing (not introduced by this diff),
   and the established convention on this pipeline (e.g. ut-docs#2294's
   own review round filing #2432/#2435 as separate cards) is to file an
   adjacent-but-distinct finding rather than widen the diff under review.
2. **MEDIUM, real, fixed** — the test's own doc comment overclaimed that
   pre-fix, all three lookups (not just modifiers) lost data at n=16,400;
   variants/prices bind only 1 arg/id and don't actually break until
   ~32,766 items, so at n=16,400 they were forward regression guards, not
   reproductions of the reported bug. Comment corrected to say so
   precisely.
3. **LOW, real, out of scope** — `internal/pages/catalog/handlers.go:806`
   has the same shape (`ItemCurrentPrices`, unchunked, error silenced)
   over the admin catalog list; breaks later (1 arg/id, ~32,766 items) and
   is admin- rather than sell/kiosk-facing. Filed as ut-docs#2452 (p3).
4. **Nit, not applied** — `chunkStrings`' doc says "size must be > 0" but
   nothing enforces it. Unreachable via the one caller (a hardcoded
   `const`), so left as documentation rather than adding a guard for a
   scenario that can't occur.
5. **Nit, not applied** — three near-identical chunk+merge loops; reviewer
   judged the current form more readable than a shared generic wrapper
   that would still need a per-lookup warn string.
6. **Nit, applied** — `chunkStrings(itemIDs, allActiveIDChunkSize)` was
   being recomputed three times over identical input; hoisted to one
   `idChunks := chunkStrings(...)` reused by all three loops.

Also independently checked and confirmed clean: chunk boundary math (no
off-by-one at 500/1000/16,400), `ids[:end:end]` correctly prevents chunk
slices from aliasing each other's backing array, partial-chunk failure
cannot produce inconsistent per-item state (flags are independent per
item; degrading only the failed chunk is strictly better than the old
all-or-nothing), the common case (<500 items) still issues exactly one
call per lookup with an unchanged query shape, and `Load()` (the
small, designer-bounded quick-button path) correctly needs no chunking
and was left untouched.

Findings 2 and 6 applied before merge. Findings 1 and 3 filed as
follow-up cards rather than folded in, per the reasoning above.

## Test plan

- [x] `gofmt -l .` — clean
- [x] `go vet ./internal/ui/...` — clean
- [x] `golangci-lint run ./internal/ui/...` — 0 issues
- [x] `go build ./...` — clean
- [x] `go test ./...` (whole repo) — all green
- [x] TDD: new tests confirmed failing against the original code (real
      SQLite bind-limit repro, not a mock), confirmed passing after the fix
      — verified independently by both the dev pass and the reviewer
- [x] Independent review (Opus, fresh context, isolated worktree); real
      findings applied (comment correction, loop-computation hoist);
      genuinely out-of-scope findings filed as ut-docs#2451/#2452 rather
      than silently dropped
