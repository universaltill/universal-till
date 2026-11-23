# Review record: tender commit point (ut-docs#6923)

Scope: BA items 1, 3, 4 (item 2, last-sale receipt on resync, is ut-docs#6925).

## Change
- `pos.Service`: `BeginAbandon/EndAbandon` (counter) and
  `BeginTenderCommit/EndTenderCommit`. Commit stops new abandons, drains
  admitted ones (bounded by `tenderDrainTimeout`), then re-checks the attempt id.
- `completeTender`: commit point replaces the last `abandonedUnder()` check;
  released right after the sale's `engine.Reset()` and on every error path.
- Abandon routes wrapped in `abandonOp` (reset, hold, resume) or gated inline
  (`/open-orders/resume`, `/open-orders/counter/open`, redirect to
  `pos.tender.in_flight`). Authorize-phase abandons (#6602) are unchanged.
- Kiosk checkout claims its basket engine (`eng.TryBeginTender`) instead of
  `selfOrderCheckoutInFlight`; type renamed `keyedInFlight` (tap legs).
- i18n: no new key; meta description of `pos.tender.in_flight` broadened.

## Risks noted
- A slow abandon (cross-till resume claim) can hold the drain for up to 10s;
  the tender then refuses as abandoned and reverses captured legs (safe side).
- A new abandon route must call `BeginAbandon`; reviewer greps
  `parkCurrentBasket(|resumeHeldSale(|ResetAndSnapshotIf(` callers.
- Table-session sweep can still evict a session engine mid kiosk checkout
  (unchanged, out of scope).
