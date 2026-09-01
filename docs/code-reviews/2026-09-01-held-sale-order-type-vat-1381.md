# 2026-09-01 — Held sales carry no OrderType, silently reverting VAT basis on resume (ut-docs#1381)

## What shipped

`internal/pos/hold.go`'s `BasketSnapshot` (the JSON payload persisted for a
held/parked sale) carried `TableID`/`TableLabel` but no `OrderType` field
at all. `Service.Restore` calls `resetLocked()` before restoring the
snapshot, and `resetLocked()` zeroes `s.orderType` back to `""` (dine-in)
— so a basket held while set to **Takeaway** silently resumed as
**Dine in**. Order type isn't cosmetic: `EffectiveLineTaxRateBP`/
`recomputeTotals` use it to pick each line's dine-in vs. takeaway VAT rate
(§12 UStG, Germany pilot) via the pluggable `TaxRateAsker` interface. A
compliance-relevant correctness bug, not a display one.

### The fix
- `BasketSnapshot` gains `OrderType string` (`json:"order_type,omitempty"`),
  populated in `Snapshot()`, restored directly in `Restore()` (not via
  `SetOrderType`, since the Takeaway-clears-table invariant is enforced
  structurally in `Restore()` itself — see review finding below).
- **Also found while writing the regression test, not in the original
  report**: `SnapshotLine` never carried `BasketLine.TaxCodeID` (added
  later in ut-docs#1351, `json:"-"`) — so a restored line always lost its
  tax code, defeating a `TaxRateAsker` that keys its takeaway/dine-in
  override by tax code (the real `ut-plugin-tax-de` mechanism), regardless
  of whether `OrderType` itself round-trips. Added alongside `OrderType`,
  same treatment.
- `internal/pages/hold_api.go`: the held-sales strip's "Move table"
  control is now hidden for a held Takeaway order (mirrors
  `Service.SetTable`'s existing no-op-while-Takeaway rule, which
  ut-docs#1355 already applies to the live basket but explicitly couldn't
  extend to held sales before this fix, for lack of a reliable order type
  to key on). `POST /api/pos/held/table` itself is also gated — the real
  enforcement point, not just the strip's UI soft-gate.
- `web/help/en/sell.md` / `tables.md`: one sentence each, stating that
  hold/resume preserves Dine-in/Takeaway and that a held Takeaway order
  has no Move-table control.

## Independent review (Opus, different model, isolated worktree)

**Verdict: yes, with one required fix** — the core fix and its TDD claims
held up under independent re-verification (build/vet/gofmt clean; all
touched-package tests pass; `guard-data-access.sh`/`guard-i18n.sh`/
`guard-help-topics.sh`/`guard-compliance-claims.sh` all green; both new
`pos`-package assertions — `OrderType`'s restore line, and `TaxCodeID`'s
restore line independently — confirmed to genuinely fail with the exact
expected symptom when reverted, then pass again restored; the two new
`pages`-package tests confirmed load-bearing the same way).

**Findings and disposition:**

1. **BLOCKING — fixed in this same commit.** `guard-docs-shots.sh` failed:
   `web/help/en/sell.md`'s prose changed but the screenshot manifest
   wasn't regenerated. Ran `make docs-shots`, confirmed green. (Ran twice
   more after the `tables.md` wording fix and the `hold_api.go`
   review-response edits, since each one re-touches the "app surface"
   hash the guard checks.)
2. **HIGH, fixed in this same commit — fail-open vs. fail-closed
   inconsistency.** `POST /api/pos/held/table`'s new Takeaway gate
   originally read `if held, found, err := repo.Get(...); err == nil &&
   found && ... { tableID = "" }` — an error from `repo.Get` silently
   skipped the gate entirely, while the very next check (`IsTableFree`)
   already fails *closed* on its own error. Since this gate is "the actual
   enforcement point, not just the UI soft-gate" (its own comment's
   words), failing open was the wrong default. Fixed: a `repo.Get` error
   now rejects the request (`renderHeldStrip` + return), matching the
   `IsTableFree` convention right below it.
3. **HIGH, fixed in this same commit — structural invariant, defense in
   depth.** `Restore()`'s comment claimed the Takeaway-clears-table
   invariant was "already applied when the snapshot was taken, so there's
   nothing left to re-derive" — not quite true for its one production
   caller: `hold_api.go`'s Resume handler overwrites `snap.TableID`/
   `TableLabel` from the `held_sales.table_id` DB column (the
   authoritative post-move source, ut-docs#820) *after* unmarshalling.
   Combined with finding #2 (before it was fixed), a `repo.Get` error
   could in principle produce `orderType=takeaway && tableID != ""` — a
   state unreachable through the normal `SetOrderType`/`SetTable` API.
   Traced the actual blast radius: `computeTotals` never reads `tableID`,
   so no wrong total; the visible effect would have been a takeaway sale
   carrying a table on the basket header/kitchen ticket/persisted
   `SaleInput.TableID` and a table shown occupied on the floor plan — real
   but narrow. Fixed anyway, for free: `Restore()` now re-asserts the
   invariant itself (`if s.orderType == OrderTypeTakeaway { tableID,
   tableLabel = "", "" }`) right after assigning all three fields,
   independent of what any caller already did. No-op in the normal case;
   harmless on a legacy pre-#1381 payload (`OrderType` decodes to `""`).
4. **Note, fixed in this same commit.** `heldSaleOrderType`'s JSON decode
   error was silently swallowed, diverging from this file's own
   established convention (the Resume handler renders a failure toast on
   the same class of error). Now `log.Printf`'d; still defaults to `""`
   (dine-in) since a payload that won't decode can never be resumed
   anyway, so there's nothing more useful to *do* with the error here —
   only to stop hiding it.
5. **Note, fixed in this same commit.** The new `pos`-package test pinned
   the tax *rate* (700bp) but not the tax *amount* — for a VAT-basis
   compliance ticket, the number that ends up on the receipt is what
   actually matters. Added an assertion on `Basket().Tax` (70p on a
   £10.00 line at the 7% takeaway rate, not dine-in's 19%/£1.90).
6. **Note, fixed in this same commit.** `web/help/en/tables.md` described
   "move a held order onto a different table" with no dine-in qualifier.
   Two words added ("move a held **dine-in** order").
7. **Note, fixed in this same commit.** Neither original `pages`-package
   test drove the real end-to-end hold→resume HTTP path (both asserted
   against hand-seeded `held_sales` rows). Added
   `TestHoldThenResume_PreservesOrderType`, which drives
   `POST /api/pos/hold` → `POST /api/pos/resume` for real and asserts
   `dp.Engine.OrderType()` afterward — proves `Snapshot()`/`Restore()`'s
   contract actually survives a real `json.Marshal`/`Unmarshal` round trip
   through the DB column, not just the `pos` package's own direct
   marshal/unmarshal test.
8. **Note — accepted, not fixed here.** A held sale already parked in a
   pilot shop's DB before this ships has no `order_type` in its payload
   and will still resume as dine-in (the field simply never existed for
   it) — true from deploy forward, not retroactively. Worth a release-note
   line, not a code change.
9. **Note — accepted, not fixed here (pre-existing, not meaningfully
   worsened).** `web/help/{fa,tr,ar}/sell.md`'s equivalent sentence
   doesn't even carry the ut-docs#1355-era dine-in/takeaway paragraph yet
   — those locales were already behind `en` before this diff added one
   more sentence to the gap. `guard-help-topics.sh` only enforces topic
   *presence* per locale, not prose parity, so nothing mechanical catches
   this; a separate localization card, not a blocker.
10. **Note — deferred as a new Backlog card, not fixed here.** The held
    strip gives no positive visual signal that a takeaway order has no
    table to move — indistinguishable from the pre-existing
    "no free tables right now" case. Filed as ut-docs#1387 (mirror the
    existing `held-chip-table` pattern with a takeaway indicator chip).

**The six adversarial questions the review asked, and the answers**
(full detail in the review transcript): direct `s.orderType` assignment in
`Restore()` is safe for every legacy/normal case, with the one narrow
combined-failure hole closed by finding #3; `""` on decode error/absence
is correct (dine-in genuinely *is* `""` everywhere in this codebase, no
information is lost) — the fix was purely about not swallowing a genuine
decode failure silently, not about the default itself; `repo.Get`'s added
cost is a trivial single-row PK lookup, and no new TOCTOU — `payload` is
write-once for a held sale, so the read/write window this adds cannot
race a concurrent order-type change; the hand-written test JSON fixtures'
`order_type` key was cross-checked byte-for-byte against the real struct
tag, so the new tests aren't passing for the wrong reason; help markdown
is legitimately exempt from `guard-i18n.sh` (confirmed from the guard's
own glob, not assumed) — localization is by directory, not `T` keys; and
the fix traced all the way through to the *persisted* sale (`pos_api.go`'s
tender path reads the same restored `orderType`/`TaxCodeID`), not just the
on-screen basket preview, in both tax-inclusive and tax-exclusive modes.

## Verified beyond automated tests

- TDD claims re-verified independently, twice, in the `pos` package: (1)
  reverting only `OrderType`'s restore line reproduces "restored
  OrderType() = "", want takeaway"; (2) reverting only `TaxCodeID`'s
  restore line (keeping `OrderType`'s) reproduces "restored effective rate
  = 1900 (blocked=false), want 700" — proving `TaxCodeID` is independently
  load-bearing for this exact test, not redundant with `OrderType`. Both
  restored to green after.
- The two new `pages`-package tests independently confirmed load-bearing:
  with both gates removed, `TestHeldTableHandler_TakeawayOrderIgnoresTableMove`
  fails showing the table actually moved, and
  `TestHeldStrip_NoMoveControlForTakeawayOrder` fails showing the rendered
  `<details class="held-move">` control.
- Traced the fix all the way to the persisted sale (not just the live
  preview): `pos_api.go`'s tender path reads `Engine.Lines()` (carrying
  the restored `TaxCodeID`) and `Engine.EffectiveLineTaxRateBP`/
  `Engine.OrderType()` (both now correctly restored) when building
  `SaleLineInput`/`SaleInput` — the same values that land on the receipt
  and the fiscal payload.
- `go build`/`go vet`/`gofmt -l .` clean; `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh` all green (the last one twice more, after the
  `tables.md` wording fix and the `hold_api.go` review-response edits).

## Safe to merge

Yes. One blocking CI-guard finding and two high-severity correctness/
robustness findings from independent review, all fixed and re-verified in
this same PR; five more informational notes fixed alongside them; two
genuinely out of scope, tracked (a new Backlog card, a noted pre-existing
localization gap) rather than silently dropped.
