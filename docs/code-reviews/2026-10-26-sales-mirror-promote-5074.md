# Review: promote adopts the sales copy (ut-docs#5074 M6)

Part of ut-docs#5074 (design §9, AC9; ADR-0011 amendment point 7). Stacked
on M5. Touches the `sales` table and the Z-report window: **not to be merged
before the real-shop onboarding regardless; the copy must not be switched on
for a real shop until the follow-ups below are decided.**

## What shipped

- `POST /api/sync/promote`, right after the replica identity is cleared:
  - **Z floor** (`reports.eod_window_floor`, per till): the previous main
    till's last copied end-of-day close, when later than this till's own;
    `generateEOD` starts no earlier, so nothing that Z counted is counted
    again. Set first, so it holds even if the adoption stops.
  - **Adoption** (`SalesMirrorRepo.AdoptCopy`): every copied sale this till
    lacks goes into `sales` and its children verbatim, oldest first, 500
    per transaction, each sale under its own savepoint — one this till
    refuses (receipt number in use, a reference it lacks) is counted and
    stays in the copy, never half-written. No stock, voucher, receipt
    counter, audit-per-sale or hook. `till_id` = the origin; for the
    previous main till's own sales its register (the key its cloud sales
    aggregates used, so uploads replace rather than double). Voids the copy
    knows reach rows held as completed. Idempotent.
  - Not tied to the request (a closed browser does not stop it); an
    unfinished adoption is marked and resumed by the maintenance tick.
  - The change log stays quiet during adoption (this till's own kill switch
    is held on, then restored): nobody consumes those rows after a promote.
  - The promote answer says how many were adopted, voided, refused, and
    **how many sales before the previous main till's last close may be in
    no Z report** (this till's own sales that never reached it, and copied
    sales it received only after that close); an audit row
    `sales_adopted`; a problem while some are refused or it stopped.
- Off = inert: with no copy (the setting was never on) promote does one
  `EXISTS` and nothing else, and `generateEOD` reads one unset setting —
  identical behaviour (`TestSalesMirrorPromote_NoCopyNoChange`).

## Tests

Promote with M "dead": A's and M's sales adopted under their origins with
lines and payments, stock unchanged, an inherited row voided, B's own row
untouched, the next Z excludes everything before M's close (gross 0) and the
own pre-close sale is disclosed, the change log stays empty, the kill switch
restored, a second adoption adds nothing; a receipt clash and a missing
payment method each refuse one sale whole and keep it in the copy; a cut-
short adoption resumes on the tick and clears its mark; the aggregate till
key of adopted sales. TDD: removing the floor from `generateEOD` fails the
promote test (`Z from ""`).

## Independent review (Fable, strict)

Verdict: safe to merge dark; not safe to enable until 1–3 are fixed or
carded with owner sign-off.

1. MAJOR — cloud sales aggregates would double the previous main till's own
   sales (uploaded under its register, adopted under its LAN id): **fixed**
   (adopted under its register when it has one). Remaining: if the copy
   lagged, the new main's re-upload for those days is smaller than what the
   old main uploaded — card.
2. MAJOR — the floor silently drops sales that were in no Z: **disclosed**
   (counted and shown on promote, in the audit row). Moving the floor below a
   close would count that close's sales twice, so it does not move; the owner
   decides whether these need a manual correction flow — card.
3. MAJOR — adoption on the request context, not resumable: **fixed**
   (`context.WithoutCancel`, pending mark + resume on the maintenance tick,
   an error line on the panel).
4. MINOR — re-logging adopted sales: **fixed** (log quiet during adoption).
5. MINOR — voids not reported: **fixed** (panel line, audit counts); a
   per-void audit row is not written (one `sales_adopted` row with counts).
6. MINOR — the problem was never resolved: **fixed**.
7. MINOR — floor gated on copied sales only: **fixed** (sales or closes);
   errors logged.
8. MINOR — weak tests: **fixed** (gross, FK refusal, resume, log quiet).
9. NIT — "1 copied sales": **fixed** (count-neutral wording).
10. NIT — a refund's link to a refused original is dropped: accepted (the
    refund itself is adopted; the copy keeps the link).

Deviations accepted by the reviewer: no final drain from a dead main; no
log position/epoch handling (every re-join re-syncs today; revisit before a
re-home without re-join, N11); no disclosure before pressing promote (card);
the re-join 35-day push not built (UT-5 sends unsent sales).

## New keys for the language packs

`tills.promote_adopted`, `tills.promote_adopt_failed`,
`tills.promote_adopt_voided`, `tills.promote_adopt_resumes`,
`tills.promote_outside_z`.
