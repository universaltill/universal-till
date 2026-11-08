# UX gate — ut-docs#5877 (History-row "Print slip" on /shifts)

Checked from code only (read-only; no screenshot existed at review time; no printer, no touch hardware).

Surfaces: till back-office page /shifts on the kiosk touchscreen (1024x600, body.kiosk and windowed), desktop, phone (<=480px card mode via table-cards.js). No customer-facing surface.

## Verdict: FAIL — 2 must-fix (F1 phone, F2 copy), the rest pass or are minor

### F1 (must-fix) Phone width: the button hides behind "More" and gets an "Actions:" label
- `web/ui/pages/shifts.html:64` — the 7th `<th>` holds `<span class="visually-hidden">Actions</span>`. `table-cards.js` `headerText()` reads `textContent`, so that column counts as a labelled column, not an action column (`.t-cards-actions` is only for an empty header).
- Result at <=480px: each closed card has 7 labelled cells, and `MAX_SHOWN = 4`, so **Print slip sits behind the More toggle**, with an "Actions:" label in front of it. The row result message is hidden with it.
- Fix (choose one):
  - (a) Preferred. Change the header to `<th aria-label="{{ T "shifts.history.actions" }}"></th>`. Its textContent is empty, so the cell becomes `.t-cards-actions`: no label, at the end of the card, never collapsed. Screen readers still get the column name. Then update `shifts_page_test.go`'s th count if it relies on the span.
  - (b) Add `data-card-keep` to `<td class="shift-slip-row">` (line 76). It stops the collapse but keeps the "Actions:" label.
- Add an e2e assertion at 360px that the button is visible without tapping More.

### F2 (must-fix) Copy: error text names a button this row does not have
- `web/locales/en.json:3039` `shifts.close_slip.print_failed` says "…then tap **Print shift slip** again". `en.json:3042` `shifts.close_slip.failed` says "Tap **Print shift slip** to try again". The History button is labelled **Print slip** (`shifts.close_slip.print_row`).
- Fix: use wording that fits both buttons. For example, print_failed: "Print failed. Check the receipt printer in Settings → Receipt printer, then print the slip again." and failed: "The shift slip could not be printed. Try printing it again." This only changes the English text; the placeholders stay the same, so the same keys are fine. Update the matching `en.meta.json` descriptions, which mention "tap Print shift slip". Language packs will drift, which is the normal follow-up. Alternative: rename the row button to "Print shift slip" (longer; German is about 30% wider, see the touch-target item).

## Gate items
- [PASS] Touch target: `app.css:964` `.shift-slip-row .btn { min-block-size:44px; min-inline-size:44px }`. The base `.btn` already sets `min-height:3rem` (48px) and touch-action:manipulation. Kiosk is not smaller than desktop.
- [PASS] Touch scroll/drag: no pointer handlers added; it is a plain htmx button.
- [N/A]  Keyboards/OSK: the row has no text field. The PIN goes through the existing global non-modal `#elevation-modal` (`.show()`, type=password, OSK-aware).
- [PASS] Nothing steals focus on load.
- [PASS] Strings via T: 3 new en keys with meta (max_length 20/20/42). Inline JS adds no strings. [FAIL] copy consistency, see F2.
- [PASS] RTL: the new CSS (`app.css:963-965`) is logical only (`text-align:end`, `min-*-size`, `max-inline-size`, `margin-inline-start:auto`).
- [PASS] aria-live per row: the `#shift-slip-print-msg-<sfx>` span has `aria-live="polite"`, and its id is unique per row and comes from the server (test covers cl1/cl2). The success fragment is `role=status`. The error path uses `isXRTarget` through `data-xr-target`, so failures are no longer silent. The busy span has `role=status` and is display:none outside a request (fine).
- [PASS w/ note] Keyboard path: it is a native `<button>`, reachable with Tab. Note: `hx-disabled-elt="this"` disables the focused button during the request. Chromium then drops focus to body, so a keyboard user has to tab back down to the row. Same pattern as elsewhere; minor, no change needed for this card.
- [PASS] Focus ring: the UA default is on `.btn`. There is no project-wide `.btn:focus-visible` rule (pre-existing, out of scope).
- [PASS] Non-colour meaning: success has a "✓" plus text, error has "⚠" plus text. The variance `.stock-low` colour is unchanged and pre-existing.
- [PASS] No modal blockers: the result is inline in the row. The PIN prompt is the non-modal `.show()` dialog. There is no showModal.
- [PASS] Empty state: colspan 6→7 on "No shifts yet". Open rows get an empty `<td>`, which table-cards marks `.t-cards-blank` (hidden on phone only after F1(a)).
- [NOT CHECKED] 1024x600 visual: there was no screenshot (`shifts-1024.png` did not exist yet). A 7-column table with 2 datetimes and 4 money columns plus a button, inside the nav rail at 1024px, is tight. The History table has no `display:block; overflow-x:auto` escape hatch, unlike `.pairing-table` and `.x-report-tenders`. The Tester must confirm `document.documentElement.scrollWidth <= innerWidth` at 1024x600 in **de** (longest), and also with a long print_failed message rendered in a row (the cell caps at 14rem and wraps, so the row grows taller, not wider). If it overflows, add `.shifts-history { display:block; overflow-x:auto }` (logical, same pattern as `app.css:944`).
- [NOT CHECKED] Real touch hardware and a real printer: emulated/unit only. Put this on the board.
- [PASS] Paper wording: "Reprinted %s" (max 42) is the line after "Generated", which follows the voucher-slip precedent (#5740). Note: both lines show the same stamp (`x.GeneratedAt` is the print time). That is redundant but not wrong. "Closed at" still carries the original close time, so the slip can't be read as a second count. Acceptable.
- [PASS] Help (`web/help/en/reports.md:401-413`): accurate. Last 20 shifts (`ListRecentShifts(…,20)`), closed rows only, the Reprinted line with date and time, result next to the row, audit marks reprint (`"reprint":"true"`), and "next to the button" on failure. Nav label "Shifts" matches `nav.shifts`. After F2, the help's failure sentence still fits ("you see 'Print failed'").
