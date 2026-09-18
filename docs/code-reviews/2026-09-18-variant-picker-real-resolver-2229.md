# Code review — variant picker tests exercise the real resolver (ut-docs#2229)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2229 (`complexity:medium`, `p3`, `source:review`)
- **Branch:** `fix/2229-variant-picker-real-resolver-tests`
- **Reviewer:** independent pass, Opus subagent (per this card's
  `complexity:medium` routing, `MODEL-ROUTING.md` — Dev on Sonnet,
  Review on Opus), isolated in its own git worktree (cleaned up on
  completion).
- **Verdict: SAFE TO MERGE** after fixes. Test-only change (zero
  production code touched), one accuracy blocker in a comment and two
  should-fix coverage gaps found and fixed; one low-severity duplication
  finding folded into a table-driven consolidation.

## The gap

Every existing POST test for the sell-screen variant picker
(`internal/pages/pos_modifiers_variants_test.go`) resolved codes through
`stubResolver` — a map hand-fed the exact `code -> BasketLine` mapping
each test needed. The cross-item guard in `resolveAndValidateModifiers`
(`internal/pages/pos_modifiers_api.go:124`,
`variantBase.VariantID != variantID`) exists specifically to catch a
**resolver** bug — a duplicate/ambiguous code silently substituting a
different variant's price — but a stub can never have a resolver bug by
construction, so that guard had never actually run against the real
four-tier resolution chain (`POSRepo.ResolveShortcutLineDecoded`).

## What shipped

- `setupVariantModifiersRealResolverTestDeps`: wires the real resolver
  (`ui.PriceResolverAdapter` over a real migrated SQLite catalog,
  mirroring `pos_scan_barcode_test.go`'s existing real-resolver pattern)
  instead of `stubResolver`.
- `TestScanWithModifiers_RealResolver` (table-driven): both shapes the
  card's acceptance criteria call for — a variant resolved by a real
  barcode (a checksum-valid EAN13, with EAN13 pinned as the only enabled
  symbology for determinism), and a variant with no barcode row at all,
  resolved by its own SKU. Asserts the resolved line's `SKU` field, not
  just `VariantID`/`PriceCents` — see F2 below for why that assertion is
  the one that actually matters.
- `TestScanWithModifiers_RealResolver_CrossItemGuardFires`: the card's
  actual headline claim. Reproduces a real way the real resolver can
  hand back the wrong row — `resolveSKU` checks `items.sku` before
  falling back to `item_variants.sku`, so a variant whose own SKU
  collides with a *different item's* SKU resolves to that other item —
  and proves the guard rejects it (500, zero basket lines added) rather
  than silently substituting the wrong price/label.
- `TestGetVariantLabel_PrefersBarcodeOverSKU`
  (`internal/data/catalog_repo_crud_test.go`): direct unit coverage that
  `GetVariantLabel`'s barcode-else-SKU fallback actually prefers the
  barcode when a variant has both — every existing `GetVariantLabel`
  test variant had no barcode at all, so nothing had ever exercised the
  barcode arm winning over the SKU arm.

## Independent review findings

**F1 — BLOCKER (accuracy), fixed.** The first draft's comment justifying
a fresh EAN13 fixture claimed the existing file's short `C-R`-style
barcode fixtures were "unlikely to match a real symbology at all." The
reviewer disproved this directly (`barcode.Default().Match` against the
default-enabled symbology set): `CODE128` is a deliberate permissive
catch-all and every one of the existing `C-S`/`C-R`/`C-L`/`T-1` codes
matches it. **AC#4's correct finding is "no fixture defect exists"** —
corrected the comment to say so and state the real reason for a fresh
fixture (deterministic symbology pinning, no unrelated fixture noise),
rather than leave a false claim about the barcode registry as a durable
record.

**F2 — should-fix, fixed.** The original barcode-shape test asserted only
`VariantID`/`PriceCents`, which are identical whichever tier resolved the
variant — SKU-fallback independently resolves the same variant via
`"COFFEE-R"`, so that alone couldn't prove the *barcode* tier won.
Added an assertion on the resolved line's `SKU`, which reads the seeded
barcode under the real chain and the SKU under a hypothetically-broken
barcode tier — verified by mutation (below).

**F3 — should-fix, fixed.** The reviewer's sharpest finding: neither new
test (at first) actually made the cross-item guard *fire* against the
real resolver — both were happy-path resolutions. Added
`TestScanWithModifiers_RealResolver_CrossItemGuardFires` per the
SKU-collision scenario above. The reviewer also confirmed something
worth recording: the existing *stub*-based
`TestScanWithModifiers_RejectsCrossItemVariant` passes for a completely
different reason — an earlier membership check
(`resolveAndValidateModifiers`'s "variant id not a member of item's
sellable variants" loop) rejects it before the resolver re-assertion
guard is ever reached. Confirmed independently here too (see Mutation
verification).

**F4 — low, folded in.** The two original positive tests were near-
identical (same form, same shape, differing only in variant/price).
Consolidated into one table-driven `TestScanWithModifiers_RealResolver`.

## Mutation verification (re-run personally, not taken on the Dev/Tester's word)

1. **`GetVariantLabel`'s fallback order** — inverted `if l.Code == ""`
   to `if sku != ""` in `internal/data/catalog_repo.go`:
   - `TestGetVariantLabel_PrefersBarcodeOverSKU` → **FAIL** (`got "S1-L"`
     instead of the barcode). ✅ load-bearing.
   - Both `TestScanWithModifiers_RealResolver` subtests → still PASS.
     Confirmed why: `resolveSKU` falls through to `resolveVariantSKU`,
     so `v-reg`'s own SKU `"COFFEE-R"` independently resolves to `v-reg`
     regardless of which arm won — the e2e tests alone cannot catch this
     class of fault; the unit test is what actually proves AC#3.
   - Reverted; all pass again.
2. **The cross-item guard itself** — short-circuited
   `variantBase.VariantID != variantID` to always-false in
   `pos_modifiers_api.go`:
   - `TestScanWithModifiers_RealResolver_CrossItemGuardFires` → **FAIL**.
   - `TestScanWithModifiers_RejectsCrossItemVariant` (the pre-existing
     stub test) → **still PASS** — independently confirming the
     reviewer's finding that it never reaches this guard at all; it's
     rejected earlier, by the sellable-variant membership check.
   - Reverted; all pass again.

Both mutations confirm the diff's own claims rather than assume them.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/data/...` (108s), `go test ./internal/pages/...`
  and its subpackages — all green, `-count=1` (not cache-masked).
- `gofmt -l` — clean.
- `bash scripts/ci/guard-data-access.sh` — pass (raw SQL is confined to
  `_test.go` seed statements, which the guard exempts, and to
  `internal/data` itself).
- `bash scripts/ci/guard-i18n.sh` — pass (no template/locale surface
  touched).
- No UI surface, no template, no `web/locales/*.json`, no `web/help/**`
  change — confirmed not needed (test-only diff, no user-facing
  behaviour change).
- No secrets, no real shop/client name in test fixtures (`itm-coffee`,
  `Flat White`, `v-reg`, `v-decaf`, `itm-water` — none real).

## Deferred / explicitly out of scope

None — the card's four acceptance criteria are all met by this diff as
reviewed above; no follow-up card filed.
