# 2026-09-10 — Inventory stock-row tap opens a full-screen receive/adjust dialog (ut-docs#2011)

## What shipped

On `/inventory`, tapping a stock row used to fill a Receive/Adjust form sitting
further down the page — the product owner's own report was "the form is at the
end". The form now lives inside a full-screen `<dialog class="record-dialog">`
that the row tap opens, prefilled with that row's item and location, with a
plain-language confirmation line naming what is about to change. A new icon-only
`+` button in the page head opens the same dialog blank, for an item that is not
in the visible list. Override and Return are untouched, still in their own
`<details>` accordions outside the dialog.

The dialog reuses the ut-docs#2010 `.record-dialog` CSS shell but deliberately
**not** the shared `record-dialog.js` engine: that engine assumes plain
POST → 303 → full navigation per mutation, whereas `/api/inventory/receipt` is
an htmx swap-in-place endpoint (`hx-post`, `hx-target=#result`,
`HX-Trigger: stock-updated`, no redirect) and this card's AC requires no full
page reload. Open/close/Escape/prefill are therefore this page's own ~40 lines
of script. It is `.show()`n, not `.showModal()`d, so the on-screen keyboard
stays reachable (ut-docs#1385) — the same reasoning `record_dialog.html`
carries.

`GetLowStock`'s hand-built HTML fragment gained the same `class='stock-row'` +
`data-*` attributes, so the one delegated listener covers a low-stock row too;
the page's stock-search filter was re-scoped to `#stock-table` so it no longer
reaches those rows. Four pre-existing e2e specs were updated because
`#stock-form`'s fields are no longer visible without opening the dialog. New
i18n keys in all four shipped locales, a manual clause in all five help locales,
and regenerated screenshots.

## What the independent review found

Fresh-eyes review in an isolated worktree, everything below reproduced by
execution rather than by reading. **Verdict: safe to merge after five fixes**,
all applied here.

### F1 — CI was red: `guard-docs-shots` (blocking) — FIXED

`make docs-shots` had been run **before** the `app.css` edit landed, so the
manifest's `surface_sha256` described a tree that no longer existed.
Reproduced and isolated exactly, not guessed: restoring only `web/public/app.css`
to its parent revision made the guard's independently-computed hash equal the
committed manifest's (`6e43f81e…`), proving that one file was the whole
difference. Regenerated (`make docs-shots`, 112 shots, surface `14e9422f…`);
guard green.

Worth noting for the next card: `web/public/**` counts toward that hash, so a
CSS-only touch after the screenshot run turns CI red with a diff in which
nothing visibly changed.

### F2 — a saved result leaked into the next open of the dialog — FIXED

`resetStockForm()` calls `form.reset()`, which resets form *fields*. `#result`
is a `<div>`, not a field, so it was never cleared. Reproduced live on a real
till: save against item A → close → tap item B, and B's freshly-blank form opens
with **"Stock movement created: 086b07f9-…"** from A sitting underneath it. On a
stock-adjustment screen that reads as "B's change has already been applied".

This did not exist before the card — the form was permanently on the page, so
the message was only ever seen in the context that produced it. Turning the form
into something opened and closed repeatedly is what created it.

Fixed by clearing `#result` in `resetStockForm()` (deliberately there and not on
close: leaving the confirmation visible after a submit is intentional, and the
comment now says which of the two behaviours is load-bearing). Covered by a new
assertion appended to e2e case `(d)`, re-verified to fail without the fix.

### F3 — the `app.css` rule is a no-op, and its comment claimed otherwise — FIXED (comment)

The new rule was committed as a fix for a genuine 360px overflow "caught live by
inventory-stock-dialog-2011.spec.ts's viewport check". Neither half held up.

Removing the rule and re-running the spec at 360×740 against a freshly built
till: **the viewport case still passes.** Measuring every box in the dialog with
and without it: geometry is byte-identical — `#stock-form` 326px, each
`.field-pair` column 158.8px, nothing past the dialog's edge either way. The only
difference is the inputs' computed `min-width`, `auto` → `0px`, with no layout
consequence.

Cause: every field here wraps its control in a `<label>`, and ut-docs#300's
global `label:has(> input)` rule already makes that a **column** flex container.
A flex item's automatic minimum size applies in the main axis only, so
`min-width: auto` is already 0 in a column container and the input already
shrinks with its grid track. The `.catalog-form`-scoped precedent this rule was
copied from is reached by the same global rule.

The first e2e run against a reverted `app.css` also *passed for the wrong
reason* — `reuseExistingServer` kept the previous till alive, and `web/public`
is `//go:embed`ed, so the running binary still carried the fixed CSS. Anyone
re-checking a CSS claim through this suite must kill the till first; the result
above is from a rebuilt one.

**Kept, with the comment corrected.** The rule is a real guard-rail for the case
the global rule does not cover — a control placed directly in a `.field-pair`
rather than inside a `<label>` — and it costs nothing. What is not acceptable is
a comment asserting a bug that was never reproduced; a future reader would
"fix" a phantom on top of it. The comment now states plainly that it is
defensive, that removal was measured to change nothing, and why.

### F4 — the manual named a button that does not exist on screen — FIXED

The added clause read "or the **Add stock** button". The button is icon-only —
"Add stock" is only its `aria-label`/`title`. `web/help/en/categories.md`, written
one card earlier for the identical control, says "Tap the **+** button beside the
search box"; that is the convention, and it describes what a merchant actually
sees. Reworded in all five locales (en/de/ar/fa/tr), swapping only the bolded
token and its "beside the search box" locator so each file's own sentence and
step structure is untouched — `guard-help-drift` stays green, bold-lead-in counts
unchanged. The regenerated `en` screenshot confirms the `+` sits exactly where
the text now says it does.

### F5 — the context line built its text with chained string `.replace()` — FIXED

```js
T.dialog_context.replace('{item}', label).replace('{location}', locName)
```

Two defects, both from merchant-typed free text. A **string** replacement expands
`$&` / `` $` `` / `$'` / `$$` inside the *replacement*, so an item name or SKU
containing `$&` renders mangled; and chaining means an item name containing the
literal `{location}` is itself substituted by the second call. Replaced with one
pass and a **function** replacement, which is taken literally and never re-scans
what it just inserted. Cosmetic-only (the value goes to `.textContent`, so no
injection either way), fixed because it is two lines.

## Verified beyond the automated tests

**TDD re-verification — the two new Go tests, done personally, not taken on
trust.** Each claimed-covered change was reverted in this worktree and the
specific test re-run:

- `TestGetLowStock_RowsCarryDialogAttributes` — `internal/pages/inventory_api.go`
  restored to its parent revision → **FAILS**, `expected low-stock rows to share
  the stock-row class…`, dumping the old class-less `<tr>`. Restored → passes.
- `TestInventoryPage_StockRowsCarryDialogAttributes` — done twice, because it
  asserts two independent things. Dropping only `data-location-name=` from
  `inventory.html` → **FAILS** on that assertion. Restoring that and deleting
  only the `<dialog>` block → **FAILS** on `expected a #stock-dialog
  full-screen popup`. Restored → passes.

**TDD re-verification of the assertions added by this review.** Both were made
to fail before being trusted: removing the `#result` clear fails `(d)` with
`Received: "Stock movement created: 377b3d3c-…"`; un-scoping the search filter
back to a bare `.stock-row` fails `(g)` with the low-stock row hidden. That
second one also confirms the implementer's filter re-scoping was a real fix —
it simply had no test until now.

**e2e ran for real** (Chromium 141.0.7390.37 from `/opt/pw-browsers`, resolved
through `e2e/scripts/resolve-chromium.sh`; `e2e/node_modules` symlinked in, this
worktree has none). The new spec: 8/8. The four updated specs: 17/17. **The whole
suite, all four projects: 436/436.**

**Live behaviour probed directly**, beyond what any spec asserts: the stale-result
leak (F2) and the dialog geometry claim (F3) were both measured in a driven
browser; opening the dialog twice in a row leaks nothing else (a cost typed and
abandoned is cleared by `form.reset()` on the next open — checked, passes);
`stockDialog.show()` on an already-open dialog is a spec no-op, so tapping a
second row while open re-prefills correctly rather than throwing.

**Two coverage gaps closed** (new e2e cases `(f)` and `(g)`). The low-stock
fragment's rows had no runtime coverage at all — the Go test asserts the server
emits the markup, but nothing asserted that a row in a container htmx replaces
wholesale actually opens the dialog, which is exactly what breaks the day someone
"tidies" the delegated listener off `document` and onto `#stock-table`. It cannot
be driven from the demo till: `reorder_level` has no UI anywhere in the app (it
arrives by import/API only), so the seeded catalogue has no low-stock rows and
the card renders "No low stock items" — verified live. Rather than add DB seeding
for one fragment, the cases reproduce it in place from a real stock row's own
dataset, in the exact shape `GetLowStock` emits — and the Go test independently
pins that the server really emits that shape, so neither half assumes the other.

**House rules, checked individually.** No file writes anywhere in the diff, so no
`os.MkdirAll` and no cwd-relative-path question arises. No new money field; the
`cost_price` path (`window.utCurrency.toMinor` → hidden minor-units input) is
carried across unchanged, still blank-preserving. No raw SQL outside
`internal/data` (`guard-data-access` green; the new test's `dp.Db.ExecContext`
is in a `_test.go`, the same convention `inventory_api_test.go` already uses).
Nothing new blocks on the network — the dialog is client-side and the submit is
the same htmx POST as before. No `/self-order` route is touched at all.
All user-facing strings go through `T`, including the one set from JS, via the
`var T = { … }` template-populated lookup the house rule prescribes.

**Green:** `gofmt -l .` (silent), `go vet ./...`, `go build ./...`,
`go test ./...` (whole repo), `golangci-lint run ./...` (0 issues), and
`guard-i18n` (no duplicate keys, all locales match en.json), `guard-help-topics`,
`guard-help-drift`, `guard-docs-shots`, `guard-data-access`, `guard-htmx-loaded`,
`guard-compliance-claims`, `guard-page-http-error`, `guard-kiosk-engine`,
`guard-autofill-suppression`, `guard-e2e-fixtures-import`. `shellcheck` is not
installed in this worktree; no shell script is touched by this branch.

## UI surface

Design tokens reused, not re-invented: the dialog is entirely `.record-dialog*`
classes from ut-docs#2010, and the `+` button is the same
`btn primary btn-icon` + `aria-label`/`title` pair `list_header.html` mandates
("icon-only is the product owner's explicit instruction; the aria-label + title
pair is therefore mandatory"). The one new CSS declaration is
`min-inline-size` — logical, RTL-safe; no physical `left`/`right` anywhere in
the diff. The dialog head is deliberately one row, so it does not repeat
ut-docs#2000's finding of a head eating 42% of a 360px screen; case `(e)` pins
that at both 1024×600 and 360px. There is no exit trap: an always-visible Close
button in the pinned head, plus Escape, both covered by case `(c)`. This is a
back-office screen, not the checkout path, and nothing here is a modal blocker in
the kiosk flow.

## Accepted as-is

**No discard guard on close.** `record-dialog.js` confirms before dropping a
dirty form on Close, Escape *and* reopen — its own review found that gap ("New
over an edit with unsaved text silently emptied the field"). This dialog has no
equivalent, so a merchant who types a quantity and reason, then presses Escape,
loses it at the next open. That is a real (small) regression: before this card
the form was permanently on the page and nothing could discard it.

Accepted rather than fixed, deliberately. Reset-before-prefill is required by the
AC — carrying a stale quantity into a different item is the far worse failure —
and the engine's own approach does not port cleanly: its submits navigate away,
so "saved, now clean" is implicit, whereas an htmx swap-in-place leaves a form
full of values that were *already saved* and must not then prompt to discard.
The follow-up wants a dirty snapshot re-taken on the `stock-updated`
`htmx:afterRequest`, not a copy of `confirmDiscard`. Bounded impact meanwhile:
five short fields, and the loss needs a deliberate close.

## Deferred

- **`/inventory` overflows horizontally at 360px, and the new `+` button makes
  one part of it worse.** Measured: `.page-head` already overflows without the
  button (scrollWidth 386 vs clientWidth 318) because of the global
  `.page-head input[type="search"] { min-width: 260px }` with no `flex-wrap`;
  the button takes it to 454, sitting at x=424 on a 360px viewport. The
  document's own scroll extent is 512 **either way** — dominated by
  `#stock-table`, the pre-existing overflow the Dev/Tester already found and
  scoped out — so the button does not create the horizontal scroll, it lands
  inside it. Not fixed here: the root cause is a global rule shared by ~25 pages
  and would move every one of their screenshots, which is far outside this
  card. Mitigated in practice — the target hardware is the 1024×600 kiosk, where
  case `(e)` proves it lays out correctly, and the primary interaction (tap a
  row) never needs the button. Worth one card covering both `.page-head` and
  `#stock-table` at phone width.
- **`bindPicker`'s `setCustomValidity('Pick an item from the list')`** is a
  hardcoded English string shown to the user in the browser's validation bubble.
  Pre-existing (unchanged by this branch) and not caught by `guard-i18n`, which
  only scans `.textContent`/`.innerHTML` assignments — a real gap in the guard as
  much as in the page.
- **`GetLowStock` builds HTML by string concatenation** in Go, now with ten
  `%s` interpolations per row, each individually `html.EscapeString`ed. Correct
  today and tested for it, but it is the only table in the app not rendered from
  a template, and the escaping is one forgotten call away from a stored-XSS
  regression. Moving it to a partial that `stock_table.html` and this fragment
  both use would delete the whole class of risk. Out of scope here.

## Verdict

**Safe to merge.** Five findings, all fixed: one CI-blocking (stale screenshot
manifest), one real user-visible defect reproduced live (stale success message on
reopen), one false provenance claim in a comment for a rule that does nothing,
one manual clause naming a button that is not on screen, and one string-building
nit. Two runtime coverage gaps closed. The two new Go tests were personally
re-verified to fail without the code they cover, as were both assertions this
review added; the full 436-case e2e suite passes.
