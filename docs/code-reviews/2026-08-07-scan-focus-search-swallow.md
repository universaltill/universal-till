# Code review: scan while focus is in the sale-screen search box (ut-docs#423)

**Date:** 2026-08-07
**Author (Dev):** scrum-master pipeline, Sonnet
**Reviewer:** independent Opus subagent (fresh context, different model)
**Card:** universaltill/ut-docs#423, filed from the independent review of
ut-docs#418 (sale screen category tabs + search)

## What shipped

`web/public/app.js` only — no Go, no templates, no locales, no CSS.

The card was filed as "focus in the new products-search box *swallows* a
wedge-scanner scan". Dev's investigation found the headline claim was
already false: `app.js`'s global `keydown` scan buffer (`buf`, 100ms
fast-typing window) fires on `Enter` regardless of which element has
focus, so the item *did* already ring up. The real defect was the
aftermath — the scanner's keystrokes are also delivered to the focused
field, so the barcode digits were left sitting in `#products-search` as a
filter query. A barcode matches no product *name*, so from that scan
onwards the whole product panel looks empty until the cashier notices and
clears the box by hand.

The change:
- `scanCodeInput()` resolves the real scan input once, replacing the
  duplicated `querySelector` in `submit()` and in the Enter branch;
  `submit(code, codeInput)` now takes it as an argument.
- `clearStrayScanTarget()` removes the characters the scanner injected
  into whatever non-scan field had focus, then dispatches a bubbling
  `input` event so Alpine's `x-model` on `#products-search` sees the
  change. It never touches the real scan input's own value or lifecycle.
- New Playwright spec `e2e/tests/sale-screen-scan-focus-search-423.spec.ts`.

Offline-first, i18n and repository-pattern rules are untouched by
construction: no user-facing strings, no CSS (so no RTL surface), no
network dependency added to checkout, no SQL.

## Independent review — findings

One BLOCKING regression found and fixed, with new coverage pinning it.
The refactor itself is sound and one incidental behaviour change in it is
an improvement worth recording.

### Fixed: the fix silently swallowed a scan made with focus in the qty box (blocking)

`clearStrayScanTarget()` as written blanked *any* focused `INPUT`/
`TEXTAREA` that wasn't the scan input. The sale screen's scan row has a
second field — `input[name="qty"]`, `required`, `min="1"` — sitting right
next to the code input, and "tap qty, set a quantity, then scan" is an
ordinary cashier flow. Driven live in a real browser:

| focus + scan (barcode `2000010000017`, Coca-Cola 330ml) | `origin/main` | Dev's fix | after this review |
|---|---|---|---|
| empty search box | digits stuck in the filter | cleared | cleared |
| search box already holding `But` | `But2000010000017` | `""` — query lost | `But` |
| qty box at its default `1` | rang up **12,000,010,000,017** units, basket total **£14,400,012,000,020.40** | **scan swallowed, nothing rang up** | rings up 1, qty back to `1` |
| cashier set qty `3`, then scanned | 32,000,010,000,017 units | **scan swallowed** | rings up 3, qty back to `3` |
| typed `Butter`, Enter within 100ms | `Butter` kept | `""` — typed text wiped | `Butter` kept |

Blanking `qty` made it fail its own `required` constraint, so htmx's
pre-request validation blocked the submit outright — the `/api/pos/scan`
request was never even sent (confirmed by a `waitForResponse` timeout, not
inferred). That directly violates the card's own headline acceptance
criterion ("a scan while focus is elsewhere still rings up the item"), just
for a different field than the one the card named, and it is the same
class of bug the card was filed about.

The two right-hand columns above are also worth reading on their own: the
`origin/main` qty row is a real money-shaped bug this card's investigation
surfaced by accident, and the fix as revised now closes it.

Fix: `clearStrayScanTarget()` strips the scanned code as a **suffix**
rather than blanking the field. That is a precise undo of what the scanner
injected — the cashier's own prior content (`But`, `3`) survives, and the
qty the sale actually needs keeps submitting. If the code isn't the field's
trailing text (caret was mid-string, a number input rejected the value,
focus is on a checkbox or file input) nothing is touched at all, which also
removes the "clearing `.value` on a non-text input silently changes what
that control submits" hazard the original had.

A `MIN_SCAN_CLEANUP = 4` floor guards the **cleanup only**, not the submit.
The buffer needs just one fast keystroke before `Enter` to look like a
scan, which is exactly what a human typing a query and pressing Enter
produces; submitting that as a bogus barcode is pre-existing and harmless
(nothing is found), but eating the last character of what they typed would
not be. Every real scan — EAN-8/13, UPC, Code-128 SKUs, 4-5 digit PLUs —
clears the floor comfortably. Deliberately not applied to the submit path,
which would risk breaking a till already scanning short internal codes.

New test `a scan with focus in the qty box rings up the cashier's quantity,
not the barcode` pins it. Verified it fails against `origin/main`
(`basket-count` `32000010000017` instead of `3`) *and* against Dev's
version (`/api/pos/scan` never fired), and passes after the revision.

### Accepted as correct: the `submit()` / `scanCodeInput()` refactor

- `submit()` now derives the form from `codeInput.form` instead of
  re-running the selector. Equivalent here: the input is a plain descendant
  of `form.scan-row` with no `form=` attribute, so `.form` is the same
  element the selector finds. Checked every `/api/pos/scan` site —
  `buttons.html:135` and `suggestions.html:9` are `hx-post` on `<button>`,
  not `<form>`, so the selector still resolves only to `index.html`'s
  scan row. `submit(` has exactly one call site.
- The Enter branch's guard tightened from `if (code)` to
  `if (code && codeInput)`, so `e.preventDefault()` no longer fires on
  pages with no scan form. That is an unadvertised improvement: fast typing
  + Enter on a settings page previously had its Enter swallowed with
  nothing to show for it.
- The `e.isComposing` guard at the top of the handler is untouched, so
  IME/composition input still never reaches the buffer — re-confirmed by
  reading, since `e.key.length === 1` accumulation would otherwise
  mis-buffer composed characters.
- No `os.MkdirAll`/`paths.Data(...)` exposure (the two bug classes this
  pipeline keeps finding) — this diff writes no files and touches no Go.
- No real client or shop name in test data (the demo seed's Coca-Cola /
  Butter tiles, already shipped), no secret-shaped literals.

### Fixed (cosmetic, same round)

- The first test's comment claimed "a second scan-eligible tile is still
  visible afterward" but asserted nothing of the kind. Added the missing
  assertion rather than deleting the claim — it's the symptom a shop owner
  actually sees.
- Widened the `describe` title from "in the sale-screen search box" to "in
  another sale-screen field", since the block now covers qty too.

### Deferred (real, out of this card's scope)

- The scan buffer's false-positive shape is still there on the submit
  side: any single character followed by `Enter` within 100ms fires a
  bogus `/api/pos/scan`. Harmless today (item not found) and pre-existing,
  but `#products-search` has no `Enter` handling of its own, so pressing
  Enter in a search box does something faintly wrong. Worth a card that
  gives the search box real Enter semantics.
- Scanner-injected characters that land mid-string (caret not at the end)
  are left alone by design. Recoverable by hand, and preferable to
  guessing.

## Verified beyond automated tests

- **TDD claim re-verified personally, not taken on trust.** Reverted
  `web/public/app.js` to `origin/main`, re-ran the new spec: the scan test
  failed on exactly the claimed symptom (`#products-search` holding
  `"2000010000017"`) while its basket assertion passed — independently
  confirming Dev's finding that the item already rang up pre-fix and the
  card's "swallows the scan" framing was wrong. Restored, re-ran, green.
- Drove the qty, partial-query and human-typing scenarios in a real
  browser against all three versions of the file (`origin/main`, Dev's,
  revised) to build the table above — that is how the blocking regression
  was found; no test in the diff covered a second focusable field.
- Confirmed the one `go test ./...` failure
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`) is the
  known pre-existing one: it asserts a write into a `0o500` directory
  fails, which uid 0 bypasses, and this sandbox runs tests as root. This
  diff contains no Go at all, so it cannot be implicated. Tracked as
  ut-docs#415.
- Confirmed `e2e/tests/catalog-image-to-till.spec.ts` fails identically on
  a clean `origin/main` `app.js` (a 2×2 PNG never reaching
  `complete === true` under this sandbox's substituted Chromium build) —
  environmental, not a regression.

## Manual (`web/help/`)

**No update needed, confirmed by reading rather than by guard script.**
No screen, control or workflow step changes: `web/help/en/sell.md` step 1
already reads "Scan a barcode, or find and tap the item on the sell
screen: switch between category tabs, or type into the search box…", which
this change makes true again rather than altering. Nothing in the manual
described the broken behaviour, so no prose has quietly gone false and no
screenshot shows a superseded UI. `guard-help-topics.sh` green.

## Gate — green

`go build ./...`; `go test ./...` (one pre-existing root-only failure,
above); `bash scripts/ci/guard-data-access.sh`; `bash scripts/ci/guard-i18n.sh`
(856 keys, all locales matching); `bash scripts/ci/guard-help-topics.sh`.

Playwright `--project=default`, full suite: **104 passed, 1 failed** — the
pre-existing `catalog-image-to-till` image-decode failure above. Targeted
regression sweep around the changed handler (`sale-screen-scan-focus-search-423`,
`sale-screen-category-tabs-search-418`, `sale-screen-213`, `settings-osk`,
`designer-search`): **19 passed, 0 failed** — `settings-osk` and
`designer-search` matter here because both drive keystrokes through the
same global handler via the on-screen keyboard.

## Verdict

**Safe to merge** with the review fix applied. The shipped intent was
right and the diagnosis was better than the card it came from; the
implementation traded one stray-field bug for a worse one in the field
sitting immediately beside it, which is now fixed and pinned by a test
that fails against both prior versions.
