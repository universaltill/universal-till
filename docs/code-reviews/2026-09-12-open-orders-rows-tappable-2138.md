# Code review: open-orders rows are tappable (ut-docs#2138)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2138
**Branch:** working tree on `main` (uncommitted at review time)
**Complexity:** medium (Review: Opus 5, independent of the authoring instance)

## What was reviewed

`/open-orders` listed parked sales it could not open. The change makes each row
a real resume control: `POST /api/pos/resume`'s handler body is extracted into a
package-level `resumeHeldSale(ctx, d, repo, posRepo, id) resumeOutcome`, a new
`POST /open-orders/resume` route calls the same function and redirects, each row
becomes `<td colspan="5"><form><button>`, an `?err=` banner is added, the help
topic is updated in all five shipped locales, and four Go tests are added.

Ten files, +311/-93. Reviewed against the card's six acceptance criteria and the
repo's own non-negotiables.

**Verdict: not ready as it arrived.** The Go side is sound — the extraction is
faithful and the route matches every applicable convention. The CSS shipped
three defects, two of them user-visible on every till at every viewport, and one
of the four new tests asserted nothing. All four are fixed here; three further
items are flagged rather than fixed.

## Findings — fixed

### 1. The column headers pointed at the wrong values, at every viewport

The header row and the value row were laid out by two different algorithms and
did not agree. Measured offsets between each `<th>` and the value under it:

| viewport | label | table | items | total | open for |
|---|---|---|---|---|---|
| 360x740 | +10 | +338 | +293 | +188 | +121 |
| 1024x600 | +10 | +339 | +293 | +188 | +120 |
| 1280x800 | +11 | +356 | +308 | +197 | +126 |

(px, real Chromium, the shipped `app.css`. The pre-change table measured **0 on
every column at every width** — this is a regression, not a pre-existing flaw.)

The cause is a CSS comment that states something the CSS cannot do:

> The button's flex children (`.open-order-cell-*`) share the same flex-basis as
> their `<th>` counterpart so the two rows still read as aligned columns

`flex` is inert on a `<th>` whose `<tr>` is still `display: table-row` — the
`<th>`s were sized by the table algorithm, the values by the button's flex box.
The classes were on the `<th>`s and did nothing. On a list carrying money, the
"TOTAL" header sat ~190px away from the total.

**Fixed** by making the header row the same flex box as the button, over the
same classes, gap and padding — the only construction in which the two can line
up. After: 0px on every column at 360px, and 0px on items/total/age with 7px on
the table column at 1024x600 and 1280x800.

### 2. About a quarter of each visible row was not tappable

The row button did not fill the row. At 1024x600 the button was **692px wide
inside a 915px list**; at 1280x800, **726px inside 1166px**. The header spanned
the full width, so the row *looked* full-width — a cashier tapping the right end
of a row, under the "OPEN FOR" heading, hit nothing.

`.users-list .table { display: block }` (the pre-existing escape hatch) makes the
internal table box shrink-to-fit, so `width: 100%` on the button resolved against
a shrink-to-fit `<td>`, not the list. The card's own criterion — *"the row is a
real control at the product's 46px touch floor"* — was met on height and missed
on width.

**Fixed** by blockifying `thead`/`tbody`/`tr` **and `td`**. The `<td>` matters
independently: a table-cell under a blockified row is wrapped in an anonymous
table box which also shrink-to-fits (with the row blockified but the cell left
alone, the button measured 412px inside 915px — worse). After: the button is
915px/1166px, i.e. the full list width, at both kiosk and tablet sizes.

### 3. The 360px phone floor regressed 1.8x, on a control you tap

At the repo's 360px floor the new row was **692px wide** (pre-change: 393px) —
every row needed ~365px of sideways scrolling to reach the money and the age, on
an element whose whole purpose is to be tapped.

This came from the Tester-pass revision. Its diagnosis was half right: money and
age must never be truncated, and giving them `flex: 0 0 auto` is correct. But it
put the **table name** in the same "always short, known-shape content" bucket.
A table name is merchant-typed free text ("Terrasse hinten links 12") — the
second unbounded field on the row, not a known-shape one.

**Fixed:** the table name now shrinks and ellipsizes (`flex: 0 1 auto` with a
`min-inline-size` floor); item count, money total and age keep `flex: 0 0 auto`
and still never truncate. 360px row width is back to 387px (pre-change 393px)
with the money and age intact — the original fa/RTL bug stays fixed.

Also removed here: `text-align: end` on the money/items/age cells. It was inert
in the body (shrink-to-fit flex items) while genuinely right-aligning the
headers — a fourth way the two rows disagreed. Every cell is now `start`, which
is what the page did before this card.

Verified under `dir="rtl"` with Farsi digits and currency: columns mirror
correctly, money and age take their natural width (91px/110px/116px — never
clipped), nothing overflows the page.

### 4. One of the four new tests asserted nothing

`TestOpenOrdersPage_RowsAreRealResumeButtons` ended with:

```go
if strings.Contains(body, `<tr data-held-id="h1" onclick`) {
```

That literal has never been emitted by this template, in any version. The test
passed against the broken page and the fixed one identically — exactly the
failure mode the card's "real control, not a `<tr>` click handler" criterion
needed pinned.

**Fixed:** the assertion now runs over the `<tbody>` only (so the base layout's
own htmx wiring cannot satisfy or break it) and requires that no script hook of
any kind — `onclick`, `onmousedown`, `ontouchstart`, `hx-post`, `hx-get`,
`data-record-open` — appears in the rows, plus that the row is one full-width
`<td colspan="5">`. The property that actually matters is that resume works with
JavaScript off; that is now what is checked.

Mutation-verified both ways: adding `onclick="resume()"` to the `<tr>` fails it,
dropping the `colspan` fails it, and the template restored to the diff's state
passes.

## Checked and found sound

- **The extraction is faithful.** Compared mechanically (statement sequence,
  comments and blank lines stripped) against `HEAD:internal/pages/hold_api.go`.
  The sequence is identical: `id == ""`, then `HasItems()` **before any DB
  read**, then `repo.Get`, `json.Unmarshal`, `snap.TableID = held.TableID` with
  the ut-docs#820 label re-resolution, `prevTable`, `RestoreHeld`,
  claim-before-delete (ut-docs#1390), release of the previous claim, `Delete`
  last and non-fatal. The only differences are the signature and each
  `renderBasket(...) + return` becoming `return resumeX`. No reordering.
- **Auth.** `/open-orders/resume` matches nothing in `auth.exempt()` — not the
  exact-match list, not the sync switch, not any prefix (`/api/auth/`,
  `/public/`, `/themes/`, `/plugin-icons/`, `/o/`, `/self-order`). It is
  operator-gated like `GET /open-orders`. No manager gate, correctly: this is a
  cashier surface, same audience as the strip and the popup it mirrors.
- **CSRF.** This codebase has no CSRF token anywhere. The convention is a
  `SameSite=Lax` session cookie plus method-scoped mutating routes — documented
  in `kitchen_stations_page.go` and `bluetooth_devices_page.go`, and pinned by
  `bluetooth_devices_page_test.go`. `POST /open-orders/resume` is POST-only with
  no GET variant, so it matches exactly; Lax withholds the cookie from a
  cross-site form POST, which is the whole protection the convention relies on.
- **Replica/primary gate correctly absent.** `tables`, `catalog` and `registers`
  gate mutations behind `requirePrimary`. `held_sales` does not sync at all —
  `sync_admin_repo.go:348` excludes it by name, with reasoning — so resume is
  purely local and a gate would be wrong. Matches `POST /api/pos/resume`.
- **i18n.** `hold.error.busy`, `hold.error.not_found` and `hold.error.failed` all
  pre-exist in `en/fa/tr/ar`. No new key, so no language-pack follow-up is
  implied. Locale re-resolution across the redirect is fine *because* `?err=`
  carries a key and not a rendered message: the next GET resolves the locale
  from the `ut_lang` cookie exactly as any page load does. `guard-i18n` green.
- **No hx-boost surprise.** The layout does not boost globally; `hx-boost` is
  opt-in per form (`categories.html`). The plain form really is a full-page
  navigation and the browser follows the 303 itself.
- **Non-negotiables untouched.** No SQL outside `internal/data` (guard green);
  `money.Money` not touched; nothing added to the checkout path; no modal
  blocker; the nav rail (status/lock/exit reachability, ut-docs#1999) still
  renders — the existing empty-state test asserts it.
- **Redirect target.** `GET /` is the sale screen on an ordinary till, and this
  page's own "← Back to sale" button already targets `/`. See N1 for the caveat.

## Flagged, not fixed

**N1 — `"/"` is wrong on two display modes (needs a product decision).** On a
`display.mode=self_order` or `backoffice` till, `GET /` re-dispatches
(`index_page.go`) to `/self-order` or `/backoffice`. A successful resume would
load the basket and land the operator somewhere that is not the sale screen, so
the card's *"shows the sale screen with its basket loaded"* would not hold there.
**Pre-existing, not introduced:** the page's own back button has the same target,
and this codebase has no route that unconditionally renders the sale screen.
Fixing it means a new route or a mode-aware redirect helper — out of scope for a
medium card.

**N2 — `?err=` renders any key, or any text (pre-existing pattern; own card).**
`errKey` is `r.URL.Query().Get("err")` passed straight to `{{ T .errKey }}`, and
`httpx.T` falls back to the key itself, so `/open-orders?err=Your+card+was+
declined` renders attacker-chosen copy in a red banner. Not XSS — `html/template`
escapes the text node. This is the identical shape used by
`country_settings_page.go`, `kitchen_stations_page.go`, `promotions_page.go`,
`tables_page.go` and `categories_page.go`, so the new route follows convention
exactly; whitelisting only here would diverge without closing the hole. Worth a
card across all `?err=` consumers.

**N3 — the row's accessible name (needs a new i18n key, so needs a decision).**
The button's accessible name is its five spans concatenated — *"Sarah Window 2 3
£12.50 90 min"* — with no statement of what activating it does, and the `<th>`s
no longer associate with values (the row is one button; the fields are spans).
Fixing the columns visually does not fix this. The obvious remedy is an
`aria-label` naming the action and the order, but no existing key says "Resume"
(`hold.toast.resumed` is *"Sale resumed"*), so it needs a new key in en/ar/fa/tr
plus the language packs. Left open — it is a real gap against the ut-docs#826
gate that this diff's own comments invoke as satisfied.

**N4 — nit.** `.open-order-row-btn` hardcodes `min-height: 46px` instead of
reusing `.btn-touch`. Justified (`.btn-touch` is `display: inline-flex`, which a
full-width flex row cannot take) and now documented in the comment, but it does
put the product's touch-floor constant in a second place that will not move with
the first.

**N5 — note.** Double-tapping a row fires two POSTs; the second sees a non-empty
basket and shows the busy banner even though the resume succeeded. Harmless — no
data loss, the order is resumed — but momentarily confusing. Guarding it needs
client JS, which this deliberately script-free row does not have.

## Process observations

**The "German check" did not check German.** The hand-off states the `?lang=de`
run turned out to exercise English strings with German number formatting, since
German UI copy ships via `ut-plugin-language-de`, not installed on the throwaway
till. `ux-guidelines.md` requires checking a layout against a long-form locale
pack, and that requirement was therefore not met — the diff reached review
carrying a German visual check that did not test German copy. Findings 1 and 3
were both found with synthetic long-locale content; finding 3 is a long-string
failure specifically.

**Nothing automated would have caught any of findings 1-3, and nothing does
now.** Go tests do not evaluate CSS and there is no e2e spec for this page. The
repo's own convention is to pin a measured layout fix with an e2e case —
`.list-card .list-scroll`'s comment cites `categories-record-dialog-2010.spec.ts`,
and the ut-docs#2137 record cites five. The fix here is verified by direct
measurement in real Chromium, **not** by a committed regression test. An e2e
spec for this page is recommended before this row-as-one-button pattern is
reused anywhere else.

**Review environment.** The hand-off described an isolated worktree of
`universal-till`; in fact `/home/user/universal-till` is a plain checkout on
`main` carrying the uncommitted change, and edits were made there directly as
instructed. The mutation experiments (finding 4, and a negative control removing
the `HasItems()` guard, which correctly fails
`TestOpenOrdersResume_BusyRefusalKeepsOrderParkedAndListed`) each restored the
file immediately, and `git diff --stat` was confirmed back to the original
figures after each. The authoritative suite run below was made after every
restore — this is the ut-docs#2135 hazard the ut-docs#2137 record warns about,
and it is worth noting that the prescribed worktree isolation was not actually
in place.

## Verification

Re-run after all fixes, with the tree in its final state:

- `go build ./...`, `go vet ./internal/pages/`, `gofmt -l internal/pages/` — clean.
- `go test ./internal/pages/... -count=1` — **green** (275s), all five packages.
- `go test ./internal/pages/ -run TestOpenOrders -count=1` — green.
- `scripts/ci/guard-i18n.sh` — green (1648 template keys resolve; all locales
  match `en.json`).
- `scripts/ci/guard-data-access.sh` — green.
- `scripts/ci/guard-page-http-error.sh` — green.
- `go run ./scripts/ci/checkhelpdrift` — green (only the recorded pre-existing
  `tax-codes` / `till-designer` / `vouchers` / `translations` baseline entries).
- Layout: real Chromium (Playwright 1.56 / chromium-1194), the shipped
  `app.css`, at 360x740, 1024x600 and 1280x800, in LTR with English and long
  German content and in `dir="rtl"` with Farsi digits and currency. Before/after
  geometry as tabulated above. Scratch harness, not committed.
- Mutation controls: `onclick` on the `<tr>` and a dropped `colspan` each fail
  the rewritten control test; removing the `HasItems()` guard fails the busy
  test. All reverted, `git diff --stat` confirmed back to the original figures.

## Changes made by this review

- `web/public/app.css` — findings 1, 2 and 3, with the comment rewritten to
  describe what the CSS actually does (the previous one asserted a shared
  flex-basis between `<th>` and span that cannot exist).
- `internal/pages/open_orders_page_test.go` — finding 4.

No change to `hold_api.go`, `open_orders_page.go`, the template or the help
topics: those were reviewed and found sound as written.

## Addendum — one more finding, independently re-verified at close-out

Before committing, the orchestrating session independently re-drove the fixed
page in a real Chromium (fresh throwaway till, same demo-seed method) rather
than trusting this record's screenshots, and re-measured all three viewports.
Headers/values matched (0px offset) and the row filled ~90-97% of the list
width at 1280x800/1024x600/360x740, confirming findings 1-2 above hold.

**Finding 5 (new): at the 360px floor, a long cashier-typed label had no
floor and vanished (0px), taking the "ORDER" header with it.** Reproduced
with a held sale labelled `Terrasse hinten links 12` (the review's own example
string, used here as the order label rather than the table name it was
originally illustrating finding 3 with — a held-sale label is equally
unbounded, up to `maxHoldLabelRunes` = 64 runes, e.g. a customer name). The
label's rule (`.open-order-cell-label`) had `min-inline-size: 0`, so under
space pressure from the four floored, never-shrinking fields beside it, it
shrank to nothing instead of ellipsizing — worse than truncation, and the
header vanished with it since both share the same flex layout.

**Fixed:** `min-inline-size: 3rem` on `.open-order-cell-label` (it now
ellipsizes instead of disappearing), plus `overflow-x: auto` on
`#open-orders-table` as the escape hatch for the case where even every
field's floor together still doesn't fit — mirroring this pattern's own
documented `.list-scroll` convention for exactly this class of overflow
elsewhere in `reference/list-and-dialog-pattern.md`. Re-verified: at 360px
the label now reads `Terr…` (ellipsized, visible) with its header aligned
above it, and scrolling the table horizontally reaches Items/Total/Open for
in full (`1`, `£1.20`, `3 min`), all still un-truncated.

Re-ran after this fix: `go test ./internal/pages/... -run OpenOrders
-count=1` (green), `guard-i18n.sh` (green), `guard-data-access.sh` (green).
No Go or template change, CSS only, so no test asserts this geometry —
carries the same "measured, not regression-pinned" caveat this record
already raises about findings 1-3; strengthens the case for the recommended
e2e spec follow-up.

Scratch Playwright specs used for both driven passes were not committed
(`e2e/tests/tmp-*.spec.ts`, deleted after use), consistent with this
record's own "scratch harness, not committed" note above.
