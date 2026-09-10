# Catalog item form: full screen, pinned bar, icon-only delete, tabs (ut-docs#1956)

**Card:** ut-docs#1956 (p1, `complexity:hard`, `lane:local`)
**Branch:** `feat/1956-catalog-item-form`
**Reviewed:** 2026-09-10
**Verdict:** safe to merge, after the review's blockers were fixed and re-verified.

## What shipped

Product-owner feedback, verbatim, working through the catalog on the pilot
tablet — *"the forms are so confusing"*. Five changes to the **item form**:

1. **Variants move into the form.** `catalog_variants.html` (variants,
   barcodes, cost price, lead time, option sets, modifier groups) rendered at
   page level under the item list; it is now the form's own **Variants** tab.
2. **The form is genuinely full screen.** ut-docs#1901 had already made it a
   `<dialog>`, but `.item-form-modal` was capped at
   `width:94vw; max-width:60rem; max-height:92vh; inset-block-start:4vh` — a
   ~960px floating card on the 1280×800 pilot tablet, with the catalog list
   visible around all four sides. Now full-bleed.
3. **Icon-only delete**, in the bar, hidden in create mode, at the opposite
   end from Save/Close so it cannot be hit by accident. **Deactivate
   semantics** (`POST /api/catalog/item/deactivate`) — an item with trading
   history is preserved and can be reactivated; the confirm says so.
4. **The three `<details>` accordions become tabs** — Details · Variants ·
   Item image · Print labels · Keypad, reusing the existing
   `.tab-bar`/`.tab`/`.tab-panel` + Alpine roving-tabindex pattern from
   `tills.html` / `index.html` / `setup.html`, not a new mechanism.
5. **The top action bar is pinned.** The dialog is a flex column: a
   non-scrolling `.catalog-form-head` (title + delete | + New / Close / Save,
   the save/error notice, then the tab strip) over a single scrolling
   `.catalog-form-body`. Save moved into the bar via the HTML5
   `form="item-form"` attribute; the old bottom submit button is gone.

Also fixed, because this card was redesigning the delete affordance anyway:
`catalog_row.html` shipped `hx-confirm="Deactivate {{ .Item.Name }}?"` — a
hardcoded English string on a destructive confirm, invisible to
`guard-i18n.sh` because it is not a `T` call. Both the row and the form now
use one `printf`-parameterised key.

## Independent review

Fresh-context **Opus** subagent (card is `complexity:hard`, so review is
deliberately not Fable — a different model, not the author's own), run in an
isolated worktree so its revert-then-restore verification could never touch
the working checkout (ut-docs#386).

Its verdict was **NOT SAFE TO MERGE**, with three confirmed defects — two of
them in the very mechanisms this card claims to fix.

### The root cause behind F1–F3

**Alpine's `x-show` DOM write lands on the *second* animation frame** — not
synchronously, not on the microtask queue, not on the first rAF. Measured
live, Labels tab active, Save clicked:

```
sync after click : display=none      microtask : display=none
rAF              : display=none      rAF#2     : display=block
```

Three call sites acted on a just-selected tab panel while it was still
`display:none`. A `display:none` element can neither take focus nor be
reported by constraint validation, so all three silently did nothing.

| # | Severity | Defect | Fix |
|---|---|---|---|
| **F1** | **Blocker** | Save with an empty required field while another tab showed: `reportValidity()` ran one frame early, so the browser refused to report and the operator got **nothing** — no bubble, no notice, no focus move, just a console line. Exactly the silent failure the guard existed to prevent. | routed through `afterTabPaint()` |
| **F2** | **High** | `modal.show()` runs the dialog focusing steps synchronously, so if the previous session left a non-Details tab selected, `#item-barcode`'s `autofocus` was skipped and focus fell to the first focusable descendant — **the delete button**. Confirmed end to end: leave the Keypad tab, close, click another row, press Enter (what a barcode scanner sends to end a scan, and this form is deliberately scanner-first) → the deactivate confirm opens for an item the operator never chose. | focus set explicitly in `openModal()` after the panel paints |
| **F3** | **Medium** | The Keypad tab never focused its capture field — the operator opens the tab, presses the physical key, and the keystrokes go nowhere. A **functional regression**: the pre-#1956 `<details>` path did focus correctly. | routed through `afterTabPaint()` |
| **F4** | **Medium** | Moving `catalog_variants.html` inside `.catalog-form` let `.catalog-form label` (0,1,1) beat `.chip-add-primary` (0,1,0) and every bare `<label>` in that panel: every checkbox put its box on the line *above* its text and doubled in height (21px → 44px). | rule scoped with `:not(.catalog-detail label)` |
| **F5** | Low-med | The new spec's horizontal-overflow assertion was **vacuous**: `.catalog-form-body` sets `overflow-x: hidden`, and a hidden-overflow box always reports `scrollWidth === clientWidth`, so it read 0 whatever the content did. | now measures each control's right edge against the body's own box |
| **F6** | Low | `fa` used `مشخصات` for "Details" where the shipped `audit.col.details` says `جزئیات`; the fa delete confirm said "goods list" where `nav.catalog` says `کاتالوگ`. | both aligned to the shipped terms |
| **F8** | Trivial | `catalog.tab.details` inserted out of alphabetical order in all four locale files. | re-sorted |

**F7** (the server-rendered `hx-confirm` placeholder) was left as-is: the
button is `hidden` in create mode and `setMode()` always rewrites the
attribute before it can be shown, so it is unreachable — noted, not changed.

### What the review checked and found clean

Worth recording, because these were the parts most likely to be wrong:

- **`hx-confirm` rewriting and the delete target.** htmx reads the rewritten
  attribute at request time; the id is sent correctly; it **cannot** fire
  against the wrong item after "+ New" or after editing a second item
  (traced: stale confirm text exists only while the button is `display:none`
  and unreachable). `.btn[hidden]` correctly beats `.btn`'s own `display`, so
  create-mode hiding is real, not just an attribute.
- **No nested forms** — asserted against the real rendered DOM, not the
  template: 16 forms on the page, 13 inside `#catalog-variants`, zero nested,
  `#item-form.parentElement.closest('form') === null`.
- **htmx `outerHTML` swap of `#catalog-variants` inside the open dialog**
  preserves the tab structure and the Alpine scope; creating a variant end to
  end from inside the dialog works and the item form still saves afterwards.
- **Escaping**, with a hostile item name (`Ev<il "q" & 'z' $& Probe`): the
  row's `{{ printf (T …) .Item.Name }}` attribute, the JS notice path, and the
  title all escape correctly; the function-replacement guard defeats `$&`; no
  markup injected.
- **RTL** — every new rule is a logical property; verified rendered in `ar`
  and `fa` that the pinned bar mirrors, the tab strip runs right-to-left, and
  `focusTab()` reverses arrow keys off `getComputedStyle().direction`.
- **The 7 pre-existing specs + 1 Go test** that were modified: all
  **adaptations, not weakenings** — one (`catalog-row-oob-1363`) now drives an
  input behind the dialog via `fill()`, which is honest in its comment but is
  a test-only path.

## Verified beyond automated tests

- **On the real 10.1" pilot tablet (TECLAST P50T, 1280×800, real touch).**
  The branch was served from the dev machine into the tablet's own Chrome over
  `adb reverse`, deliberately **not** by flashing a dev APK: `lane:cloud-54`
  is mid-investigation on that device's installed build for ut-docs#1953, and
  replacing it would have destroyed their evidence. Confirmed against the
  baseline captured on v0.14.4 before the change: the dialog fills the
  viewport; the bar and tab strip survive scrolling the Details tab to its
  end; the bin icon appears only in edit mode; the Variants tab holds the
  clicked item; the panel is gone from under the list.
- **TDD re-verified independently, all six new Go tests** — each production
  change reverted individually, test run, restored, re-run. All six failed for
  the claimed reason and passed on restore. The delete test was probed three
  ways (remove the button; remove only `hidden`; swap the endpoint to a hard
  delete) because its substantive claims are the easiest to assert shallowly —
  it catches all three. **No false-pass tests.**
- **The four new regression tests were verified the same way**: reverted the
  fixes for F1–F4, confirmed each fails for its own distinct reason
  (`#item-name` not focused; `#item-form-delete` focused; `#keypad-capture`
  not focused; `flex-direction: column` in the variants panel), restored,
  confirmed all pass.

## Gates

`go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...` (53 packages),
`make docs-shots`, and every CI-blocking guard in `ci.yml`'s `build` job.
**Playwright: 378/378.**

Three guards fail only in this local environment and pass in CI:
`guard-deadcode-baseline.sh` (CI runs it with `-tags desktop` and real
GTK/WebKit headers; every hit is in `cmd/unitill-desktop`, untouched here),
`guard-price-history-sync.sh` (every hit is inside `.claude/worktrees/`,
which a CI checkout does not have), and `guard-shellcheck-version.sh` (local
shellcheck 0.11.0 vs the runner's pinned 0.9.0).

## Interaction with the other lanes

Three lanes are building in the catalog at once. This branch was rebased onto
`main` at `b1dfb430` and re-gated afterwards, not merged from a stale base.

- **ut-docs#1967** (variants grid Save button off-screen) landed on `main`
  while this was in flight. Its spec navigated to the variants panel the old
  way — closing the dialog and scrolling the page — so this branch adapts that
  navigation to the Variants tab. **No assertion was weakened**; the helper
  now scrolls **vertically only** (Playwright's `scrollIntoViewIfNeeded()`
  would also scroll the grid's *horizontal* scroller and mask the very defect
  that spec exists to catch), and `geometry()` gained a `gridScrollLeft === 0`
  assertion so "reachable" cannot be satisfied by a sideways drag. That makes
  the spec strictly stronger than it was.
- **ut-docs#1962** (help translation-drift guard) also landed mid-flight. The
  German help topic conflicted: `main` had brought it to full parity with
  English but describing the pre-#1956 UI. Resolved by taking `main`'s
  complete bullet set and applying this card's wording to it — not by taking
  either side. The new `guard-help-drift.sh` then caught a real mistake: the
  Turkish step 2 led with `**Varyantlar**`, giving it one bold lead-in more
  than English. Reworded.
- **universal-till#1012** (ut-docs#1951, card grid) and **#1017**
  (ut-docs#1957, modifier relocation) are open and touch the same files.
  Whichever merges second resolves.

## Deferred, with cards

| card | what |
|---|---|
| ut-docs#1967 | Save Variant off-screen — **fixed on `main` by another lane**; this branch improves the available width and adapts its spec. |
| ut-docs#1997 | `<label class="field-checks">` renders as a stretched, stacked checkbox — pre-existing, but full screen makes it grotesque (a measured 1220px-wide checkbox). |
| ut-docs#1998 | The OSK reservation constant is ~1.45rem short (`15.5rem` vs a measured 288px), currently masked by the body's bottom padding. Shared with `.payment-overlay`. |
| ut-docs#1999 | The full-screen dialog has no Escape and hides the nav rail entirely — deliberate (`.show()`, not `.showModal()`, for OSK reachability), but worth an explicit decision against ut-docs#1346. |
| ut-docs#2000 | At 360px the pinned head is 312px of 740 (42%) in `en`/`tr`. Non-overlapping and passing, but heavy. |

## Language packs

The four new `en.json` keys are **brand new**, so pack-first is impossible —
a pack's own `check-key-drift.sh` fails a key core `main` lacks as an orphan
(ut-docs#1934). Core merges first; `lang-pack-drift` is red on `main` until
`ut-plugin-language-de` (v1.1.40) and `ut-plugin-language-es` (v1.1.31) land,
and closing that window in the same cycle is this lane's job. Both pack
branches are prepared and validating.
