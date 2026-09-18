# Code review — NaN/Inf delta and qty corrupting basket totals (ut-docs#2383)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2383 (`complexity:easy`, `bug`, `p2`)
- **Branch:** `fix/2383-nonfinite-line-delta`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing, `MODEL-ROUTING.md` — the one case
  where "different model" relaxes to "different instance"), isolated in
  its own git worktree.
- **Verdict: SAFE TO MERGE.** No findings.

## The bug

`internal/pages/pos_api.go`'s `/api/pos/line` handler and its self-order
twin `internal/pages/self_order_shop.go`'s `/api/self-order/line` both
parse a `delta` form param with `strconv.ParseFloat`, which parses the
literal strings `"NaN"`, `"Inf"`, `"-Inf"` with no error. Both handlers'
negative-clamp (`if qty < 0`) is `false` for `NaN`, so a non-finite delta
passed straight through: the basket line's `Qty` became `NaN`/`±Inf` and
the whole basket's lineTotal/subtotal/total rendered as `£0.00` despite
visibly holding items. The absolute `qty` path had the same class of gap
for `+Inf` specifically (`f >= 0` rejects `NaN` by luck, since `NaN >= 0`
is `false`, but `+Inf >= 0` is `true`).

Not exploitable from the shipped UI today (the +/- stepper only ever
sends literal `1`/`-1`), but `/api/self-order/line` is auth-exempt and
reachable by any anonymous LAN client (ADR-0020), making it the more
exposed of the two.

## What shipped

- `internal/pages/pos_api.go` and `internal/pages/self_order_shop.go`:
  the existing invalid-delta 400 branch now also rejects
  `math.IsNaN(delta) || math.IsInf(delta, 0)`, in both handlers
  symmetrically. The absolute-`qty` branch in both handlers gained
  `&& !math.IsInf(f, 0)`, preserving the pre-existing convention that an
  invalid absolute qty is silently treated as unset (voids the line via
  `qty := 0.0`'s zero value) rather than a new 400 — only the `+Inf` gap
  is closed, `NaN` was already excluded there.
- New regression tests: `TestLineHandler_NonFiniteDeltaRejected` (table
  over `NaN`/`Inf`/`+Inf`/`-Inf`) and
  `TestLineHandler_AbsoluteQtyRejectsPositiveInfinity` in
  `internal/pages/pos_api_test.go`; the mirrored
  `TestSelfOrderShop_NonFiniteDeltaRejected` and
  `TestSelfOrderShop_AbsoluteQtyRejectsPositiveInfinity` in
  `internal/pages/self_order_shop_test.go`.
- Updated the stale comment in `pos_api.go` that used to describe the
  NaN/Inf gap as a "known, pre-existing gap... tracked separately" — it's
  fixed in this same change now.

## What the independent review found

Read both handlers in full context (not just the diff hunks), then ran
the actual gate: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./internal/pages/...` (0 issues),
`go test ./internal/pages/... -run 'TestLineHandler_|TestSelfOrderShop_'`
(all green, 98 subtests), then the full `go test ./...` (every package
green), `guard-i18n.sh` (confirms the `"invalid delta"` string is
pre-existing/unchanged, no new key needed), `guard-data-access.sh`
(confirms no SQL touched).

**TDD claim independently re-verified, done for real**: reverted just the
two source files to `main` in an isolated worktree (tests left at the fix
version), reran the four new tests — all failed with the exact symptom
the ticket describes (200 instead of 400; `£0.00` rendered totals; and an
incidental further finding, a `NaN` qty also corrupting an unrelated
`basket-count` int cast to `-9223372036854775808` downstream — not a
new bug, just further evidence a non-finite qty propagates badly and
reinforces rejecting it at the boundary). Restored the fix, confirmed the
working tree matched the reviewed commit byte-for-byte, reran everything
green.

**Checked and cleared explicitly**: `math.IsInf(x, 0)` correctly catches
both signs; `strconv.ParseFloat` already rejects out-of-range literals
like `1e400` pre-fix (not a gap this fix needed to cover); `Qty` is
`float64` throughout `internal/pos` by deliberate pre-existing design
(weighed items need fractional quantities) — never `money.Money`, and
this diff doesn't touch the discount/money path at all, so the
quantity-vs-money-type distinction holds; no filesystem writes in the
diff (`os.MkdirAll`/`paths.Data(...)` class of bug doesn't apply); no
other caller of the affected code paths exists outside the two handlers
and their own tests; a `-Inf` delta now 400s instead of being silently
clamped to the existing valid `qty < 0 → 0` floor as it was pre-fix — a
deliberate, in-scope behavior change (uniform rejection of all
non-finite deltas) with no real caller impact, since the shipped UI never
sends anything but literal `1`/`-1`; the qty `<input>`'s HTML5
`pattern="[0-9]+"` is client-side-only cosmetic validation, confirming
the server-side fix here is the actually load-bearing one since both
endpoints are directly POST-able. New tests have real assertions beyond
status code (qty unchanged on reject; line voided via the existing
`UpdateLine`/`UpdateLineByKey` qty-zero-voids behavior) and were proven
to genuinely fail pre-fix.

No UI surface changed (backend validation only, same pre-existing error
text), so no `web/help/` topic or screenshot update applies.

## Explicitly deferred

Nothing — narrowly scoped, complete for what ut-docs#2383 asked for.
