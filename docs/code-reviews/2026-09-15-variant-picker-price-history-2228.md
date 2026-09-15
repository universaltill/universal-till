# Code review — variant picker shows stale (configured) price instead of the current price_history price (ut-docs#2228)

- **Date:** 2026-09-15
- **Ticket:** ut-docs#2228 (`complexity:medium`)
- **Branch:** `fix/2228-variant-picker-price-history`
- **Reviewer:** independent pass, different model (Opus) from the Sonnet
  implementation, per this card's `complexity:medium` routing.
- **Verdict: SAFE TO MERGE**, after the fix pass below. One blocker-class
  finding from the first review round was fixed and re-verified in this
  same round (money-correctness class — the process rule for a second
  full round is a re-review of the fix, not the whole diff again).

## The bug

`CatalogRepo.ItemVariantsFor` fed the sale-screen and kiosk variant
pickers a variant's raw **configured** `item_variants.price`, never
consulting `price_history`. `POSRepo.resolvePrice`/`ResolveCurrentPrice`
(what actually prices a basket line) DOES apply an active price_history
override. So whenever a scheduled/promotional price was active on a
variant, the picker could show one price (e.g. £3.10) while the basket
charged another (e.g. £2.50) — a number rendered as part of a picker
choice reads as a quote to the customer.

## What shipped

- `internal/data/catalog_repo.go`: new `ItemVariantsForSale`, a
  single-query (no N+1) counterpart to `ItemVariantsFor` — identical
  shape/ordering/active-only filter, except `PriceMinor` is resolved
  through the same price_history window `lookupPriceHistory` applies
  (correlated subquery, one round trip), falling back to the configured
  price when no active row exists. `ItemVariantsFor` itself is
  UNCHANGED — it still feeds the catalog admin grid (`row_oob.go`),
  which needs the configured price to edit, never a transient promotion.
- Three picker-rendering call sites now use `ItemVariantsForSale`:
  - `internal/pages/pos_modifiers_api.go`: `GET /ui/pos/modifiers` (the
    cashier tile's own picker).
  - `internal/pages/self_order_shop.go`: `GET /api/self-order/modifiers`
    (the kiosk equivalent).
  - `internal/pages/pos_api.go`: the `/api/pos/scan` parent-code guard
    (ut-docs#2227) — the suggestion-strip/manual-code-entry route into
    the SAME picker markup. Missed in the first pass (see below).
- Left unchanged, correctly: `resolveAndValidateModifiers`'s own
  `ItemVariantsFor` call (submit-time membership check — never reads
  `.PriceMinor`), and `self_order_shop.go`'s kiosk `/api/self-order/scan`
  guard (existence-check only, refuses rather than rendering a picker).
- Tests: `internal/data/catalog_repo_single_item_test.go` (repo-level:
  active/expired/absent price_history rows, plus a direct differential
  test against `POSRepo.ResolveCurrentPrice`), `internal/pages/
  pos_modifiers_variants_test.go` (cashier + kiosk picker HTTP tests),
  `internal/pages/pos_scan_variant_guard_test.go` (the `/api/pos/scan`
  redirect path).
- Filed as a separate follow-up, per this ticket's own acceptance
  criterion: ut-docs#2258 (the sale-screen/kiosk **tile** — a different
  code path, `internal/ui/buttons.go` / `internal/pages/
  self_order_shop.go`'s `loadShopItems` — has the identical defect, out
  of scope for this diff to avoid an N+1 risk that needs its own batched
  design).

## What the independent review found

**First pass verdict: NOT SAFE TO MERGE**, on one blocker.

### BLOCKER 1 — a third picker-rendering call site was missed

`internal/pages/pos_api.go`'s `/api/pos/scan` parent-code guard renders
the exact same `web/ui/partials/modifier_picker.html` markup that
`GET /ui/pos/modifiers` renders (via `renderModifierPicker`), but was
still calling `ItemVariantsFor`. Per its own comment, this is the guard
"the suggestion strip and manual code entry both post here" — an
ordinary cashier route, not a theoretical one. The reviewer reproduced
it with a scratch test showing the stale £3.10 rendered instead of the
active-override £2.30.

**Fix:** `ItemVariantsFor` → `ItemVariantsForSale` at that call site
(one line), plus a new regression test,
`TestScanAPI_ParentCodeWithVariants_ShowsCurrentPriceHistoryPriceNotConfiguredPrice`
(`pos_scan_variant_guard_test.go`), driving `POST /api/pos/scan` with an
active price_history row and asserting the resolved price renders, the
stale one does not.

### Independent TDD re-verification (this session, after the fix)

For all three call sites: reverted the fix (call-site line only),
confirmed the corresponding test fails with the exact defect described,
restored the fix, confirmed green again. Full `internal/...`/`cmd/...`
suite re-run clean afterward.

### Non-blockers — addressed this round

- **N2** (test comment overclaim): the cashier picker's HTTP-level test
  doc comment claimed to prove the rendered price "matches what the
  basket will actually charge" — that fixture's `stubResolver` hardcodes
  each code's price independent of `price_history`, so it only proves
  the handler wires the right repo method in. Softened the comment to
  say so explicitly and point at the real equality proof,
  `TestItemVariantsForSale_MatchesPOSRepoResolveCurrentPrice`
  (repo-level, both sides resolved for real).

### Non-blockers — noted, not fixed here (filed as follow-ups)

- **N1** (`price_history` has no index usable for a `variant_id`
  lookup — full scan per variant on every picker open, pre-existing but
  multiplied by this diff) and **N3** (the price_history tie-break on an
  identical `starts_at` is unconstrained; the two code paths currently
  agree because SQLite's plan happens to match, not because it's
  structural) — both filed together as ut-docs#2259, since both touch
  the same queries and the same follow-up change is the natural place to
  fix both.
- **N5** (the shared test fixture's hand-declared `price_history` table
  omits the production index, so repo-level tests run a different plan
  shape than production) — informational only, no correctness impact,
  not filed separately (covered by ut-docs#2259's own fix once it lands).

## Verified beyond automated tests

- `go build ./...` clean, `gofmt -l .` silent, `golangci-lint run ./...`
  — 0 issues.
- Full repo suite (`go test ./internal/... ./cmd/...`) green, both
  review rounds.
- All relevant CI guard scripts green: `guard-data-access.sh` (no inline
  SQL outside `internal/data`), `guard-i18n.sh` (no new/missing keys —
  this diff adds no new user-facing strings), `guard-kiosk-engine.sh`
  (kiosk path only ever touches `d.KioskEngine`), `guard-price-history-
  sync.sh` (no new caller of `AppendPriceHistoryItem/Variant`).
  `guard-deadcode-baseline.sh` fails on missing `webkit2gtk-4.1` C
  headers in this sandbox — confirmed it fails identically on `main`,
  environmental, not this diff.
- Reviewer independently wrote and ran an 8-case differential test
  (overlapping open rows, identical `starts_at` ties, future-only rows,
  boundary timestamps, alternate timestamp formats, expired-then-active,
  plus an item-level price_history row present alongside variant-level
  in every case) comparing `ItemVariantsForSale` against
  `ResolveCurrentPrice` directly — all 8 agreed exactly; item-level
  history correctly never leaks into a variant's resolved price.
- Reviewer enumerated every remaining `ItemVariantsFor` call site in the
  repo and confirmed each of the three left unchanged is correct for a
  specific, checked reason (admin-grid editing needs the configured
  price; the membership check never reads price; the kiosk scan guard
  never renders a picker at all).

## Explicitly deferred (not this card)

- The sale-screen/kiosk tile's identical defect (raw `base_price`,
  ignoring `price_history`) — ut-docs#2258.
- The `price_history(variant_id, starts_at)` index and the
  same-`starts_at` tie-break — ut-docs#2259.
