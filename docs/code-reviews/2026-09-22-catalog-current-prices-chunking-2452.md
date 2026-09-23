# Code review: admin catalog list ItemCurrentPrices unchunked + error-silenced (ut-docs#2452)

**Date:** 2026-09-22
**Card:** universaltill/ut-docs#2452
**Author:** scrum-master pipeline (`lane:cloud-41`), on behalf of the pipeline owner
**Reviewer:** independent fresh-context Sonnet subagent, isolated worktree (`complexity:easy` → Sonnet review, per `scrum-master`'s model routing)

## What changed

Found during independent review of ut-docs#2318. `internal/pages/catalog/handlers.go`'s
admin `/catalog` list handler called `repo.ItemCurrentPrices(r.Context(), itemIDs)`
with the WHOLE active-item id set in one unchunked call and discarded its error via
blank `_`. `ItemCurrentPrices` (`internal/data/catalog_repo.go`) binds 1 SQL arg per
item id, so it silently starts failing past SQLite's bind-variable ceiling
(empirically 32766 on this repo's `modernc.org/sqlite` build) — later than
`ItemIDsWithModifiers`' 2-args/id break point (#2318/#2451's ~16,383), but the same
shape: above that catalog size, every promotional/current price silently reverts to
`base_price` catalog-wide, with the error thrown away rather than even logged.

Same bug family as #2318 (`internal/ui.LoadAllActive`) and #2451
(`self_order_shop.loadShopItems`) — this is the third and last of the three sibling
cards #2318's own independent review spawned (#2454, the fourth, was a narrower
logging-only fix with no chunking need, already shipped separately).

Two files changed in `universal-till`:

1. **`internal/pages/catalog/handlers.go`** — reuses the already-existing,
   already-reviewed `ui.ChunkStrings`/`ui.MergeMapInto`/`ui.AllActiveIDChunkSize`
   helpers (exported by #2318, already reused by #2451) rather than re-implementing
   chunking. A chunk that fails now logs via this file's own existing
   `log.Printf("[catalog] ...")` convention (used a dozen times elsewhere in this
   file already) instead of switching to the `internal/logging` package the
   `internal/ui` sibling fixes use — deliberate, for local consistency; this file
   never imports `internal/logging` anywhere else. A failed chunk's rows keep
   falling back to `base_price` via the existing, unchanged fallback in
   `buildCatalogRows` (`internal/pages/catalog/row_oob.go`).
2. **`internal/pages/catalog/current_prices_chunking_2452_test.go`** (new) — seeds
   32,800 active items (past the 32,766 ceiling) and two `price_history` overrides
   (one in the first chunk, one in the last, short chunk), then asserts both resolve
   via the rendered `/catalog` page's `data-price="..."` attribute rather than
   silently falling back to `base_price`.

## TDD verification

Confirmed red pre-fix (both by the implementer and independently by the reviewer,
who reverted only the fix and re-ran the test from a clean worktree): the test
failed with "expected first-chunk item bulk-00000 to resolve its price_history
override (7) ... got no data-price=\"7\"" — a real assertion failure, not a compile
error. Confirmed green after restoring the fix.

## Independent review (fresh-context Sonnet, isolated worktree)

**Verdict: PASS, no blocking findings.** The reviewer independently re-verified the
TDD claim itself (reverted only `handlers.go`, re-ran the test, confirmed the
identical failure, restored, confirmed pass again) rather than trusting the
implementer's word. Also verified: `ui.ChunkStrings`/`ui.MergeMapInto` signatures
match the call site correctly; no nil-map/panic risk (`currentPrices` is
pre-initialized, and the loop `continue`s before touching a failed chunk's `nil`
result); the merge/fallback semantics match the #2318/#2451 precedent exactly
(a failed chunk's rows stay absent from the map, `buildCatalogRows` already treats
an absent id as "keep base_price"); the new test's chunk-boundary claims are sound
(`ListItems` orders by name, and zero-padded 5-digit item names sort
lexicographically = numerically, so item 0 really lands in chunk 0 and item 32799
really lands in the final short 300-item chunk); `data-price="..."` is emitted by
exactly one template location and, with all other 32,798 items at `base_price=100`,
cannot false-positive against anything else on the page; no pagination/slicing
truncates the item list before rendering; the `internal/logging` vs. local
`log.Printf` convention choice is judged correct (full local file consistency); no
real client/shop name or secret-shaped literal in the new test data; repository
pattern respected (`guard-data-access.sh` clean — no inline SQL added).

No non-blocking nits noted.

## Verification beyond the independent review

- `go build ./...`, `go vet ./internal/pages/catalog/...`, `gofmt -l internal/pages/catalog/`
  — all clean.
- `go test ./internal/pages/catalog/...` (full package) — green, 9.483s.
- `go test ./internal/ui/...` — green (unchanged, sanity-checked since this diff
  reuses its exported helpers).
- `golangci-lint run ./internal/pages/catalog/...` — 0 issues.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` — clean.
- Full-repo gate, `go test ./... -race`, run twice: the first run surfaced one
  timeout (`internal/plugins`, 600.04s, on a test in a different package this diff
  never touches); the second run surfaced four (`internal/data`, `internal/db`,
  `internal/pages`, `internal/plugins`, all timing out at exactly 600.0xx s — the
  Go default per-package timeout, not a specific failing assertion). None of these
  packages' source is touched by this diff (which only changes
  `internal/pages/catalog` and imports the already-existing `internal/ui`). Ruled
  out as environmental rather than a regression, methodically:
  - Every one of the four passes cleanly **without** `-race`
    (`internal/data` 165s, `internal/db` 27s, `internal/plugins` 130s, all green) —
    confirms no logical bug, only race-instrumentation overhead.
  - `internal/plugins`' specific timed-out test
    (`TestSyncLocales_LogsShadowedKeyCollisionDeterministically`) passes in isolation
    with `-race`: 5.70s.
  - `internal/data` **alone**, with `-race`, still hits the 600s timeout solo (not
    just when competing with the rest of the suite) — this is a pre-existing
    characteristic of this sandbox (race-instrumented migration test suites
    genuinely running past the default per-package timeout here), not contention
    from other packages, and not something this diff could plausibly cause or fix.
  - This mirrors the exact same "unrelated timeout in a different package,
    confirmed clean in isolation" pattern already documented and accepted in
    `docs/code-reviews/2026-09-21-modifier-lookup-error-buttons-2454.md` for this
    same sandbox — worth a human/infra look if it keeps recurring (this is now the
    second and third occasions it's been hit), but not blocking for this diff, and
    real CI (a real runner, not this constrained sandbox) is the authoritative gate
    for the full `-race` suite regardless.

Verdict: **SAFE TO MERGE.**
