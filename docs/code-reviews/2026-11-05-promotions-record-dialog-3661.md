# Review: /promotions on the list-and-dialog pattern (ut-docs#3661)

**Scope:** `git diff origin/main...` on `fix/3661-promotions-record-dialog`
(WIP snapshot 6ce792f plus this review's fixes). The till's /promotions page
put a whole edit form in every row, so at 1024×600 the table scrolled
sideways. It now uses the shared list-and-dialog pattern
(ut-docs `reference/list-and-dialog-pattern.md`), copying `locations.html` /
`locations_page.go`.

## What shipped
- `web/ui/pages/promotions.html`: rewritten onto `list_header` +
  `.list-card/.list-scroll` + `record_dialog`. There are 8 columns: Starts and
  Ends merge into one "Valid" column (start above end, an en dash for an open
  side). Rows carry `data-record-*` / `data-field-*` for the client-side
  prefill. The row holds only the pencil (aria-label + title). Every field
  sits in a labelled control in the dialog. A page script shows only the value
  input that matches the type and disables the other. On edit it makes the
  code and the customer read-only, and the customer stays visible. Deactivate
  (trash icon, `hx-confirm`) and Activate live in the dialog's destructive
  slot and are switched by `data-record-when="active=…"`. The Alpine
  `x-data` markup is gone.
- `internal/pages/promotions_page.go`: `redirectPromotions` /
  `renderPromotionsDialogError`, copied from locations. An htmx request gets
  `HX-Redirect` + 200 on success, or a 400 `record_dialog_msg` fragment on a
  refusal. A non-htmx caller gets the same 303 / `?err=` as before. The
  manager gate (`requireManager`, 403), validation and audit are unchanged.
- `web/public/app.css`: `promo-wrap` / `promo-dates` rules, plus a shared
  `.record-dialog-body input[readonly]` look. Logical properties only.
- 5 new `promotions.*` keys in `en.json`, each with `en.meta.json` metadata
  (English only, ADR-0160).
- The help topic `web/help/en/promotions.md` was rewritten for the dialog
  flow, and `make docs-shots` regenerated the screenshots (en/ar/fa/tr).
- Tests: Go tests cover the slot contract, the Valid column, htmx refusals
  and HX-Redirect + audit. The money and percent prefill tests moved to
  `data-field-*`. There is a new e2e spec
  `promotions-record-dialog-3661.spec.ts`, and 5 existing specs were adapted.

## Review
I am an independent reviewer (Opus 5.5). The code was written by a Sonnet Dev.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | **High** (regression) | The promo code is free text and is also the URL path segment. The old inline `<form action="/api/promotions/{{.Code}}/edit">` was URL context to html/template, which percent-encoded the code. `data-record-action` / `data-record-destructive-action` are *not* URL context: html/template treats a `data-*` name as URL only if it contains src/uri/url. So the code was HTML-escaped only, and record-dialog.js copies it verbatim into `form.action`. A code such as `10%OFF` reached the server as an invalid escape and got a 400, so the code could no longer be edited or deactivated. `#`, `?` and `/` (already broken before this change) cut the path short. | **Fixed.** `promotionView.PathCode = url.PathEscape(code)` is used in both row attributes. New test `TestPromotionsPageList_RowActionsPathEscapeTheCode` covers `%`, `#`, `?`, `/` and space: it checks the rendered attrs, then edits and deactivates through the escaped path and checks the DB. New e2e test "a code with % and # in it saves and deactivates from the dialog". Both fail with the fix reverted (the e2e times out because the save is refused in the dialog). |
| R2 | Medium (test gap, from Tester) | The e2e spec read "the first customer, if any". The e2e till has no customers, so every `if (cust)` branch was skipped: UUID wrapping, read-only customer prefill, and create with a picked customer. A mutation that removed the read-only prefill passed all 14 tests. | **Fixed.** New test-support tool `e2e/promo_customer` (idempotent; writes through `data.CustomerImportRepo`; generic name) puts one customer in this worker's till via `UT_E2E_WORKER_DATA_DIR`, the same pattern as `e2e/till_setting` and `e2e/category_picture`. The conditionals are gone. The spec now asserts the read-only `#promo-cust-q`, the hidden `customer_id` prefill, `data-field-customer_id` after save, a real search → pick → save, and that New after a customer-bound edit starts empty. Re-verified: with `q.value = hid.value…` removed, the prefill test fails (`Expected <uuid>, Received ""`). The file skips (same as the other data-dir specs) only on a reused local server. |
| R3 | Low (test gap, from Tester) | `TestPromotionsPageList_ValidColumnMergesDates` checked for an en dash anywhere on the page. | **Fixed.** The test now extracts VALID1's own `td.promo-dates` cell and compares it exactly. Re-verified: with the `–` fallback removed it fails (`Valid cell = "…<span class=\"promo-date\"></span>"`). |
| R4 | Info | No CSRF token on the dialog forms. | **Accepted.** That matches every list-and-dialog page (locations, categories). The till has no form-token scheme. |
| R5 | Info | XSS / escaping: every `data-field-*` value is HTML-attribute-escaped by html/template, and record-dialog.js sets `.value`, never markup. The customer search still escapes quotes (`esc`). | No issue. |
| R6 | Low (pre-existing, shared) | `td.list-actions { display:flex }` makes a jagged bottom border on multi-line rows. The record dialog's exit/lock buttons are 33×32 (under 44px). Both come from the shared pattern/partial (Tester obs 3–4). | **Deferred** to a shared-pattern card. Out of this card's scope. |

**TDD re-verified by me** (revert → run → fail → restore → pass):
`redirectPromotions` ignoring HX → `HtmxSuccessSendsHXRedirect` +
`RefusalRendersInDialog…` fail (`create: code=303`).
`renderPromotionsDialogError` always redirecting → `RefusalRendersInDialog…`
fails (`bad value: code=200`). R1, R2 and R3 are covered as above.

## Verified
- `go build ./...`. `go vet` and `gofmt -l` are clean.
  `golangci-lint run ./internal/pages/... ./e2e/...`: 0 issues.
- `go test ./internal/pages/ -run 'Promotion|MoneyPrefill|MoneyPattern'`: ok.
  Full `go test ./internal/pages/ ./e2e/...`: ok (`internal/pages` 433s).
- Guards: all 61 guards named in `ci.yml` were run. 59 pass, including i18n,
  i18n-meta, rtl-logical-css, no-showmodal, help-topics, fixed-dates,
  data-access, e2e-fixtures-import, core-neutral and kiosk-engine. 2 fail on
  this environment only: `guard-deadcode-baseline` (a toolchain mismatch in
  the deadcode binary) and `guard-shellcheck-version` (no shellcheck
  installed). No `.sh` was touched.
- e2e (Chromium, default project): `promotions-record-dialog-3661` 15/15 with
  0 skipped. `phone-promotions-3653`, `percent-comma-2954`,
  `money-comma-2925` and `osk-decimal-admin-fields-1275`: 24/24.

## Verdict
**Safe to merge** with R1–R3 fixed.

## Deferred (Backlog cards)
- Shared pattern: the `list-actions` flex cell's border jag on multi-line
  rows, and the record dialog status-row buttons under 44px (Tester obs 3–4).
- Show the customer's name instead of the UUID, and allow change/clear of the
  customer on edit (Architect follow-up).
- Delete the now-unused keys `promotions.edit`, `promotions.new.submit` and
  `promotions.list_empty`, and their baseline lines.
- Docs-shots harness: a per-topic dialog screenshot (UX A7, the inactive-row
  dialog).
- kitchen-stations has the same inline-form defect. Afterwards, drop the
  #3653 `.users-inline` phone CSS.
- ut-docs side: tick promotions on #2012, and fix the "every locale" wording
  in `list-and-dialog-pattern.md`.
- Not verifiable here: real-touch scroll of `.list-scroll` at 481–1023px on
  the Pi, the native date picker under the kiosk compositor, WebKit, dark
  theme.

Pre-flight: till user-visible behaviour → `web/help/en/promotions.md`
updated + `make docs-shots` regenerated (en/ar/fa/tr) · `reference/` page
n/a: `list-and-dialog-pattern.md` needs no change for an adopter; its wording
fix is deferred (ut-docs, above) · README n/a (it does not describe
/promotions) · new `en.json` keys → `en.meta.json` updated (5 keys, English
only) · plan/price, platform claim, ADR, plugin type/host function, my. UI
n/a · review record: this file; follow-ups listed above as cards.
