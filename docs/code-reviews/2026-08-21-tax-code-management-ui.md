# Code review: tax-code management UI (ut-docs#259)

**Branch:** `feat/259-tax-code-management-ui` (based directly on `origin/main`
@ `616c1ec`, no merge conflict)
**Reviewed commits:** `be132e7` (feature), `21ae2d7` (checkbox/CSS fix +
migration-level permission test)
**Reviewer:** independent, different-model, fresh-context pass in an isolated
worktree — no reliance on the implementer's own claims.

## What shipped

A manager-gated CRUD admin screen for `tax_codes`, reached from a new button
on Catalog.

- `internal/pages/tax_codes_page.go` (new): `GET /catalog/tax-codes`,
  `POST /api/catalog/tax-codes` (create), `POST /api/catalog/tax-codes/update`
  (edit **and** activate/deactivate through the one endpoint). All three gated
  on a new `tax_code_management` action via `canPerform`. **No delete route,
  no delete repo method, no delete control in either template** — confirmed by
  grep, not taken on faith. Correct: `tax_codes.id` is FK-referenced by
  `items.tax_code_id` (`001_init.sql`), so retirement is a flag, not a DELETE.
- `internal/data/catalog_repo.go`: `CreateTaxCode`, `UpdateTaxCode`,
  `GetTaxCode`, `ListAllTaxCodes`, plus `ErrTaxCodeNameExists` /
  `ErrTaxCodeNotFound`. `TaxCodeView` grew an `IsActive` field.
  **`ListTaxCodes` (active-only) is genuinely untouched** — verified in the
  diff and pinned by a new regression assertion in
  `catalog_taxcode_list_test.go`, which matters because
  `catalog_lookups.html`'s read-only autocomplete depends on it.
- `internal/db/migrations/057_tax_code_management_permission.sql`: additive,
  `INSERT OR IGNORE`, granted to manager/admin/super_admin only. 057 is the
  next free number; nothing existing was edited (append-only respected).
- `web/ui/pages/tax_codes.html`, `web/ui/partials/tax_codes_table.html`,
  a Catalog link, `taxcodes.*` keys in en/ar/fa/tr, a new
  `web/help/{en,ar,fa,tr}/tax-codes.md` topic with a `?` link, and generated
  screenshots.

## Findings

Four real issues, all fixed on this branch. Nothing blocker-class: no money
mishandling, no data-loss path, no auth bypass in the shipped code.

### 1. False "✓" on a rejected save, and the form wiped with it — MEDIUM, fixed

`web/ui/pages/tax_codes.html`

htmx's `htmx.ajax()` promise **resolves on a 4xx too**. In the vendored
`htmx.min.js`, `xhr.onload` runs `M(n, I)` (`handleAjaxResponse`, which
dispatches `htmx:responseError` synchronously) and only *then* calls the
promise's resolve — so the page's `.then()` success branch runs as a
microtask **after** the error handler and silently overwrote it.

Net effect: every localised error this card introduced
(`taxcodes.err.duplicate_name`, `.invalid_rate`, `.not_found`) flashed for one
tick and was then replaced by `✓`. Worse, on a **create** the same branch also
called `clearForm()` — so a manager who typed a duplicate tax-code name had
their input wiped and was told the save had succeeded, with nothing persisted.

The whole localised-error path the card built was, in practice, invisible.

Fixed with a `failed` flag set by the `htmx:responseError` listener and
checked in the `.then()`. Also reordered `clearForm()` before
`msg.textContent = '✓'`, since `clearForm()` blanks `msg` and so a successful
create was erasing its own confirmation.

Verified by reading the vendored htmx control flow (`var o`/`var s` are the
promise's resolve/reject; `b.onload` ends `ie(o); w()`), plus a Node
demonstration of the microtask ordering. **Not** verified in a live browser —
this sandbox has no Playwright-pinned Chromium available for an interactive
run, only the docs-shots harness.

*The identical shape pre-exists in `web/ui/pages/catalog.html:249-254`, which
this page was cloned from. Deliberately NOT fixed here* — it is a different
feature's bug and would balloon this diff. Flagged as a follow-up below.

### 2. The update endpoint had zero permission-gating coverage — MEDIUM, fixed

`internal/pages/tax_codes_page_test.go`

The code is correct — `POST /api/catalog/tax-codes/update` does call
`canPerform(d, r, "tax_code_management")` first, and a cashier does get a real
403, not just on the GET page. Confirmed by reading the path, not by assuming
symmetry with the create handler.

But **nothing tested it.** `TestTaxCodesPage_RealSessionGatesByRole` covers
only the read-only GET page; the write endpoints had only a no-session 403
check (create) or nothing at all (update).

Proven, not asserted: deleting the `canPerform` call from the update handler
**entirely** left all eleven tax-code tests in `internal/pages` green. The one
endpoint that can rewrite a tax rate and retire a code was completely
unprotected against regression.

Added `TestTaxCodesAPI_RealSessionGatesByRole`, covering create **and** update
across cashier/manager/admin/super_admin, and additionally asserting that a
403 wrote nothing (a gate that answers 403 *after* mutating would still pass a
status-only assertion).

### 3. Exactly 100% was rejected by a message that said 100 was allowed — LOW, fixed

`internal/pages/tax_codes_page.go`

`parsePercentToBP` rejected `bp >= 10000`, i.e. 100%. Meanwhile the input
carries `max="100"`, the returned message is *"Enter a valid rate between 0 and
100"*, and `catimport.ParseTaxRateBP` — the import-side parser this must agree
with — accepts `f > 100` as its reject condition, i.e. 100 is fine there. So a
tax code the CSV importer creates happily could not be re-entered or re-saved
by hand, and the rejection message contradicted itself.

Changed to `> 10000` and, while there, moved **both** bounds onto the float
before the int conversion (`f > 100`), mirroring `catimport`. Go leaves an
out-of-range `float64 -> int` conversion implementation-defined (amd64 wraps to
MinInt64, arm64 saturates to MaxInt64), so the existing post-conversion range
check on input like `"1e300"` was only accidentally correct on whichever
machine it happened to be tried — same class as ut-docs#512's finding B1 on
the import-side parser. Added `"100.01"` to the rejected-input table and a new
`TestTaxCodesAPI_Create_AcceptsExactly100`.

### 4. `"name required"` shipped as a bare English literal — LOW, fixed

`internal/pages/tax_codes_page.go`, `web/locales/{en,ar,fa,tr}.json`

The other three errors on the same handler are localised, but a blank name
returned a hardcoded English `"name required"` — and the page's own JS renders
the response body **verbatim** into the form's status line, so a Turkish
operator got English. It is genuinely user-reachable: a whitespace-only name
satisfies the browser's `required` attribute and only trims to empty
server-side.

Added `taxcodes.err.name_required` to all four locales, matching the key shape
every sibling admin CRUD page already uses (`locations.error.required`,
`promotions.error.required`, `tables.error.required`,
`kitchenstations.error.required`), plus a test asserting the response body is
the localised string.

`guard-i18n.sh` does not catch this — its Go-side check deliberately excludes
`http.Error` (documented in the guard as a knowingly-deferred class). Found by
reading, not by CI.

## Checked and found correct — no change needed

- **Repository pattern.** No SQL text anywhere outside `internal/data` /
  `internal/db`. `guard-data-access.sh` green, and eyeballed independently:
  the handler holds only a `*data.CatalogRepo`.
- **Money / basis points.** Rates stay `int`/`int64` basis points end to end;
  `money.Money` correctly does **not** appear, and no float is persisted — the
  only float is a transient `strconv.ParseFloat` in the percent parser, rounded
  to an int before it can reach the DB. `taxrate.FormatPercent` is reused for
  display rather than a second formatter being invented.
- **Percent round-trip.** `FormatPercent` emits a bare `"19"` (the template
  appends the `%`), so the toggle's `hx-vals` payload re-parses cleanly. An
  explicit 0% takeaway rate (`"0"`) survives the round-trip and is not
  confused with "no override" (`""`) — checked in both the template's
  truthiness test and the handler's blank check.
- **Escaping / XSS.** Drove a tax code named
  `Ev"il' <script>alert(1)</script> &amp;` through the real handler. Both
  layers hold: `json.Marshal` escapes to `<script>`, then
  html/template's attribute escaper turns `"` into `&#34;` and `'` into
  `&#39;`, so the single-quoted `hx-vals='…'` attribute cannot be broken out
  of and no script tag reaches the body. Nothing to fix.
- **CSS regression from `.field-checks[hidden] { display: none; }`.** Read
  every other use of the class: `catalog.html:58`, `catalog.html:99`,
  `receipt_designer.html:17`. None carries a `hidden` attribute and none has
  it set from JS, so the new rule cannot bite them. It can only ever *fix* a
  `hidden`-attributed instance, since a class `display` beating the UA
  `[hidden]` style is a bug in every case. Safe.
- **UNIQUE-name conflict.** Handled cleanly at both layers, and the raw
  SQLite string genuinely cannot reach the user on this path — see the TDD
  section for the exact leak that was prevented.
- **Migration 057.** Verified by the real migration runner in
  `internal/db/tax_code_permission_test.go`, not by a hand-mirrored fixture —
  the right way round, and its doc comment is honest about why the
  handler-level test alone would not have caught a bad migration.
- **Manual.** `web/help/en/tax-codes.md` declares `routes: [/catalog/tax-codes]`,
  the page carries `{{ helpLink "tax-codes" }}`, all four locales have the
  topic, and the prose is accurate about there being no delete and about a new
  code always being active. `guard-help-topics.sh` green.
- **Screenshots.** Regenerated for real (`playwright.docs.config.ts`, 84 shots,
  all passing) after this review's edits changed the surface hash — not
  hand-patched into the manifest. Only `alerts.png` and `designer.png` changed
  bytes (both are time/preview-sensitive screens); `tax-codes.png` came back
  **byte-identical**, which independently confirms this review's changes are
  pixel-neutral. `guard-docs-shots.sh` green on the new surface hash.
- **Concurrency.** Both new files are plain `net/http` handlers plus
  `database/sql` calls — no goroutines, channels, or shared mutable state
  beyond the `*sql.DB` (itself concurrency-safe). Nothing for `-race` to find
  that a read cannot.

## TDD re-verification (performed personally, not taken on trust)

Every sabotage below was applied and reverted **within a single turn**, with
`git diff`/`git status` confirming a byte-identical restore afterwards.

Against the **pre-existing** code:

| Sabotage | Result |
|---|---|
| `canPerform` deleted from the **GET** handler | ✅ real fail — `GET role=cashier: got 200, want 403` |
| `canPerform` deleted from the **update** handler | ❌ **all 11 tax-code tests stayed green** — the gap in finding 2 |
| `CreateTaxCode` stops mapping the UNIQUE violation | ✅ real fail at both layers — repo: `expected ErrTaxCodeNameExists, got create tax code: constraint failed: UNIQUE constraint failed: tax_codes.name (2067)`; handler: `expected 400, got 500 body create tax code: constraint failed: UNIQUE constraint failed: tax_codes.name (2067)` |

That third row is worth keeping: it is the literal string a shop owner would
have seen in the form's status line without the mapping. The mapping is
correct and does its job.

Against **this review's own** new tests, each re-broken to confirm it is not
tautological:

| Sabotage | Result |
|---|---|
| update handler's `canPerform` removed | ✅ `POST /api/catalog/tax-codes/update role=cashier: got 200, want 403` |
| bound restored to `bp >= 10000` | ✅ `rate=100: expected 200, got 400 body Enter a valid rate between 0 and 100` — the self-contradiction, reproduced |
| localised message reverted to the literal | ✅ `body "name required", want the localised "Name is required"` |

All restored and green afterwards.

## Full gate

`gofmt -l .` clean · `go build ./...` · `go vet ./...` ·
`go test ./internal/data/... ./internal/pages/... ./internal/pages/catalog/...
./internal/db/... ./internal/catimport/... ./internal/taxrate/...` all ok ·
all 16 CI-blocking guards from `.github/workflows/ci.yml`'s `build` job green.

A full `go test ./... -race` was not attempted: it hits a known ~600s
environment timeout in this sandbox unrelated to this change. Mitigated by the
concurrency read-through above.

## Deferred / follow-ups

- **`lang-pack-drift` will go RED on `main` after this merge.** Ran
  `scripts/ci/check-lang-pack-drift.sh` against the live pack repos: all 13
  original `taxcodes.*` keys (14 with this review's `name_required`) are
  missing from **both** `ut-plugin-language-de` and `ut-plugin-language-es`.
  That workflow is advisory on a PR but **blocking on push to `main`**. This
  is the documented follow-up in `CLAUDE.md`, and it is not fixable inside
  this repo — the packs are separate repositories. **Must be landed in the two
  pack repos before or alongside this merge**, not after.
- **`catalog.html` has the same false-`✓` bug as finding 1** (`:249-254`), on
  the item create/edit form. Same fix applies. Out of scope for this card;
  worth its own small issue, since a false "saved" on the item form has the
  same shape of consequence.
- **`taxCodeFormActive` defaults to `true` when `isActive` is absent** from the
  form. Not reachable from the UI (both the edit form's hidden-fallback pair
  and the toggle's `hx-vals` always submit it), but a hand-crafted API call
  omitting the field silently *reactivates* a retired code. Accepted for now —
  it mirrors `internal/pages/catalog/handlers.go`'s `formCheckboxActive`
  convention, and changing it unilaterally here would diverge the two.
- **`http.Error(w, err.Error(), 500)` leaks raw error text** on the unmapped
  paths. 118 occurrences of this exact pattern already exist under
  `internal/` — an established repo-wide convention, not something this card
  introduced. The two error classes a user can actually provoke (duplicate
  name, bad rate) are both mapped to localised 400s, so the practical exposure
  here is nil. Accepted, not fixed.
- **The page `<title>` is a hardcoded English `"Tax codes"`.** Consistent with
  ~30 other pages in `internal/pages` doing exactly the same; a repo-wide
  convention to change deliberately, not in this diff.
- **After a successful *edit*, the form stays in edit mode showing the
  pre-rename title.** Cosmetic; same as `catalog.html`. Left alone.

## Verdict

**Safe to merge** — conditional on the language-pack follow-up above being
landed so `main` does not go red on `lang-pack-drift`.

No blocker-class finding. The feature's core correctness (basis-point
handling, no hard-delete, active-only lookup preserved, permission gating,
migration seeding, escaping) holds up under independent scrutiny; the four
fixed findings were all in the layer around it — error feedback that never
reached the user, a write endpoint with no gating test, a self-contradicting
bound, and one unlocalised string.
