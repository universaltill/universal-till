# Code review: chunk loadShopItems' batched lookups (ut-docs#2451)

**Date:** 2026-09-21
**Card:** ut-docs#2451 — follow-up finding from the independent review of
ut-docs#2318 (sell screen All-tab chunking fix)
**Complexity:** medium — Sonnet built, Opus reviewed (one round)

## What shipped

`internal/pages/self_order_shop.go`'s `loadShopItems` — the self-order
kiosk browse-grid data source — called three repo lookups
(`ModifierRepo.ItemIDsWithModifiers`, `CatalogRepo.ItemIDsWithVariants`,
`CatalogRepo.ItemCurrentPrices`) with the WHOLE active-item id set in one
unchunked call each, the identical bug ut-docs#2318 fixed in
`ui.ButtonStore.LoadAllActive` (`internal/ui/buttons.go`). SQLite's
bind-variable ceiling (32766; `ItemIDsWithModifiers` binds 2 args/id via a
UNION) means it starts failing past ~16,383 active items — worse than
`LoadAllActive`'s pre-fix state, since `loadShopItems` discarded that
error outright (`_`) rather than even logging a warning: a kiosk tile
needing a modifier prompt would silently add straight to the cart with no
prompt once a shop's catalog crossed that size. Money/UX-correctness bug,
not just performance, on the customer-facing kiosk this time.

**Fix:**
- Exported `internal/ui/buttons.go`'s three previously-unexported helpers
  (`chunkStrings`→`ChunkStrings`, `mergeMapInto`→`MergeMapInto`,
  `allActiveIDChunkSize`→`AllActiveIDChunkSize`) so `loadShopItems` could
  reuse the exact same chunking pattern `LoadAllActive` already uses,
  rather than duplicating it — `internal/pages` already imports
  `internal/ui` directly in a dozen other files, so this added no new
  cross-package dependency.
- `loadShopItems` now chunks all three lookups into batches of 500 ids,
  merging per-chunk results, with a chunk's error now logged (previously
  silently swallowed for the modifiers lookup) rather than the whole
  kiosk grid degrading on one failure.

## TDD

New test file `internal/pages/self_order_shop_chunking_test.go`:
`TestLoadShopItems_BeyondSQLiteBindLimit`, mirroring
`internal/ui/buttons_all_tab_chunking_test.go`'s
`TestLoadAllActive_BeyondSQLiteBindLimit` — seeds 16,400 active items
(past the empirically-confirmed 16,384 break point), with marker items
wired to a real modifier group (one in chunk 0, one in a later chunk —
see review finding 1 below), a real variant, and a real price-history
override, asserting `loadShopItems` resolves all of them correctly and a
plain item still gets its base price with no false-positive flags.

Confirmed red→green myself before requesting review: ran the new test
against the original unchunked code (via a scratch revert of the three
touched files, keeping the new test), got a real failure —
`HasModifiers=false` on the first item, want `true` — matching the exact
silent-degradation failure mode ut-docs#2451 describes. Restored the fix,
confirmed all new + existing tests green, full repo `go test ./...`
green, `gofmt`/`go vet`/`golangci-lint`/`guard-data-access.sh`/
`guard-i18n.sh` all clean.

## Independent review — round 1

Opus subagent, fresh context, isolated worktree, told to review the diff
adversarially with no prior discussion in context, and to independently
re-verify the TDD claim itself (a scratch revert-then-restore in its own
throwaway worktree, not the shared checkout).

**Verdict: safe to merge, after one recommended test strengthening.**
Re-verified the TDD claim independently (own revert, confirmed the same
real failure, restored, confirmed green) and ran the full toolchain
itself (`gofmt`, `go vet`, `golangci-lint` — both scoped and whole-repo,
`go build ./...`, `go test -count=1 ./...` whole repo uncached, plus
`guard-data-access.sh`/`guard-kiosk-engine.sh`/`guard-i18n.sh`/
`guard-help-topics.sh`/`guard-help-drift.sh` and others) rather than
trusting the dev pass's own run — all clean.

Findings:
1. **MEDIUM, real, fixed** — the test's only positive modifier-flag
   assertion was at chunk 0 (`markerMod` = index 0), the one lookup that
   actually breaks unchunked at n=16,400. Demonstrated with a real
   experiment: injecting `break` right after the first chunk's
   `MergeMapInto(hasMods, m)` in `loadShopItems` (i.e. silently dropping
   every chunk past the first, 97% of the catalog) left the test
   green — it could not tell "chunked correctly" from "only chunk 0
   worked, chunk 1+ silently dropped." Fixed by adding a second modifier
   marker in a later chunk (index 12000, chunk 24) with its own
   `HasModifiers=true` assertion. Re-verified myself: re-injected the
   same `break`, confirmed the strengthened test now fails on the new
   chunk-24 marker; restored, confirmed green again.
2. **LOW, real, fixed** — the test's doc comment overclaimed that all
   three markers "prov[e] correctness at every chunk position," but
   `ItemIDsWithVariants`/`ItemCurrentPrices` bind only 1 arg/id and don't
   actually break until ~32,766 items — at n=16,400 those two markers are
   forward regression guards, not reproductions of the reported bug (the
   `#2318` precedent test's own comment already makes this same
   distinction). Comment corrected to say so explicitly, and to note why
   the second modifier marker exists.
3. **LOW, real, out of scope** — `AllActiveIDChunkSize`/`ChunkStrings`
   read oddly at the `loadShopItems` call site (nothing to do with
   buttons/`LoadAllActive`); the SQLite-bind-budget concept belongs next
   to `internal/data`'s `inPlaceholders`, not a UI package. Filed as
   ut-docs#2453 (p3) rather than folded in — pure placement/naming
   cleanup on code this diff only just introduced, cheaper as its own
   small follow-up than churning this PR twice.
4. **Nit, applied** — `hasMods`/`hasVariants` were pre-sized to
   `len(ids)` (up to 16,400 map buckets) for maps that typically hold a
   handful of entries on real catalogs, on every kiosk grid load
   including Pi-class hardware; `LoadAllActive`'s own equivalents use
   unsized `map[string]bool{}`. Changed to match.
5. **Nit, not applied here, out of scope** — `ItemIDsWithModifiers`'
   error is still discarded via `_` in `ButtonStore.Load` (the cashier
   sale screen) and `SearchSellable`, a higher-traffic surface than
   either already-fixed call site; both sets are bounded so there's no
   bind-limit bug there, only the silent-swallow half of the pattern.
   Filed as ut-docs#2454 (p2, since it's the primary sale screen) rather
   than folded in, per the same out-of-scope-finding convention #2318's
   own review round used.

Also independently checked and confirmed clean: the chunking is
semantically safe (all three lookups are pure id-filtered per-id-row
reads with no cross-chunk aggregation, so per-chunk merge is exactly
equivalent to the unchunked result); empty-input/nil-map behavior is
unchanged; no leftover unexported references anywhere in the repo after
the `ui` package rename; no file writes / no `money.Money` / no
cwd-relative paths in this diff (recurring bug classes this pipeline
watches for); the self-order kiosk `Engine`-vs-`KioskEngine` isolation
guard passes (this diff never touches `common.Deps.Engine`); no real
client/shop name used in the new test's seed data; and this diff needs no
`web/help/` update — zero templates/locales/routes touched, and at normal
catalog scale the rendered grid and modifier-picker flow are unchanged
(`TestSelfOrderShop_ModifierFlow` still passes), with the fix only
*restoring* already-documented behavior above the ~16,383-item threshold,
not introducing anything new for a shop owner to read about.

Findings 1, 2 and 4 applied before merge. Findings 3 and 5 filed as
follow-up cards (ut-docs#2453, ut-docs#2454) rather than folded in, per
the reasoning above.

## Test plan

- [x] `gofmt -l .` — clean
- [x] `go vet ./...` — clean
- [x] `golangci-lint run ./internal/pages/... ./internal/ui/...` — 0 issues
- [x] `go build ./...` — clean
- [x] `go test -count=1 ./...` (whole repo, uncached) — all green
- [x] `bash scripts/ci/guard-data-access.sh` — clean
- [x] `bash scripts/ci/guard-i18n.sh` — clean (no strings touched)
- [x] `bash scripts/ci/guard-kiosk-engine.sh` — clean (no `Engine` reference)
- [x] TDD: new test confirmed failing against the original code (real
      SQLite bind-limit repro, not a mock), confirmed passing after the
      fix — verified independently by both the dev pass and the reviewer,
      each in a separate scratch revert
- [x] Independent review (Opus, fresh context, isolated worktree); real
      findings applied (test strengthened to catch a demonstrated
      chunk-1+ blind spot, comment corrected, map pre-sizing matched to
      precedent); genuinely out-of-scope findings filed as
      ut-docs#2453/#2454 rather than silently dropped
- [x] No visible/UI surface touched — screenshot check not applicable;
      existing `TestSelfOrderShop_ModifierFlow` handler-level test proves
      the real modifier-picker flow is unaffected at normal scale
