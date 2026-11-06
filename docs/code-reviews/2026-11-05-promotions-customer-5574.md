# Review: promotions show the customer's name; change or clear it on edit (ut-docs#5574)

**Scope:** `git diff origin/main...` on `feat/5574-promotions-customer-name`
(f439621 + b68a203, plus this review's working-tree additions). Follow-up
deferred from the ut-docs#3661 review: the /promotions list showed the bound
customer's UUID, and the customer was fixed once a code was created.

## What shipped
- `internal/data/pos_repo.go`: `ListPromotionsForAdmin` uses one
  `LEFT JOIN customers` (no N+1) and returns `CustomerName` plus
  `CustomerMissing` (bound, but the row is gone or is a retired shell with
  `name = ''`). `CreatePromotion` and `UpdatePromotion` now run in one tx
  that validates a new customer id against a live, named customer by exact
  id (`liveCustomerExists`). An unknown id returns the new
  `ErrPromotionCustomerUnknown`, and nothing is written. `UpdatePromotion`
  reads the stored binding first (unknown code → `ErrPromotionNotFound`).
  When the binding is unchanged or `KeepCustomer` is set, it leaves
  `customer_id` **out of the SET list**. SQLite FK-checks a child column
  named in SET even when the value is identical, so a legacy dangling id would
  otherwise make every edit fail.
- `internal/pages/promotions_page.go`: an absent `customer_id` means
  unchanged (`KeepCustomer`), an empty one is an explicit clear, and an id is
  validated in the repo. `ErrPromotionCustomerUnknown` maps to
  `promotions.error.customer_unknown` on create and edit (400 fragment for
  htmx, `?err=` otherwise), so the raw FK text never reaches the operator.
  `promotionView.CustomerLabel` holds the name, "Deleted customer" or "".
  The manager gate (`requireManager`) and the single `promotion_*` audit row
  are unchanged.
- `web/ui/pages/promotions.html`: the list cell shows the name in
  `<bdi dir="auto">` (or "Deleted customer" / "Any customer"), never the id.
  The cell carries `data-card-keep`, so the phone card shows it without
  More. The dialog's customer field is editable in edit mode too, prefilled
  from `data-promo-customer-label`. A × button (outside the label, hidden
  rather than invisible when there is nothing to clear) removes the
  customer. Text typed with no result picked refuses the save on the client
  (`htmx:beforeRequest` plus a capture-phase `submit` fallback): it sets
  `aria-invalid`, shows the page-local `T.unpicked` message and scrolls it
  into view. Focus returns to the field after a pick or a clear.
- `web/public/app.css`: `.promo-cust-row` (logical properties only), a
  danger outline for `[aria-invalid="true"]` inside a record dialog, and
  `scroll-margin-block-start` on `.record-dialog-msg`.
- 4 new `promotions.*` keys in `en.json` with `en.meta.json` metadata
  (`review: standard`; English only, ADR-0160).
- `web/help/en/promotions.md` steps 1 and 3 updated. The docs-shots manifest
  was regenerated.
- Tests: `internal/data/promotions_customer_5574_test.go` (list name/missing
  for none/live/shell/dangling, the dangling-unchanged edit, KeepCustomer,
  rebind/clear, unknown and shell refused with the row unchanged, create
  refused, not-found, erase → "any"). There are 9 new `internal/pages` tests
  (escaped name and no visible id, deleted label, change/clear audited once,
  omitted keeps, unknown refused on create and edit, shell binding survives
  an edit, cashier 403). `e2e/promo_customer` gains `-name`, `-shell` and
  `-promo`. The e2e spec gains change+clear, unpicked-refused,
  refusal-visible at 1024×600/360×740, deleted-survives-edit and an RTL
  `dir=auto` assert.

## Review
I am an independent reviewer (Opus 5.5). The code was written by a Sonnet Dev.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | Medium (CI blocker) | `run-docs-shots-guard.sh` fails on HEAD. b68a203 (the Tester D1–D3 fixes) changed `promotions.html` and `app.css` after `make docs-shots` ran, so the manifest's `surface_sha256` was stale. CI's `build` job would go red. | **Fixed**: see the "Docs-shots" line under Verified. |
| R2 | Low (test gap) | D3 (`data-card-keep` on the Customer cell) had no test. No e2e asserted that the collapsed 360px card shows the customer. | **Fixed.** New e2e test "phone card shows the customer name collapsed at 360x740": the row is `.t-cards-row` and not open, the Customer `td[data-card-keep]` is visible, not `.t-cards-extra`, and has no id. |
| R3 | Info | `window.promoCustSync` couples the page's two IIFEs through a global. | **Accepted.** It is page-local, guarded with `typeof`, and the page has no other consumer. |
| R4 | Info | `ErrPromotionNotFound` on edit still renders the generic `promotions.error.update` (400), not a 404. | **Accepted.** That matches the design (architect §2) and the pre-change behaviour. The sentinel is pinned by a data test. |
| R5 | Info | The architect asked the × click to dispatch a synthetic `change` for the discard guard. The Dev did not. | **No issue.** `record-dialog.js` compares `serialize(form)` against the open-time snapshot, and the hidden `customer_id` is in the form, so a clear is seen as dirty without any event. |

Checked and found sound:
- **SQL.** The LEFT JOIN keeps unbound and dangling rows. Validation reads
  through the tx (`_txlock=immediate`, so check-then-write cannot race a
  picker). An `EraseCustomer` that runs at the same time is still caught by
  the FK, and `EraseCustomer` already NULLs `promotions.customer_id`.
  `RowsAffected` was replaced by the in-tx SELECT. `liveCustomerExists`
  matches the exact id only, not loyalty_no or phone.
- **Handler.** `r.PostForm` is used, so a query-string `customer_id`
  cannot change the binding. The browser always posts the hidden field, so
  absent applies only to API clients.
- **Security.** The name is escaped by html/template in the cell and in the
  `data-promo-customer-label` attribute (a test pins `<b>&` escaping). JS
  sets `q.value`, never markup, and the result buttons keep the quote-safe
  `esc()`. The visible text has no id. The gate is unchanged, and a cashier
  POST is refused with 403 (test).
- **CLAUDE.md.** Repo pattern: all SQL is in `internal/data`; raw SQL only
  in `_test.go` and the `e2e/` tool. i18n: `{{ T }}` and a page-local
  `var T`. RTL: logical properties, and the × sits on the left in fa
  (Tester). No `showModal`. Help topic updated. No money or migration
  change.

**TDD re-verified by me** in a throwaway `git worktree` (revert → run → fail →
restore → pass):
- Forcing `customer_id` into SET (`if keep {` → `if false && keep {`):
  `TestUpdatePromotion_KeepsDanglingCustomerOnUnchangedEdit` fails with
  `update promotion: constraint failed: FOREIGN KEY constraint failed (787)`.
  This confirms the SQLite "column in SET is FK-checked even when unchanged"
  claim. Restored → ok.
- Dropping `|| in.CustomerID == stored`: the same test fails with
  `unchanged dangling edit: promotion customer not found`. Restored → ok.
- The Tester's 12-row mutation table (M1–M12, all killed) covers the rest.

## Verified
- `gofmt -l .`: no output (excluding `.claude/` worktrees). `go build ./...`:
  ok. `golangci-lint run ./...`: 0 issues.
- `go test ./internal/data/... ./internal/pages/...` (non-race): all ok
  (`internal/data` 68s, `internal/pages` 474s). `-race` was not re-run: the
  Tester's whole-package race run hit the 10-minute package timeout on this
  4-core box. The Tester's targeted race run of
  `-run 'Promotion|EraseCustomer'` passed.
- Guards (web-related plus the always-on ones from the `build` job), all
  PASS: data-access, i18n, i18n-meta, rtl-logical-css, no-showmodal,
  help-topics, help-drift, help-breadcrumbs, help-terms, no-inline-handlers,
  compliance-claims, competitor-naming, kiosk-engine, core-neutral,
  fixed-dates, mail-addresses, page-http-error, osk-loaded, htmx-loaded,
  autofill-suppression, card-data-schema, e2e-fixtures-import,
  e2e-no-browser, netaccess. `run-docs-shots-guard.sh` failed before R1
  and passes after it.
- Docs-shots: `make docs-shots` gave 128 passed. Only `manifest.json`'s
  `surface_sha256` changed, and no PNG differed: the docs seed has no
  customer-bound promotion and no dialog shot.
- e2e: `npx playwright test --project=default
  tests/promotions-record-dialog-3661.spec.ts`: **21/21 passed** (Chromium,
  0 skipped). The R2 test was confirmed red with `data-card-keep` removed
  from the Customer `<td>` (`toBeVisible … Received: hidden`), then green
  after the restore.
- D3 was verified by driving a real browser at 360×740 against the e2e
  harness till (the R2 test), not by reasoning from `table-cards.js` alone.

## Tester's visual attestation (summary)
**No real touch hardware.** "Touch" was emulated in Chromium (`hasTouch` with
`.tap()`, OSK forced on). The Tester drove two real tills built from the
branch (windowed :8191 and `UT_KIOSK=1` :8192, auth off, seeded demo data
plus en/fa customers and a retired shell). Every UX §7 gate row passed
except D1–D3, which the Dev fixed in b68a203 and the e2e now covers:
- Prefill: name, hidden id and `dir=auto` at 1024×600 (kiosk and windowed),
  1280×800, 360×740 and 360×740 fa. Focus on open lands on Type, not the
  customer field.
- The × box is 51×51 (53.6 at 1280). It never overlaps the input and sits
  on the left in fa.
- With the OSK on: no keyboard on open, a × tap opens none, and a field tap
  opens exactly one. Save stays above the OSK.
- Pick+Save, ×+Save, and editing a deleted-customer row all persist
  correctly. A forged id gets a visible 400 `customer_unknown`. Status,
  lock and exit stay reachable, with 0 `showModal` calls. The Persian name
  renders correctly. No JS errors.
- D1: the unpicked message was off-screen; it now scrolls into view, and the
  field gets a danger outline. D2: focus was lost after a pick; it now
  returns to the field. D3: the Customer cell was hidden behind More on the
  phone card; it now carries `data-card-keep` (R2 test).
- Not looked at: dark theme, WebKit, the ar locale, and the de copy length
  (the de pack is not installed and the keys are English-only).

## Verdict
**Safe to merge** with R1–R2 fixed.

## Deferred (Backlog cards, per architect §7)
- Before/after diff payload on `promotion_*` audit rows (ids only) — ut-docs#5588.
- Align the picker's `/api/data/customers` `data_management` gate with the
  page's `settings` gate — ut-docs#5589.
- Bind a promotion to several customers or a customer group; editable promo
  code — not filed: no request for either, speculative.
- Data repair: report or clean legacy dangling `promotions.customer_id` rows
  from the period when the FK pragma was ignored — ut-docs#5590.
- Real-touch check of the × and the picker on the Pi kiosk; WebKit; dark
  theme — ut-docs#5595.

Pre-flight: till user-visible behaviour → `web/help/en/promotions.md`
updated (steps 1, 3) + docs-shots manifest refreshed (R1) · `reference/` n/a
(`list-and-dialog-pattern.md` is unaffected; no reference page describes
promotion customer binding) · README n/a (it does not describe /promotions)
· new `en.json` keys → `en.meta.json` updated (4 keys, English only) ·
plan/price, platform claim, ADR (none needed, architect), plugin type/host
function, my. UI n/a · review record: this file; follow-ups listed above as
cards.
