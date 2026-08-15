# Code review: a variant scanned by barcode is untenderable (ut-docs#744)

**Date:** 2026-08-15
**Author (Dev/Tester):** Claude (Sonnet), autonomous SDLC pipeline
**Reviewer:** Claude (Opus), independent subagent, isolated worktree
**Branch:** `fix/variant-barcode-scan-untenderable`
**Complexity:** medium

## What shipped

A variant resolved by barcode carries **both** `ItemID` (the parent item)
and `VariantID` (the specific variant) on `pos.BasketLine` — deliberately:
`internal/pages/tax_hook.go`'s `pluginTaxRateAsker.AskTaxRateBP` sends
`ItemID` to tax plugins via the `tax.rate.ask` payload even for a variant
line, and `internal/ui/resolver_test.go`'s `TestResolve_VariantBarcode`
already pins that shape. But six different call sites
(`internal/pages/pos_api.go`, `self_order_shop.go`, `sync_sales.go`,
`refund_page.go`, `inventory_api.go`) copy both IDs verbatim into
`pos.SaleLineInput`, and `internal/pos/sales.go`'s `validateLine` rejects
any line with both set (`"line cannot have both item_id and variant_id"`)
— the same invariant the `sale_lines`/`inventory`/`price_history`/
`stock_movements` CHECK constraints already enforce. Net effect: scanning
a variant's own barcode (not the base item's) made that line **completely
untenderable**, cashier or kiosk — a real offline-first/checkout-must-
never-fail violation (universal-till/CLAUDE.md).

**Fix:** normalize once, at the top of `CompleteSale`
(`internal/pos/sales.go`) — clear `ItemID` when `VariantID != ""`, before
`validateLine`/persistence. This is the single choke point every caller
goes through (`InsertSaleLine` has exactly one caller — `CompleteSale` —
confirmed by the reviewer), so one ~19-line change (15 logic + a
follow-up clarifying comment) fixes all six construction sites and every
non-barcode resolve path (shortcut/SKU/name-search) that has the same
both-set shape, without touching `BasketLine` or the tax-ask payload.

**Tests added** (TDD, confirmed failing pre-fix by both Dev and,
independently, the reviewer):
- `internal/pos/sales_test.go`:
  `TestCompleteSale_VariantLineWithBothIDsSetIsTenderable` — unit level,
  asserts `CompleteSale` succeeds and the persisted `sale_lines` row has
  `item_id` NULL / `variant_id` set, inventory decremented against the
  variant's own row.
- `internal/pages/pos_api_test.go`:
  `TestTenderHandler_VariantBarcodeScanIsTenderable` — handler level,
  drives the real `/api/pos/tender` HTTP route end to end.
- `internal/pages/ui_smoke_test.go`: added a variant + barcode + its own
  inventory row to the shared `seedForPages` fixture for the test above.

## Independent review — findings

Reviewed at Opus (this card's complexity:medium routing: Sonnet builds,
Opus reviews), in an isolated worktree, with an explicit brief to find
real problems rather than confirm the work. Full findings below;
verdict: **safe to merge as-is**.

1. **Placement (`CompleteSale` as the single choke point) — confirmed
   correct, on stronger grounds than the original design note.**
   `InsertSaleLine` has exactly one caller. All six `SaleLineInput{}`
   construction sites were checked; none needs `ItemID` to survive into
   the persisted row. The reporting layer (`pos_repo.go` lines 733, 804,
   950, 1063, 1188, 2140) already does
   `COALESCE(sl.item_id, iv.item_id)`-style joins through
   `item_variants`, i.e. reports were already written *expecting*
   variant-only sale lines — this fix produces exactly the shape the
   rest of the system assumed. Fixing at `CompleteSale` is also strictly
   better than a resolver-level fix would have been: `ResolveShortcutLine`
   returns both IDs on the shortcut, SKU-exact, and name-search paths
   too, not just barcode — a resolver fix would have missed three of
   those four paths; the `CompleteSale` choke point covers all of them.

2. **Real finding, non-blocking: an undocumented aliasing dependency.**
   `SaleInput` is passed by value but `Lines` is a slice, so
   `in.Lines[i].ItemID = ""` mutates the caller's own backing array —
   and two post-sale consumers in every handler
   (`publishStockAdjustedForSale` in `pos_api.go`,
   `warnIfStockNegative` in `sync_sales.go`) read `l.ItemID`/`l.VariantID`
   from that same slice after `CompleteSale` returns. The mutation is
   *necessary* there too (`CurrentQty`'s query only matches when exactly
   one ID is set — with both populated it can silently read the parent
   item's inventory row instead of the variant's), but it was relying on
   slice-aliasing implicitly. **Fixed**: added an explicit comment at the
   normalization site documenting why the mutation must be visible to
   the caller, cross-referencing the same fragility class already called
   out for `PaymentInput.TipAmount` in `pos_api.go`.

3. **Test fixture soundness — confirmed, with an extra check the
   reviewer ran beyond the brief.** The reviewer ran a wrong-field
   mutation test (clearing `VariantID` instead of `ItemID`) against both
   new tests; both caught it — the handler test specifically because
   `seedForPages` seeds *two* inventory rows (item-level `inv1` and
   variant-level `inv2`), so a sale against the wrong row still returns
   200 and only the `item_id`/`variant_id`/`qty==29` assertions expose
   the bug. No false-pass risk found.

4. **"No backfill needed" — confirmed on three independent grounds**:
   the CHECK constraints have been present since `001_init.sql` (not
   added by a later migration — no migration touching `sale_lines`
   relaxes or recreates it), `validateLine`'s both-set rejection has
   existed since the function was introduced, and `InsertSaleLine` has
   exactly one caller with no repo-level bypass (the LAN sync replay
   path goes through `CompleteSale` too). No bad row was ever
   persistable by any path — no migration/backfill required.

5. **The two recurring bug classes** (missing `os.MkdirAll`, cwd-relative
   path instead of `paths.Data(...)`) — confirmed not applicable; no
   file I/O anywhere in this diff.

6. **Scope check** — confirmed via `git diff --name-only`: exactly 4 Go
   files, nothing under `web/` or `web/help/`, no new user-facing
   string, so the UX-guidelines/help-manual checklist and i18n
   obligation don't apply. No real client/shop name in fixtures (Apple,
   Coffee, Cash, Main — all generic).

### Adjacent pre-existing bugs found — out of scope, filed separately

Neither is caused by or worsened in a new way by this change (variant
sales couldn't complete *at all* before this fix, so neither was
reachable), but both become live now that variant checkout works:

- **`internal/data/pos_repo.go`'s `resolvePrice` silently swallows a
  price-history-resolution error for the shortcut/SKU-exact/name-search
  variant paths** (which pass both `ItemID` and `VariantID` into
  `ResolveCurrentPrice`, which explicitly rejects that combination) and
  falls back to the item_variants base price — meaning a variant's
  `price_history` override is silently ignored on those three resolve
  paths (barcode is unaffected — it already passes `""` for itemID).
  Real money impact. Filed as a new Backlog card.
- **`internal/data/related_items_repo.go`'s "frequently bought together"
  query filters `WHERE sl.item_id IS NOT NULL`**, so variant sale lines
  never contribute — the only sale-line query in the codebase that
  doesn't `COALESCE` through `item_variants`. Minor, filed as a new
  Backlog card.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean (Dev, and independently by
  Reviewer in an isolated worktree).
- `go test ./...` (full module, no `-race`): 100% pass, run twice
  (Dev, then Reviewer independently).
- `go test ./internal/pos/... ./internal/pages/... ./internal/ui/... -v`:
  100% pass, including the untouched `TestResolve_VariantBarcode` (proof
  `BasketLine`'s intentional both-IDs-set shape was not disturbed).
- All 3 guard scripts (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`): pass, run by both Dev and Reviewer.
- **TDD re-verified independently, twice**: Dev reverted the fix, saw
  both new tests fail with the real `"line cannot have both item_id and
  variant_id"` error, restored, saw both pass. The Reviewer then
  independently repeated this in an isolated worktree (so no shared-
  checkout risk with the orchestrator — ut-docs#386) and additionally
  ran a wrong-field mutation to probe for false-pass risk (see finding
  3). Exact revert-mode failures the reviewer captured:
  ```
  sales_test.go:174: CompleteSale error: line cannot have both item_id and variant_id
  pos_api_test.go:260: expected 200, got 400: line cannot have both item_id and variant_id
  ```
- `go test ./... -race`: hits a pre-existing, already-tracked,
  **unrelated** flake in `internal/plugins` (wazero JIT/register-
  allocator crash under heavy parallel `-race` load — same signature as
  the already-open ut-docs#643/#750/#674). Confirmed unrelated: the
  specific failing test passes standalone without `-race` in ~2s; this
  diff touches only `internal/pos` and its own/`internal/pages` tests,
  zero overlap with `internal/plugins`; and the package hangs/crashes
  under `-race` in isolation on this same commit with the identical
  signature, so it isn't cross-lane interference — it's the package's
  own known race-mode flakiness. Not fixed here (out of scope, already
  tracked).

## Safe-to-merge verdict

**Yes.** Root cause fully traced and confirmed both by code reading and
by two independent TDD reproductions (Dev, then Reviewer). Fix is
minimal, centrally placed at the one point proven to be the sole choke
point for persistence, and verified not to silently break any of the
traced downstream consumers (stock movements, inventory, reporting,
receipts, audit log). One review finding (undocumented aliasing) fixed
with a clarifying comment; no logic change needed. Two adjacent bugs
found and filed separately, correctly out of scope for this ticket.

## Deferred / explicitly out of scope

- `resolvePrice`'s silent price-history-resolution swallow for variant
  shortcut/SKU/name-search resolves (new Backlog card, real money
  impact).
- `related_items_repo.go` excluding variant lines from "frequently
  bought together" (new Backlog card, minor).
- `internal/plugins`' pre-existing `-race`-mode flake
  (ut-docs#643/#750/#674, already tracked, unrelated to this change).
