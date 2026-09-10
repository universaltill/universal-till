# 2026-09-10 — App-wide list/edit standard, proven on /categories (ut-docs#2010)

## What shipped

The product owner, on the pilot tablet: every list-of-records admin screen
should behave the same — the list is the page, a **full-screen dialog** with a
close button handles both create and edit, a **row tap** opens edit prefilled, a
**New button sits beside a search box**, and add/edit/delete are **icon-only**
("we dont need caption for them"). Ten screens shared the old
list-plus-side-form pattern; this card writes the standard down and proves it on
one. The other nine are ut-docs#2012.

`/categories` before this change was the `.users-layout` 2fr/1fr grid: a table
card with a separate "Create category" card beside it. There was **no edit
dialog to replace — there was none at all**: editing was an inline rename
`<input>` inside every row, next to captioned Rename / Deactivate buttons and
literal `▲`/`▼` glyphs. A row tap did nothing, and there was no search.

Four things were extracted, because the dialog *body* is inherently per-screen
and deliberately is not one of them:

1. **`web/ui/partials/list_header.html`** — search + icon-only New,
   parameterised through a new `dict` template func (the FuncMap had none), so
   the other nine screens adopt the header with a template-only change.
2. **`web/ui/partials/record_dialog.html`** — the full-screen dialog chrome,
   generalised from `.catalog-form*` (ut-docs#1901/#1956). A page fills two
   named slots, `record_dialog_fields` and `record_dialog_destructive`.
3. **`web/public/record-dialog.js`** — open/create, tap-to-edit prefill, Close,
   Escape, the discard guard, the focus trap, and the client-side list filter.
   One file, not ten copies; a no-op on any page without a dialog.
4. **Six icons** in `internal/httpx/icons.go` — `plus`, `pencil`, `x`, `search`,
   `chevron-up`, `chevron-down`. The registry had 32 entries and `trash-2`, but
   **none** of the vocabulary this card mandates.

Plus the binding pattern doc at `ut-docs/reference/list-and-dialog-pattern.md`,
which #2012's nine child cards point at, and a pointer to it from
`ux-guidelines.md`.

**No ADR.** ADR-0007 reserves ADRs for architectural decisions future work must
not contradict; this is a presentation convention, and ADR-0088 already owns the
decision it sits under ("the domain model does not fork per vertical — only its
presentation does"). Recorded in the doc so it is not re-litigated.

**ut-docs#1999's first half is settled here.** These dialogs are `.show()`n, not
`.showModal()`d — the on-screen keyboard is appended to `<body>` and
`showModal()`'s inert behaviour makes it unreachable on kiosk hardware
(ut-docs#1385) — so Escape is inert unless implemented by hand. It now is, bound
per dialog rather than on `document`, sharing one discard guard with Close.
#1999's *second* half (does "status/lock/exit must always be reachable" scope to
the sale flow or to every surface?) is a product rule and stays with a human;
both halves are written up on that card.

## What the Tester pass found before review

A real visual defect, on the real pilot tablet: the list card was full-bleed but
its table only painted to its own content width — **495px of table inside a
1155px card** at 1280×800, more than half the card empty. Cause was
`.list-card .table { display: block }`, the scroll-container trick copied from
`.users-list`: it turns the `<table>` into a block box, so `table.table`'s own
`width: 100%` sizes the *box* while the internal table boxes still shrink-to-fit
and sit at the start edge. Fixed by keeping the element a real table and moving
`overflow-x` onto a `.list-scroll` wrapper. Pinned by case `(f)` as a ratio of
painted width to card inner width, and written into the doc, because the wrong
version is the one a reader would naturally copy.

## What the independent review found

Opus subagent (`complexity:hard` → Opus review per `MODEL-ROUTING.md`), isolated
worktree. **Verdict: NOT SAFE TO MERGE.** Three blockers, all reproduced by
execution, all fixed.

**B1 — the action fallbacks read a *mutated* attribute.**
`form.setAttribute('action', row.getAttribute('data-record-action') || form.getAttribute('action'))`
looks like a fallback to the server-rendered default. It is not: a previous
`open()` already overwrote that attribute. Demonstrated for all three call
sites — open row A, close, open a row with no `data-record-action` → the form
still posts to A; and with an empty `data-create-action`, **New** opens in
create mode still pointing at A, so Save overwrites an existing record instead
of creating one. Not reachable through `/categories`' own markup, but this is
the shared file a binding doc tells nine screens to copy. Fixed by capturing the
rendered action **once** into `data-record-default-action`, setting the
destructive action unconditionally (cleared when the row carries none), and
refusing to open a row that has no action at all with a developer-facing
`console.error` — failing loudly beats editing the wrong record.

**B2 — `dict` fails loudly only on arity; a misspelled key was silent.**
Rendering `record_dialog` without `formID` returned **HTTP 200** with `form=""`
→ `document.getElementById("")` → `null` → Save did nothing and the discard
guard silently disabled. The `dict` comment and the doc both asserted it failed
loudly; both were wrong. Fixed with a `req` template func called as the first
line of each partial, erroring by name on any required key that is absent, nil
or empty, and the false claims corrected.

**B3 — keyboard focus escaped the open dialog.** Twelve Tab presses from the
name field walked out to `a.sb-item`, `body`, five nav-rail links,
`#bugreport-toggle`, `.help-hint`, the search box, New, then a row button —
every one underneath an opaque full-screen dialog, so the focus ring was
invisible — and the dialog's own Close and Save were never reached going
forward. A **regression**: the old side-by-side form had ordinary tab order.
Fixed with a real focus trap (Tab/Shift+Tab cycling over *visible* focusables,
a `focusin` backstop, focus restored to the opener on close), whitelisting
`#osk` — which is a `<body>` child by design and is the entire reason these
dialogs use `.show()`.

Should-fix, all applied:

- **S1** — `open()` skipped the discard guard, so New over a dirty edit silently
  emptied the field. Both paths now share one `confirmDiscard()`.
- **S2** — the OSK fallback was `15.5rem`. `origin/main` merged `be61307b`
  (PR #1027 / ut-docs#1998) *while this card was being built*, moving every
  other reservation to `17rem` precisely because 15.5rem under-reserved — and
  because this block is appended at end-of-file it **merges cleanly**, so the
  divergence would have landed silently. Now `17rem`, with a comment saying the
  fallback must track the others.
- **S3** — the `aria-live` message line was dead markup: nothing ever wrote to
  it, while it permanently reserved 1.3rem of a pinned head ut-docs#2000 already
  calls too tall at 360px, and advertised an error surface that does not exist.
  Removed rather than left as a promise the code does not keep. The real gap it
  papered over — a refused save closes the dialog and discards what the operator
  typed — is filed as **ut-docs#2020** with the `deactivate_blocked` repro.
- **S4** — `/categories` reorder has **never worked from a browser**: the page
  posts `multipart/form-data` via `FormData`, the handler called only
  `ParseForm`, which ignores multipart bodies → `400 ids required` every time.
  The row swaps client-side, the request fails, the JS reloads, and the order
  snaps back, so it reads as "the arrows do nothing". Pre-existing, but pulled
  into this card rather than left on **ut-docs#2018** for two reasons the review
  surfaced: the new e2e spec *stubbed* that route so the suite could not see it,
  and the help rewrite re-asserts the reorder step **in five languages**.
  Shipping a manual describing an action that returns 400 was not acceptable.
  Fixed with `ParseMultipartForm` + `ParseForm` fallback, mirroring
  `buttons_api.go`, which already carries a comment naming this exact trap.
- **S5** — the slot templates could not reach page data: `{{ template "…" . }}`
  passes the *dict*, and `$` inside a `define` binds to that same argument.
  `/categories`' single text input does not care, but `/users`, `/registers`,
  `/tables`, `/kitchen-stations` and `/country-settings` all need a `<select>`
  and would have rendered it empty at HTTP 200 with no error. `"root" .` is now
  passed, used at the reference call site, and documented in the skeleton.

Nits applied: `common.search` was added to four locales and never used (removed
— brand new, so no pack churn); `categories.empty` said "tap **New**" while the
button is icon-only, now "tap +", matching the help topic; the discard-message
assertion matched the untranslated *key* as well as the value, tightened to the
resolved string; `htmx:afterSwap` re-binds dialogs but did not re-apply an
active filter, which would leave a `<tbody>`-only swap unfiltered.

Doc corrections: the false "fails loudly" claim, `.root`, the caveat that
`catalog` and `/tax-codes` render through `RenderWith` with private file sets
and **do** need a Go change (the doc implied no screen did), the flip side of
per-dialog Escape binding, and a "Where errors surface (today)" section.

**`categories.rename` is deliberately left orphaned.** Removing a core key
fails the `ut-plugin-language-{de,es}` packs' own orphan guard until they drop
it too — a three-repo change for a dead string. Noted here so it does not read
as an oversight.

## One finding the reviewer did not raise, added afterwards

The focus trap's `focusin` backstop pulls focus back whenever it lands outside
the record dialog. Harmless on `/categories`, but several ut-docs#2012 screens
also carry `#plugin-install-modal`, `#hold-modal` and similar. A dialog opening
*on top* would have had its focus yanked out — the trap causing the exact bug it
exists to prevent. Any other open `<dialog>` is now whitelisted alongside `#osk`.

## What was verified beyond automated tests

**On the real pilot tablet (TECLAST P50T, v0.14.6, 1280×800 landscape), over
adb, with real touch — not emulated:**

- Row tap opened the full-screen dialog in edit mode, "Edit category", name
  prefilled, trash at the opposite end of the head from Close/Save.
- Typing in the search box filtered the list live.
- Screenshots taken **and looked at**. The first one is what exposed the
  half-empty card above; the second confirmed the fix and the reorder chevrons'
  disabled edge states.

**Measured, not eyeballed**, at 1024×600 / 1280×800 / 360×780: no horizontal
document overflow at any width; the New button is a 51–54px square; rows are
70–73px tall; first-row up and last-row down are genuinely `disabled`.

**RTL** driven in both `fa` and `ar`: `direction: rtl`, the New button moves to
the end edge, the search icon to the start edge, and the dialog's title and
action groups swap sides. Screenshot read — nothing clipped or overlapping.

**The three fixes were re-verified personally by reverting each one**, not taken
on the implementer's word:

- `req` neutered → `TestListHeader_MissingRequiredKeyFailsAtExecute` fails with
  "without \"searchID\" the header rendered 1144 bytes instead of failing";
  restored → `ok`.
- multipart branch removed → `multipart (what the browser's FormData sends):
  code=400 body="ids required\n", want 204`; restored → `ok`.
- focus trap removed → `(c4)` fails with "Tab #1 landed on a.sb-item.sb-enrol,
  outside the open dialog"; restored → passes.

Full gate on the merged tree: `go build`/`go vet`/`gofmt` clean, `go test ./...`
green except the pre-existing `mobile.TestStart_ListensOnAllInterfaces…`, which
**reproduces identically on `origin/main`** and is environmental (it dials a VPN
interface address on the build host). All 11 CI guards pass, including
`guard-docs-shots` after regenerating on the merged tree. **e2e: 428/428.**

## Deferred, with cards

- **ut-docs#2020** — a refused save closes the dialog and discards the
  operator's input. Needs the forms converted to htmx; a change of shape, not a
  nit. Worth landing before #2012's nine screens inherit the gap.
- **ut-docs#2012** — roll the standard out to the other nine screens.
- **ut-docs#1999** — the nav-rail half, waiting on the product owner.
- `catalog.html` adopting `.record-dialog*` — deliberately untouched here;
  `universal-till#1012` and `#1017` are both open in that file under
  `lane:cloud-41`, and it needs a Go change for its `RenderWith` file set.
- The language-pack follow-up (`ut-plugin-language-{de,es}`) for the six new
  core keys and the reworded `categories.empty`, owned by this same cycle.

## Verdict

**SAFE TO MERGE** after the fixes above.
