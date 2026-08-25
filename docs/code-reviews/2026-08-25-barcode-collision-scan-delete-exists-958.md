# Code review: barcode scan/delete/exists collision resolution (ut-docs#958)

- **Date**: 2026-08-25
- **Card**: ut-docs#958 (`complexity:medium`)
- **Branch**: `fix/958-barcode-collision-exact-wins`
- **Author (build)**: Sonnet (inline, pipeline cycle)
- **Reviewer (independent)**: Opus, fresh-context subagent, worktree-isolated
- **Repos touched**: `universal-till` (this record), `ut-docs` (ADR-0059 amendment)

## What shipped

ADR-0059 documented but left unresolved a known asymmetry: `POSRepo.
ResolveScanLine` resolves a barcode collision (an explicit-`BarcodeType`
escape-hatch row for one item, colliding via zeroed-template
canonicalisation with a *different* item's genuine embedded-data label)
canonical-first, while `CatalogRepo.DeleteBarcode`/`BarcodeExists` resolve
the same collision exact-first — so scanning a code and then deleting it
could act on two different rows.

Two orderings were tried before deciding:

1. Reorder `ResolveScanLine` to exact-first (to match delete). **Rejected**:
   breaks `TestScanAPI_WeightEmbeddedLabel` (`internal/pages`), a shipped
   ADR-0059/ut-docs#934 acceptance criterion — a genuine weight-embedded
   scale label would stop resolving (and stop decoding its embedded
   quantity) whenever an unrelated item's plain code coincides with its
   zeroed template. A real money bug (silently wrong price/quantity at
   sale time).
2. Reorder `DeleteBarcode`/`BarcodeExists` to canonical-first (to match
   scan). **Rejected**: breaks `TestDeleteBarcode_
   ExactMatchWinsOverCoincidentalCanonicalCollision` (ut-docs#948 F6) —
   deleting an escape-hatch code would delete a *different, unrelated
   item's* row instead. Data loss on the wrong item, strictly worse than
   the ambiguity being fixed.

**Decision (ADR-0059 §6, `ut-docs` repo): keep both orderings exactly as
shipped, and document the asymmetry as deliberate** — scan protects money
correctness (a genuine embedded decode must never be shadowed);
delete/exists protect data safety (acting on a named code must never touch
a different item's row). No production code changed in `universal-till`;
`internal/data/pos_repo.go` is byte-identical to `main`. Changes are:

- `internal/data/catalog_repo.go`: `DeleteBarcode`'s doc comment rewritten
  to state the asymmetry is intentional (was: "tracked as a Backlog
  follow-up, not resolved here").
- `internal/data/pos_repo_scanline_test.go`: new test,
  `TestScanDeleteExists_CollisionResolutionIsDeliberatelyAsymmetric`,
  constructing the actual collision and asserting all three properties in
  one run — scan resolves the canonical item with its embedded weight
  decoded; `BarcodeExists`+`DeleteBarcode` act on the exact escape-hatch
  row only, leaving the canonical row provably untouched; a final
  post-delete `BarcodeExists` call exercises the canonical-fallback code
  path once the exact row is gone.
- `ut-docs/adr/0059-barcode-symbology-registry.md`: new §6 documenting the
  decision, including the two rejected unifications and why, plus a noted
  future path (write-time collision prevention in `AddBarcode`) if true
  end-to-end coherence is ever wanted — explicitly out of scope here.

## Independent review findings

The review actually built and ran the suite, instrumented the barcode
registry directly to confirm the collision is genuine (not contrived), and
mutation-tested both rejected unifications against the real test suite to
verify the stated trade-offs are real, not asserted. Verdict: **safe to
merge**, judgment call on documenting-rather-than-unifying endorsed as
correct on the merits. Four non-blocking findings, all fixed before
commit:

1. A test comment claimed `BarcodeExists` finds the escape-hatch row "via
   canonical fallback" — backwards; it finds it exact-first, before ever
   reaching the fallback. Fixed: comment corrected, and a new post-delete
   assertion added that genuinely exercises the fallback branch (the
   exact row is gone by that point, so only canonicalisation can still
   find the surviving row).
2. The AC's "exists ... verified by a test constructing the collision"
   clause was asserted but not actually pinned: with both rows present,
   `BarcodeExists` returns `true` under either ordering (its result is a
   plain OR of two lookups), so the original assertion couldn't
   distinguish exact-first from canonical-first. Fixed: added the
   post-delete assertion above for functional coverage of the fallback
   path, with an explicit comment noting this is coverage, not an
   ordering pin — that asymmetry is provably unobservable through
   `BarcodeExists`'s boolean return, unlike `Scan`'s resolved item or
   `Delete`'s which-row-survives outcome.
3. A test comment miscounted the canonical key's digit length
   (`"2412345000000"+cd`, implying 14 characters when the zeroed template
   — weight digits *and* check digit zeroed, not recomputed — is exactly
   the 13-digit string `"2412345000000"`). Fixed.
4. A comment describing what mutation-testing had shown went stale the
   moment the new test was added (it referred to "this package's own
   tests" not yet including itself). Fixed to describe the mutation
   result precisely, including against the new test itself.

## Verified beyond automated tests

- `go build ./...`, `gofmt -l .`, full `go test ./...` (twice — once
  before, once after the review's fixes) — all green. `go test ./...
  -race` genuinely hangs in this sandbox on an unrelated package
  (`internal/plugins`, wazero JIT compilation under heavy `-race`
  overhead, confirmed independent of this diff — the same package/tests
  pass cleanly in isolation and pass the full non-race suite); not a
  regression from this change.
- Guards run: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh` —
  all pass (this diff touches no `web/`, no page routes, no locale keys).
- Reviewer independently instrumented the barcode registry to confirm
  `escaped`'s raw code (`2412345000009`) genuinely differs from its own
  canonical form, and that `genuineScaleLabel`'s canonical form genuinely
  coincides with it (`2412345000000` both times) — not a contrived
  collision.
- Reviewer mutation-tested three variants (`DeleteBarcode` canonical-first,
  `ResolveScanLine` exact-first, `BarcodeExists` canonical-first) against
  the full test suite to confirm which properties are actually pinned and
  which aren't (finding 2 above came directly from this).
- No real client/shop name, no secret-shaped literal values (test items
  are `i1`/`i2`, `S1`/`S2`, generic names).

## Safe-to-merge verdict

Yes. No production-behavior change; the fix is entirely
documentation + a regression test formalizing an intentional, analyzed
trade-off. Both previously-shipped acceptance properties
(`TestScanAPI_WeightEmbeddedLabel`, `TestDeleteBarcode_
ExactMatchWinsOverCoincidentalCanonicalCollision`) remain green and are
now cross-referenced from the new test and the ADR so a future change
can't quietly collapse the asymmetry without a named test failure.

## Explicitly deferred

- **Write-time collision prevention** (reject a colliding `AddBarcode` at
  insert time instead of allowing two rows to coexist with an
  order-dependent meaning) — noted in ADR-0059 §6 as the actual path to
  true end-to-end coherence, explicitly out of scope for this card. Not
  filed as a new Backlog card: the collision itself is not yet reachable
  in production (needs ut-docs#935's settings checklist UI, unshipped),
  so speculative write-time validation work is deferred until that
  changes or a real shop report surfaces it.
