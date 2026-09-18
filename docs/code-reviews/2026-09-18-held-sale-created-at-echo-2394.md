# Held-sale sync: echo created_at from primary's upsert response (ut-docs#2394)

## What shipped

Closes the `created_at` half of the clock-skew class of bug ut-docs#2271
already fixed for `updated_at`. On a genuine first park (`h.CreatedAt`
blank, exactly what `hold_api.go`'s `parkCurrentBasket` sends), the
PRIMARY till's `POST /api/sync/held-sales/upsert` handler
(`internal/pages/sync_held_sales.go`) used to stamp a blank incoming
`created_at` with its own clock, insert the row, and never report the
stamped value back — so the REPLICA's own local mirror
(`heldSaleWriteThrough` → `mirrorHeldSaleFromPrimary`,
`internal/pages/held_sale_sync_proxy.go`) took a second, independent
`datetime('now')` read for its own insert. These two clock reads could
differ by up to ~1-2 seconds under real scheduling jitter between the two
HTTP-round-trip-separated writes — this is what produced ut-docs#2389's
observed 1-second flake (fixed there with a test-side tolerance).

Fix, mirroring #2271's own established pattern exactly:

- `syncHeldSaleUpsertResult` gained a `created_at` field. The primary's
  upsert handler now stamps a blank incoming `created_at` with its own
  clock (same as it already did for `updated_at`) and echoes the value it
  actually stored back on the wire.
- `upsertHeldSaleOnPrimary` returns the new `createdAt` string.
  `heldSaleWriteThrough` overwrites `h.CreatedAt` with the primary's
  reported value (guarded on non-blank, for a mixed-version rollout
  window against an old primary that never sends the field) before
  calling `mirrorHeldSaleFromPrimary`, so the local insert lands the
  exact same value instead of re-deriving its own.

## Independent review

Reviewed by a fresh-context Sonnet subagent (card complexity: easy, per
`scrum-master`'s model-routing table) with no visibility into the
implementation reasoning. Verdict: **safe to merge, no blocking
findings.**

The review specifically interrogated the one real asymmetry in this
design: `created_at` (unlike `updated_at`) is written on INSERT only and
deliberately left alone by the UPDATE branch of both
`HeldSalesRepo.Upsert` and `UpsertIfNewer` (ut-docs#1918). Echoing a
freshly-stamped `created_at` on an UPDATE path would be echoing a value
the database never actually stores. Tracing the only real caller
(`hold_api.go`'s `parkCurrentBasket`) shows this is not reachable in
practice: a blank `h.CreatedAt` occurs only on a genuine first park, and
that branch always mints a fresh id
(`fmt.Sprintf("hold-%d", time.Now().UnixNano())`) the primary cannot
already hold — so the primary's write is always an INSERT whenever the
stamping code fires. Every other caller (a re-park via
`HeldOrigin.CreatedAt`, or the table-move path via `heldSaleForResume`)
already carries a non-blank `CreatedAt`, so the new code is a no-op for
those regardless of insert/update. The fix is correctly scoped to the one
case where it's both needed and safe.

One purely theoretical, explicitly non-blocking note the review raised:
first-park id uniqueness relies on `time.Now().UnixNano()` never
colliding across tills, which is astronomically unlikely and not worth a
follow-up card.

## Verified beyond automated tests

- `go build ./...`, `go vet ./internal/pages/...` — clean.
- `gofmt -l` on all four changed files — clean.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- Full `go test ./internal/pages/...` — green (all sub-packages).
- **TDD claim independently re-verified by the reviewer**, not just
  taken on trust: the reviewer disabled the
  `if primaryCreatedAt != "" { h.CreatedAt = primaryCreatedAt }` guard in
  a scratch copy, re-ran the new regression test
  (`TestHeldSaleWriteThrough_FirstParkCreatedAtIsByteIdenticalOnBothSides`),
  confirmed it failed with the exact expected mismatch (a live local
  clock read vs. the fixed sentinel `2020-01-01 00:00:00` the fake
  primary answers with), then restored the guard and confirmed it passed
  again. The Dev/Tester pass that produced this diff had already done the
  same revert/restore check itself before handing off; the reviewer
  repeated it independently rather than trusting that report.
- The new regression test deliberately uses a fixed, clearly-artificial
  primary timestamp rather than a live clock read — a same-process,
  no-network unit test reverting the fix was observed to still pass by
  timing coincidence (both sides landing in the same wall-clock second)
  when tested against a real-clock fake primary, which would have made
  the test a false-pass tautology. Recorded in the test's own comment.
- Backward compatibility against a pre-#2394 primary (omits the
  `created_at` field entirely) is covered by extending the existing
  `TestHeldSaleWriteThrough_PreFixPrimaryOmittingUpdatedAtStillMirrors`
  mixed-version test with matching created_at assertions.
- Backend-only change (no UI, no templates, no locale strings, no help
  topics touched) — confirmed via diff scope, so i18n/UX/help-manual
  review sections don't apply.
- No real client/shop name used in test data (generic "Table 4"/"Table
  5").

## Safe-to-merge verdict

Yes. No findings deferred.
