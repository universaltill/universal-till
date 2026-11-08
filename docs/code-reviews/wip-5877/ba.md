# BA — ut-docs#5877: Reprint the shift-close slip from a History row on /shifts

## Current state (verified)
- Endpoint already exists and takes any closed shift id:
  `internal/pages/shift_close_slip_print.go:26` `POST /api/shifts/{id}/close-slip/print`.
  404-before-PIN (`:46-58`), `checkOrElevate(d, r, "reports", pin)` (`:60`),
  502 on printer disabled/failed with no audit (`:80-91`), audit
  `shift`/`shift_slip_printed` only after print (`:96-110`). Paper built by
  `buildShiftSlipDoc` (`:126`).
- The only button is in the close result fragment,
  `internal/pages/shifts_api.go:747-756` (ids `shift-slip-print`,
  `shift-slip-print-busy`, `shift-slip-print-msg`, all singletons).
- The elevation prompt target is **hard-coded** to `#shift-slip-print-msg`
  (`shift_close_slip_print.go:71`) — a per-row button needs its own target.
- /shifts "Recent shifts" is the **History** card (`shifts.history` = "History"),
  `web/ui/pages/shifts.html:61-80`, rows from `repo.ListRecentShifts(ctx, 20)`
  (`internal/pages/shifts_page.go:31`; SELECT incl. `id` at
  `internal/data/pos_repo.go:4586`). Rows have `.Open`; closed rows show
  ClosedAt/Counted/Expected/Variance. 6 columns, no action column.
- Error swapping is keyed on exact ids: `XR_TARGETS` in
  `web/ui/pages/shifts.html:192` (responseError/sendError handlers) — per-row
  ids won't match without a change.
- Help: `web/help/en/reports.md:389-406` "Printing a shift slip" says "The
  button is only on that confirmation message, so reload the page and it is gone."
- Demo mode already lists the route (`internal/pages/demo_mode.go:420`).

## Decision: mark a reprint on the paper — YES, from the History row only
Precedent research:
- Voucher slip printed again from the voucher page carries a centred note
  "Reprinted <date>" (`internal/print/voucher_slip.go:17-20`,
  `internal/pages/voucher_slip_reprint.go:33`, key `receipt.vouchers.reprinted`
  "Reprinted %s", ut-docs#5740) — the closest analogue (a non-fiscal slip
  printed again later from a list/detail page).
- Journal receipt reprint (`journal.reprint`) and EOD archive reprint
  (`eod_api.go:1122-1205`, audit `eod_reprinted`) do not add a paper marker.
Recommendation (no escalation; not a legal call — the slip is not a fiscal
document, "never a Z"): a slip printed from a History row prints one extra
line "Reprinted <date time>" (shop language, same as the rest of the slip),
like the voucher slip. The close-result button's paper stays byte-identical
(it's the original, printed at close). Rationale: a later slip carries a
fresh "Generated" stamp anyway, but an explicit marker stops two slips for
one shift being read as two counts/two handovers in the cash bag. Not a
merchant setting (one safe default, low stakes). Audit: same
`shift_slip_printed` action; payload may add `"reprint":"true"` (Architect's
call) so the audit log distinguishes the two.

## Requirements
1. Each **closed** row in the History card on /shifts gets a "Print slip"
   action; open rows get none (an open shift 404s anyway).
2. It calls the existing endpoint unchanged in gate/404/502/audit behaviour;
   the only server-side addition is the reprint marker signal (e.g. a
   `reprint=1` form field also carried through the PIN prompt's hidden fields)
   and a per-row result target for the elevation prompt.
3. Result (ok / Print failed / not closed / PIN prompt / network error)
   shows inline next to that row in an `aria-live="polite"` region, never a
   dialog (no `showModal`).
4. Touch target ≥44px at 1024x600; table must not scroll the page
   horizontally at that size; RTL-safe (logical CSS only, `text-align:end`).
5. All new strings via `{{ T }}` with `en.json` + `en.meta.json` entries
   (e.g. `shifts.close_slip.print_row` "Print slip", a header/sr-only label
   for the action column, `shifts.close_slip.reprinted` "Reprinted %s").
6. Help `web/help/en/reports.md` "Printing a shift slip" updated (English
   only); refresh the docs-shot if /shifts History is screenshotted.

## Acceptance criteria
1. /shifts with ≥1 closed and 1 open shift: every closed History row has a
   "Print slip" button; the open row has none.
2. Tapping it (Reports permission, printer OK) prints the SHIFT CLOSE slip
   for **that row's** shift id (not the latest), shows "✓ Shift slip sent to
   the printer" next to that row only, and writes exactly one
   `audit_log(entity=shift, entity_id=<row id>, action=shift_slip_printed)`.
3. The paper from a History row contains a "Reprinted <date time>" line; the
   paper from the close-result "Print shift slip" button does not (existing
   test of that paper stays green unchanged).
4. Without Reports permission, the PIN prompt renders inside that row's
   result region; a correct manager PIN prints and audits as elevated
   (`InsertAuditElevated`), with the reprint marker still present.
5. Printer disabled/unreachable: "Print failed…" appears in that row's region
   (502), no audit row written.
6. A network drop mid-tap shows `designer.error.server` in that row's region
   (sendError handler matches per-row targets).
7. Unknown/open shift id → 404 `shift_not_closed` before any PIN prompt
   (unchanged; existing tests stay green).
8. Two rows' results don't overwrite each other; ids are unique per row.
9. At 1024x600 the button's hit area is ≥44×44px and the page has no
   horizontal scroll; in an RTL locale (ar/fa) the action column sits at the
   inline end with no left/right CSS (`guard-rtl-logical-css.sh` passes).
10. `guard-i18n.sh`, `guard-i18n-meta.sh`, `guard-help-topics.sh`,
    `guard-no-showmodal.sh` pass; new keys only in en.json/en.meta.json.
11. `web/help/en/reports.md` no longer says the slip is only offered right
    after closing; it describes Print slip on History rows and the
    "Reprinted" line.
12. The close-result button keeps working exactly as before (#5841 tests green).

## Non-goals
- Translated help topics (Weblate, ADR-0160).
- Reprinting for shifts older than the 20 History rows (no paging/search) —
  follow-up card only if a real shop asks.
- Marking reprints from the close-result button (repeat taps there).
- Any change to the gate, 404/502 ordering or audit action name.

## Dependencies
None open — #5841 (universal-till#2451) is merged.
