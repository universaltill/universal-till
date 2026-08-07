# Code review — draggable 🐞 bug-report panel (ut-docs#395)

**Branch:** `fix/395-bugreport-panel-draggable`
**Date:** 2026-08-07
**Reviewer:** independent read (reviewer did not write the implementation)
**Verdict:** **safe to merge** — after four defects found in review were fixed on
the branch and locked in with tests.

---

## What shipped

ut-docs#395 has four acceptance criteria. Only the fourth is new work in this
branch; the review confirmed the state of the other three rather than taking
them on trust.

| # | Criterion | State |
|---|-----------|-------|
| 1 | "No picker" auto-capture | Partly addressed by universal-till#210 (Wayland screencast portal installed so the picker has something to select). The literal "no picker at all" criterion is still open and deliberately **out of scope here**. |
| 2 | Thumbnail after each capture, removable | **Already shipped** by ut-docs#347 and untouched. Verified in `web/ui/partials/bugreport_panel.html`: `#ir-screenshot-thumbs` markup, `addScreenshotThumb()`, a per-thumb `✕` that splices the entry out and calls `URL.revokeObjectURL`. |
| 3 | Non-modal | **Already true** and untouched. Verified by grep: no backdrop element, no focus trap, no `role="dialog"`/`aria-modal`, no document-level `keydown`. The only document listeners are the 🐞 click delegation and, transiently, the drag's own pointer listeners. |
| 4 | **Draggable** | **This branch.** Pointer Events drag from `.bugreport-head`, clamped into the viewport, switching the panel from its logical resting position to explicit `left`/`top`. |

Also in the branch: `.bugreport-head { cursor: move; touch-action: none;
user-select: none }` with `.bugreport-close` opting back out; two mirrored
Playwright tests per suite; one sentence added to `web/help/{en,ar,fa,tr}/bug-reporting.md`;
`web/help/img/manifest.json` regenerated.

---

## Findings

### Fixed on this branch (4 real defects)

**1. Dragging downward started a native HTML5 link drag and killed the drag.**
This was the most serious one, and the shipped tests could not see it. The
sale screen underneath is full of `<a class="btn">` anchors, which are natively
draggable. A pointer drag over them makes Chromium begin a link drag-and-drop
and fire `pointercancel` on the captured pointer. Reproduced directly: dragging
the panel `(-100, +300)` fired `dragstart` on an `<a class="btn secondary">`
after the first frame and the panel travelled 37px of the requested 300 instead
of the full distance.

The two shipped tests both drag **horizontally only** — a choice the diff
documents as deliberate (to stay clear of the scan row). That reasoning is
sound on its own terms, but it also meant the one motion that breaks was the
one motion never tested. Fixed with `e.preventDefault()` on `pointerdown`
(cancelling the compatibility `mousedown`, which is what arms the browser's own
drag gesture). Confirmed: `(-100, +300)` now lands at the requested position,
no `dragstart`, no `pointercancel`.

**2. `pointercancel` was not handled at all — a stuck drag on the touchscreen
this feature exists for.** Driven with a real CDP `touchStart`/`touchMove`/
`touchCancel` sequence, the browser fires `pointercancel` then
`lostpointercapture`; the shipped code listened for neither. The result was a
drag that never ended: the document `pointermove` listener stayed attached for
the life of the page, `dragging` stayed `true`, and the panel then followed the
pointer around with nothing pressed — measured jumping from `(441, 192)` to
`(0, 468)` on a single stray pointer move, i.e. wandering straight back over the
screen the operator had just dragged it off. `pointercancel` on touch is not
exotic: palm rejection, an extra touch point and system swipes all produce it.
Fixed by ending the drag on `pointercancel` and on `lostpointercapture`.

**3. A second pointer's release ended the live drag *and* threw an uncaught
`NotFoundError`.** `onDragEnd` was a document-level `pointerup` handler with no
pointer-id check, so any pointer releasing anywhere ended the drag, and
`head.releasePointerCapture(e.pointerId)` was then called with an id that was
never captured. Reproduced with a real touch drag plus a foreign `pointerup`:
the drag froze mid-gesture while the finger was still down, and the page threw
`Failed to execute 'releasePointerCapture' on 'Element': No active pointer with
the given id is found.` Two fingers on the glass — holding the panel while
tapping the till — is exactly the workflow this panel exists to enable. Fixed
by tracking the drag's `pointerId` and ignoring events from any other pointer,
guarding the release with `hasPointerCapture`, and refusing a second concurrent
drag. `dragPointerId` is now also assigned only *after* `setPointerCapture`
succeeds, so a throwing capture can no longer leave a half-started drag behind.

**4. A dragged panel could be stranded entirely off-screen by a viewport
change.** The clamp ran only during a drag. Dragging the panel to `y=382` and
then shrinking the viewport to 1024×400 left it wholly below the fold —
and because the position survives close/reopen on the same page, there was no
head bar left to grab it back by (recovery required a navigation). Real on the
desktop shell (window resize) and on a tablet till (rotation). Fixed by
re-clamping on `resize`, and again on re-open (a resize while the panel is
hidden measures 0×0, so the clamp has to be redone once it has a box again).
The listener is attached on the first drag only, matching this file's existing
"no page pays for machinery until it's used" lazy-init convention.

**Also fixed (one line, no ticket):** a **right-button** press on the head bar
started a drag. Reproduced: right-press-and-move moved the panel 300px. Now
gated on `e.button !== 0`, which is correct for touch and pen too (both report
button 0 on `pointerdown` — verified in a real touch sequence).

### New tests

Three tests added to `e2e/tests/bugreport-panel.spec.ts` (two mirrored into
`tests/e2e/tests/bugreport_panel.spec.ts`), including the first coverage of the
**touch** path — driven through CDP `Input.dispatchTouchEvent`, because
`page.mouse.*` only ever simulates a mouse and so never exercised the input
method this feature is actually for:

- `touch: the panel drags, and a cancelled gesture does not leave it stuck to
  the pointer`
- `touch: another pointer releasing mid-drag neither ends the drag nor throws`
- `a dragged panel is pulled back on-screen when the viewport shrinks` — which
  doubles as the suite's only **downward** drag, and so is the regression lock
  for the native-link-drag defect.

### Accepted as-is

- **Not persisting the dragged position.** Verified: dragging then closing then
  reopening keeps the position within the page; any navigation resets it to the
  default corner. Defensible and probably what an operator expects — the
  criterion is "can be dragged", not "remembers where".
- **Physical `left`/`top` instead of logical inset properties once dragged.**
  A deliberate, well-commented exception to the repo's RTL rule, and the right
  one: dragging is a screen-space operation. Verified by screenshot in `fa` and
  `ar` that the resting position still mirrors correctly (logical properties
  untouched until a drag happens) and that a dragged panel renders correctly
  with the internal layout still mirrored, ✕ on the leading edge, no breakage.
  Drag direction feels physical in RTL, as it should.
- **Clamp arithmetic on a narrow viewport.** `window.innerWidth - rect.width`
  goes negative below ~26rem, and `Math.max(min, Math.min(max, v))` then pins to
  0 rather than inverting. Checked at 375×700 and 320×260: the panel sits flush
  with the start/top edge and stays usable.
- **The "dragging over the ✕ does not close it" test cannot fail.** Confirmed
  by running it against the un-fixed tree: it passes with or without the drag
  code, because `click` always targets the common ancestor of the press and
  release. Harmless as a regression guard, and the *inverse* — a press that
  starts on the ✕ still closing — is genuinely covered by the suite's first
  test (and by a real touch tap, verified).
- **A panel clamped to the very bottom edge sits partly behind the status bar.**
  The clamp is against the viewport, not the status bar, so the last ~28px can
  be covered. Cosmetic; the head bar, note field and Save all stay reachable.

### Out of scope — would be new Backlog cards

- **`make docs-shots` is not reproducible for two topics.** Every run rewrites
  `alerts.png` and `designer.png` in all four locales even when nothing about
  those screens changed. Root cause found (it is not harness flakiness): both
  screenshots bake a **wall-clock time** into the image — `alerts` renders the
  RECENT PROBLEMS log row timestamp (`10:52` → `14:07`) and `designer` renders
  the receipt-preview date-time line. Any docs-shots run therefore produces
  eight spurious file diffs that every author then has to notice and revert by
  hand (this branch and #394 both did). Worth freezing the clock for the docs
  harness.
- **No touch-hardware test harness.** The new CDP-driven tests reach the
  touch *event* path, which is the part that was broken, but they are still
  synthetic input in a desktop Chromium. Real touchscreen verification would
  need a different harness.

### Pre-existing failures (not caused by this branch — confirmed by re-running
against a stashed tree)

- `e2e/tests/catalog-image-to-till.spec.ts` fails identically with all of this
  branch's changes stashed (uploaded catalog thumbnail never reports
  `complete`). Unrelated to the panel.
- `TestSaveCleansUpDirectoryOnWriteFailure` fails because this sandbox runs as
  UID 0, so the test's read-only `0o500` directory is still writable. Not a
  code problem.
- `e2e` `auth` project cannot launch a browser in this sandbox (no
  `executablePath` override for that project); unrelated to the diff.

---

## What was verified personally (not taken on trust)

- Read the whole of `web/ui/partials/bugreport_panel.html`, not just the hunk —
  the drag code sits correctly alongside ut-docs#394's `dismissed()`
  sessionStorage logic, `syncToggle`, the delegated 🐞 toggle and `initCapture`'s
  lazy-init guard, and does not disturb any of them.
- **TDD re-verification of the shipped work:** stashed
  `bugreport_panel.html` + `app.css`, re-ran the drag tests, watched
  `the panel can be dragged…` fail with a real assertion error
  (`Expected < 650.75, Received 800.75`), restored, watched it pass.
- **TDD re-verification of every fix made in review**, by mutating each one back
  out one at a time and confirming the matching new test fails:
  - remove `pointercancel`/`lostpointercapture` → cancelled-gesture test fails
  - remove the pointer-id guards → foreign-pointer test fails with the uncaught
    `NotFoundError`
  - remove `e.preventDefault()` → downward drag reaches 119px instead of 382px
  - remove the resize re-clamp → panel ends 585px down a 400px viewport
- Full `e2e/tests/bugreport-panel.spec.ts`: **15/15 pass** (12 pre-existing +
  3 new), including ut-docs#394's dismissal test.
- Full `tests/e2e/tests/bugreport_panel.spec.ts`: **9/9 pass**; whole second
  suite 21 passed / 4 skipped.
- Whole primary e2e suite run; the only failures are the two pre-existing ones
  listed above.
- **Looked at the result, not just the assertions**: screenshots taken and read
  for LTR dragged down-left, `fa` dragged down-right, `ar` dragged, the
  bottom-edge clamp, and a 375px-wide clamp. No visual glitches, ✕ reachable in
  every case, no layout breakage in either direction.
- Behaviour probes run by hand: pointer released outside the viewport (drag ends
  cleanly, listener add/remove balance is exactly 0 after eight drags — no leak
  on a kiosk that stays up all shift); drag → close → reopen; drag then
  navigate; repeated drags after a cancel.
- `go build ./...`, `go vet ./...` — clean (and confirmed the diff contains no
  Go changes at all).
- `guard-data-access.sh`, `guard-i18n.sh` (no new keys — this is behaviour/CSS/JS
  plus manual copy), `guard-help-topics.sh`, `guard-htmx-loaded.sh`,
  `guard-emoji-font.sh` — all pass.
- `guard-docs-shots.sh`: **initially failed** on the branch as reviewed, because
  the surface hash covers `web/ui/**` and the review's fixes changed
  `bugreport_panel.html` again. Re-ran the full capture and `write-manifest.js`;
  manifest regenerated to `b31fb282d8db…` and the guard passes. No
  `bug-reporting.png` changed (the panel is visually identical at rest — only
  behaviour changed), which independently confirms the diff author's reasoning.
  The eight clock-drift `alerts`/`designer` PNGs were reverted, not bundled.
- **Read the four manual sentences** rather than confirming they exist. All four
  are genuine, idiomatic translations of the English, not machine-copied:
  ar `إذا كانت تغطي شيئًا تحتاج إلى رؤيته، يمكنك سحبها من شريط عنوانها إلى مكان آخر.`
  (correct feminine agreement with لوحة, correct accusative tanwīn);
  fa `اگر روی چیزی که باید ببینید قرار گرفت، می‌توانید آن را از نوار عنوانش بکشید و جابه‌جا کنید.`
  (natural Persian, ZWNJ used correctly);
  tr `Görmeniz gereken bir şeyi kapatıyorsa, başlık çubuğundan tutup başka bir yere sürükleyebilirsiniz.`
  (standard Turkish UI term *başlık çubuğu*, correct ablative and vowel harmony,
  same formal register as the surrounding steps).
- No real client or shop names and no secrets in the diff.
- Manager gating and the offline-first/non-modal guarantees are untouched: the
  change is additive JS and CSS only, with no Go, no routes and no data access.

## Files

- `web/ui/partials/bugreport_panel.html`
- `web/public/app.css`
- `e2e/tests/bugreport-panel.spec.ts`
- `tests/e2e/tests/bugreport_panel.spec.ts`
- `web/help/{en,ar,fa,tr}/bug-reporting.md`
- `web/help/img/manifest.json`
