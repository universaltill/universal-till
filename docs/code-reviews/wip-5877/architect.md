# Architect — ut-docs#5877: Print slip from a /shifts History row

**Placement:** core `universal-till` (`internal/pages`, `web/`). Not country/vendor code. **ADR: no.**
The change follows existing patterns: the #5841 endpoint, the voucher-slip "Reprinted" note (#5740) and the elevation prompt's hidden fields.
**Out of scope:** paging past 20 rows, translated help, any change to the gate, the 404/502 order or the audit action name.

## Key decision: the client never supplies a target id
The row button sends only `reprint=1`. The server works out the result-region id from the **DB** shift id
(`slip.X.Shift.ID`). It never uses the path string or any form field for that. Pure helper, new in
`internal/pages/shift_close_slip_print.go`:

```go
var shiftSlipSafeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
// shiftSlipRowSuffix: the id itself when safe (uuid.NewString ids always are),
// else "h"+hex(sha256(id))[:16]. Same function for the template and the handler.
func shiftSlipRowSuffix(id string) string
```
Ids: `shift-slip-print-msg-<sfx>` (result), `shift-slip-print-busy-<sfx>` (indicator).
The close-result singletons `#shift-slip-print*` stay unchanged.

## Files
1. **internal/pages/shift_close_slip_print.go**
   - `reprint := r.PostForm.Get("reprint") == "1"` (PostForm only; any other value means false).
   - Elevation: target = `"#shift-slip-print-msg-" + shiftSlipRowSuffix(slip.X.Shift.ID)` if reprint, else `"#shift-slip-print-msg"` as now.
     Hidden fields: `[]elevationHiddenField{{Name: "reprint", Value: "1"}}` if reprint, else nil. So the PIN retry keeps the target and the marker.
   - `buildShiftSlipDoc(slip, store, charset, reprint bool)`: when reprint, append one Meta line straight after the
     `reports.zpaper.generated` line: `fmt.Sprintf(T("shifts.close_slip.paper.reprinted"), stamp(x.GeneratedAt))`.
     This is the same Latin date/time as "Generated", following the voucher_slip `Note` pattern. When reprint is false the doc is byte-identical to today.
     Update the existing callers and tests to pass `false`.
   - Audit: **yes**. When reprint, payload gets `"reprint":"true"` (same action `shift_slip_printed`). The payload is unchanged when false.
   - Success/502/404 bodies: unchanged.
2. **internal/pages/shifts_page.go**: wrap the History rows in a view struct `{data.ShiftSummary; SlipSfx string}`
   (embedded, so the template's `.OpenedAt` etc. keep working). `SlipSfx = shiftSlipRowSuffix(s.ID)`, filled only for closed rows.
   Alternative: register the helper as a page-local func. Prefer the struct, because there is no global FuncMap change.
3. **web/ui/pages/shifts.html**: History table
   - Add a 7th `<th><span class="visually-hidden">{{ T "shifts.history.actions" }}</span></th>` and change the empty-row `colspan` to `"7"`.
   - Per closed row (open rows get an empty `<td></td>`):
     ```html
     <td class="shift-slip-row">
       <button type="button" class="btn secondary shift-slip-row-print" data-shift-id="{{ .ID }}"
         hx-post="/api/shifts/{{ urlquery .ID }}/close-slip/print" hx-vals='{"reprint":"1"}'
         hx-target="#shift-slip-print-msg-{{ .SlipSfx }}" hx-swap="innerHTML"
         hx-indicator="#shift-slip-print-busy-{{ .SlipSfx }}" hx-disabled-elt="this">{{ T "shifts.close_slip.print_row" }}</button>
       <span id="shift-slip-print-busy-{{ .SlipSfx }}" class="htmx-indicator muted" role="status">{{ T "shifts.close_slip.printing" }}</span>
       <span id="shift-slip-print-msg-{{ .SlipSfx }}" class="x-report-print-msg" data-xr-target aria-live="polite"></span>
     </td>
     ```
     For the path, use `pathEscape` if a helper exists, else build the escaped URL in the view struct (`SlipURL`). Do not hand-roll escaping.
   - JS: keep `XR_TARGETS` and add `|| target.hasAttribute('data-xr-target')` to both the `htmx:responseError` and the `htmx:sendError`
     checks (a small `isXRTarget(t)` helper). Do not grow the list or match on a prefix.
4. **web/public/app.css**: `.shift-slip-row { text-align: end; white-space: nowrap; }` and `.shift-slip-row .btn { min-block-size: 44px; }`.
   Let the msg wrap (`white-space: normal`, `max-inline-size` around 14rem). Logical properties only.
5. **web/locales/en.json** + **web/i18n-meta/en.meta.json** (review `standard`):
   - `shifts.close_slip.print_row` "Print slip": the button on each closed History row on /shifts that prints that shift's close slip again. max_length 20.
   - `shifts.history.actions` "Actions": screen-reader-only header of the History action column.
   - `shifts.close_slip.paper.reprinted` "Reprinted %s": printed line on a shift close slip printed from History. placeholders ["date and time of the reprint"], max_length 42.
6. **web/help/en/reports.md** "Printing a shift slip": replace "only on that confirmation message… gone" with a description of
   **Print slip** on each closed row of **History** (the last 20 shifts), and say a slip printed from History carries a
   "Reprinted <date and time>" line. Same permission/PIN and Print-failed wording. No routes change (/shifts is already claimed). There is no docs-shot of /shifts (e2e/tests-docs has none).
7. Review record `docs/code-reviews/<date>-shift-slip-history-reprint.md`.

## Tests
- **shift_close_slip_doc_test.go**: `TestBuildShiftSlipDoc_ReprintLineOnlyWhenReprint` checks that false gives the identical doc and no "Reprinted", and true gives exactly one line after Generated.
  Existing golden tests stay green.
- **shift_close_slip_print_test.go**:
  - `_ReprintPrintsMarkerAndAuditsReprint`: the printed bytes contain "Reprinted" and the payload has reprint=true.
  - `_NoReprintFieldNoMarker`: the close button path has no marker and no payload key.
  - `_ReprintElevationTargetsRowAndCarriesHidden`: the prompt has `hx-target="#shift-slip-print-msg-<id>"` and a hidden `reprint` = `1`.
  - `_ReprintManagerPINKeepsMarker`: InsertAuditElevated and the marker.
  - `_RowTargetFromDBNotInput`: a shift id with unsafe chars (e.g. `a"b<c`) is inserted. Expect the hashed suffix, and the raw id never appears in the target.
  - `_ReprintValueOtherThan1Ignored`.
- **shiftSlipRowSuffix** unit test: a uuid passes through; unsafe, over-long or empty input gives `h`+16 hex characters; the result is deterministic.
- **shifts_page_test.go**: a closed row has the button with the hx-post for its own id and unique msg/busy ids; an open row has no button; there are 7 columns and colspan is 7.
- **e2e**: a spec exists (`e2e/tests/shifts-close-slip-5841.spec.ts`). Add `e2e/tests/shifts-slip-history-5877.spec.ts`:
  1. Close a shift, then reload.
  2. The History row button posts and the 502 "Print failed" lands in that row's msg only.
  3. Simulate a dropped connection with `route.abort`; expect `designer.error.server` in that row.
  4. Check the button is at least 44px and there is no horizontal scroll at 1024x600.
  5. Check there is no button on the open row.
  The 5841 spec stays unchanged.
- Guards: i18n, i18n-meta, rtl-logical-css, no-showmodal, help-topics.
