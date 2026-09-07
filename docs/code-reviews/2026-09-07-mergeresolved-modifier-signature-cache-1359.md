# Code review — memoize `mergeResolved`'s per-line modifier signature (ut-docs#1359)

**Date:** 2026-09-07
**Branch:** `fix/1359-merge-resolved-signature-cache`
**Card:** ut-docs#1359 — performance-audit follow-up, ut-docs#1317 finding A.3
**Reviewer:** independent fresh-context Opus subagent (no sight of the
implementer's reasoning)
**Verdict:** SAFE TO MERGE **after** the fix below. As submitted it was **not**
safe: the review found and proved a real money bug that the diff introduced.

## What shipped

`mergeResolved` re-derived every existing basket line's `ModifierSignature()`
(sort + join of modifier `OptionID`s) from scratch on every basket add — a till
with 40 modifier-bearing lines already rung up redid 40 fresh signature
computations on every subsequent tap.

The change adds two unexported fields to `BasketLine` (`modSig string`,
`modSigSet bool`) and an unexported pointer-receiver `cachedModifierSignature()`
that lazily memoizes the existing pure value-receiver `ModifierSignature()`
(left unchanged). `mergeResolved`'s two comparison sites now use the cached
method. A benchmark (`BenchmarkMergeResolvedManyModifierLines`) and a
restore-then-add regression test
(`TestRestore_ThenAddLineWithModifiers_MergesIntoCorrectSibling`) were added.

## Finding 1 — stale memo silently merges a plain line into a customized one (HIGH, money bug, FIXED)

**This was a regression introduced by the diff, not a pre-existing risk.**

The diff's safety argument was that `modSigSet` is `false` on every freshly-built
`BasketLine`, and that the only in-place overwrite of an existing line's
`Modifiers` is `mergeResolved`'s merge-combine branch, which is only reached
after the signatures were already proven equal.

The second half of that argument is correct. The first half is not, because it
omits the exported API surface. `Basket()` and `Lines()` hand out `BasketLine`
**values** copied out of `s.lines`, and by then those lines *do* carry a
populated memo — `mergeResolved` sets `modSigSet = true` on every line it scans,
and on the line it appends. So a caller that re-uses a handed-out line as the
`base` of a new `AddLineWithModifiers` call carries the **old** selection's
signature into `line := base`, and the subsequent
`line.Modifiers = append(...)` replaces the modifiers without touching the memo.
`mergeResolved` then compares the wrong key.

Concrete reproduction, entirely through the exported API (ring up a flat white
with an extra shot, then ring the same drink up again plain, starting from the
line you already have):

```go
s.AddLineWithModifiers(base, 1, shot)   // line 0: +extra shot, 370
reused := s.Basket().Lines[0]           // modSigSet=true, modSig="extra-shot"
reused.LineKey = ""
reused.PriceCents = 320                 // the plain price
s.AddLineWithModifiers(reused, 1, nil)  // plain — must be a NEW line
```

Observed on the submitted diff — one line, not two:

```
qty:2 PriceCents:3.20 LineTotal:6.40 Modifiers:[] modSig:extra-shot modSigSet:true
```

The customized line silently lost its extra shot **and** the 50c delta folded
into its price: wrong price, wrong receipt, wrong tax basis, wrong kitchen
ticket. Exactly the failure class the card called out as the thing to guard
against.

Verified this is a regression and not pre-existing: the same probe was run with
`internal/pos/service.go` reverted to `HEAD~1` (pre-cache) — **passes**. With the
diff's `service.go` — **fails**.

**Fix applied.** Rather than patch the one reachable call site and leave the trap
armed, the invariant is now enforced at the point of mutation:

- Added `func (l *BasketLine) setModifiers(mods []data.SelectedModifier)`, which
  assigns `Modifiers` and clears the memo.
- `AddLineWithModifiers` (`service.go`) now uses it — this is the actual fix; it
  sanitises **any** caller-supplied `base` regardless of what memo it carries.
- `mergeResolved`'s merge-combine branch also uses it. The memo provably survives
  there, so this is belt-and-braces: it makes the rule "no bare `Modifiers`
  assignment, ever" instead of a case-by-case argument that a later edit could
  quietly invalidate. Cost is at most one recompute of that one line on a later
  add — O(1), not O(n); measured as within benchmark noise.
- The `modSig`/`modSigSet` doc comment was rewritten from "safe by construction"
  into an explicit load-bearing INVARIANT, including the one hole `setModifiers`
  cannot close (in-place mutation of a `SelectedModifier.OptionID` through the
  backing array `mergeResolved` aliases). No code does that today —
  `grep -rn 'Modifiers\[' --include=*.go .` returns only test reads.

Two regression tests added to `internal/pos/modifiers_test.go`:
`TestAddLineWithModifiers_BaseFromBasketDoesNotReuseStaleSignature` (the
Service-level money bug) and `TestSetModifiers_InvalidatesSignatureMemo` (the
unit-level invariant, so a future bare assignment is caught even if no Service
path exercises it yet).

## What was checked and found clean

- **Every `.Modifiers` reference repo-wide** (`grep -rn '\.Modifiers'
  --include=*.go .`, not just `internal/pos`). Only three write sites exist on a
  `pos.BasketLine`: `service.go:514` and `service.go:607` (both now routed
  through `setModifiers`) and `data/pos_repo.go:6432`, which is a different type
  (`data.SaleDetail`). `internal/print/kitchen.go`, `internal/pages/pos_api.go`,
  `self_order_shop.go` and `kitchen_print.go` only read.
- **`internal/pos/hold.go` Snapshot/Restore.** `Restore` builds every line as a
  fresh composite literal, so `modSigSet` is `false` and the first read computes
  correctly. This is what the shipped
  `TestRestore_ThenAddLineWithModifiers_MergesIntoCorrectSibling` pins, and it is
  a legitimate test.
- **Every `s.lines` mutation** (append, index-assign, splice-delete) in
  `service.go`. `SetLineOrderType`'s `unit := src` copy-and-split carries the
  memo forward with `Modifiers` unchanged, so it stays valid; the
  `copy(s.lines[idx+2:], ...)` shift moves whole structs, memo included.
  `removeLocked`/`removeLineLocked`'s `s.lines[:0]` filter copies structs
  wholesale. All correct.
- **Where `base` comes from in production.** Both real callers
  (`pages/pos_modifiers_api.go`, `pages/self_order_shop.go`) get it from
  `ResolveBase` → `PriceResolver.Resolve`, and `ui/buttons.go`'s adapter builds
  fresh `pos.BasketLine{...}` literals. So no *current* production path hit
  finding 1 — it was a live trap in an exported API, not yet a live outage. That
  is why it is fixed rather than deferred: nothing in the type system or the
  tests would have caught the first caller to do the natural thing.
- **`scanCache`.** `cacheScan(code, resolved)` stores the value *before*
  `mergeResolved` could touch it (`mergeResolved` takes a value parameter), so
  cached entries carry `modSigSet == false`; and the scan path never reassigns
  `Modifiers`. Clean either way now.
- **Receiver types.** `ModifierSignature()` correctly stays a value method —
  `modifiers_test.go:82` calls it on the non-addressable composite literal
  `(BasketLine{}).ModifierSignature()`. All three `cachedModifierSignature()`
  call sites are on addressable values (a local parameter, a slice element, a
  local variable). Confirmed empirically that the pointer receiver is also a
  compile-time guard: a probe calling it on a map index failed to build with
  `cannot call pointer method cachedModifierSignature on BasketLine`. (It does
  not guard against calling it on an addressable *copy*, e.g. a `range`
  variable, which would silently memoize into a throwaway — a wasted-work
  hazard, not a correctness one.)
- **Concurrency.** Both new fields are only ever written inside `mergeResolved`,
  which is reached exclusively from `scanQty` / `AddLineWithModifiers`, both
  holding `s.mu`. `totalsSnapshot` copies lines out by value before releasing
  the lock, so the lock-free `computeTotals` never touches shared memo state.
  `go test -race ./internal/pos/... -count=1` clean.
- **JSON/wire format.** Unexported fields, so `encoding/json` skips both with no
  tag needed — the claim in the comment is correct; hold/resume payloads and API
  responses are unchanged.
- **Pipeline's two recurring bug classes.** Confirmed inapplicable rather than
  assumed: `grep` for `MkdirAll|paths.Data|os.WriteFile|os.Create` across
  `internal/pos/*.go` returns nothing. This is pure in-memory logic.
- **No UI/i18n/help-manual surface.** Confirmed, not assumed: the diff touches
  only `internal/pos/service.go` + two `internal/pos` test files. No templates,
  no `web/locales`, no `internal/ui`. UX/manual checklist correctly skipped.
- **No real client/shop names** in test data — `COFFEE`, `Flat White`,
  `Extra shot`, `SKU%03d` etc. are all generic.

## Verification beyond running the suite

The point of this section is that a passing suite proves nothing about whether
the tests *test anything*. Three deliberate-breakage runs were done:

1. **`cachedModifierSignature()` stubbed to return a constant.** 7 tests failed,
   including four that predate this diff
   (`TestAddLineWithModifiers_DifferentSelectionsDoNotMerge`,
   `TestRemove_LegacySKUMethod_DeletesAllLinesSharingSKU`,
   `TestRemoveLine_TargetsExactlyOneLineEvenWhenSKUsCollide`,
   `TestUpdateLineByKey_TargetsExactlyOneLineEvenWhenSKUsCollide`) plus the
   shipped restore test and both new ones. The existing modifier-merge coverage
   genuinely exercises the new cached path.
2. **`setModifiers` stripped of its invalidation** (i.e. the fix reverted, the
   memoization left in). **Exactly** the two new tests failed and nothing else —
   confirming both that they pin the fix precisely and that the pre-existing
   suite had a real gap here.
3. **Caching disabled entirely** (`return l.ModifierSignature()`) to get an
   honest before/after for the benchmark.

Restored after each; green.

## Benchmark assessment

`BenchmarkMergeResolvedManyModifierLines` (80 lines × 4 modifiers, every line a
distinct selection so nothing ever merges and the scan always runs to
completion) is a fair measurement: line and modifier construction is hoisted
above `b.ResetTimer()`, and it is not tautological — it exercises the real
`AddLineWithModifiers` → `mergeResolved` path rather than calling the signature
function in a loop.

Measured here (`-benchtime 200x -count 3`):

| variant | ns/op |
| --- | --- |
| caching disabled (baseline) | ~1,078,000 |
| as shipped | ~653,000 |
| after the `setModifiers` fix | ~649,000 |

~1.65× on this shape, and the fix does not erode it.

Minor, accepted as-is: `NewServiceWithResolver` and the `s.Basket()` assertion
sit inside the timed loop. That is constant overhead identical on both sides of
the comparison, so it dilutes the measured ratio rather than inflating it — the
real speedup of the scan itself is larger than 1.65×.

## Deferred (out of scope, deliberately not fixed here)

- `SetLineOrderType` (`service.go:1041,1046`) still calls the uncached
  `ModifierSignature()` in its own O(n) scan. Correct, just not memoized. It
  runs once per operator line-mode toggle rather than once per tap, so it is not
  the hot path the audit flagged, and pulling it into the cache would widen this
  diff's blast radius for no measured gain. Note for whoever picks it up: it is
  now the only place where a signature is derived rather than read, so if the
  two ever disagree, staleness is the reason.

## Gate

All run in this worktree after the fix:

| command | result |
| --- | --- |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l internal/pos/` | clean |
| `go test ./... -count=1` (whole repo) | all packages pass |
| `go test -race ./internal/pos/... -count=1` | pass (25.6s) |
