# Review: Gift vouchers page — list, history, import, export (ut-docs#4251)

## Scope
The owner asked for a page that lists sold vouchers, tops up a balance, and
imports/exports. This branch ships the page. Top-up needs a new voucher
transaction kind (`voucher_transactions.type` is `CHECK (type IN
('issue','redemption'))`), plus sync, EOD/Z and DSFinV-K work, so it was
split into ut-docs#4254 (top-up as a sale) and ut-docs#4255 (direct
correction with a reason), both carrying the owner's decision.

## What shipped
- `GET /vouchers` (manager/admin, action `settings`; a cashier gets 403 and
  doesn't see the Menu tile). The list shows number, holder, original value,
  balance, status (active / used up / void), purpose (the single-purpose
  "specific item" tag), created date, and the issuing sale (link to
  `/journal/{receipt}`). An imported balance or a sale archived by a reset
  is labelled as such. Search (number or holder, `%`/`_` matched literally)
  and a status filter refresh the list via the `GET /ui/vouchers/list`
  fragment. Newest first, capped at 200 with a note.
- `GET /vouchers/{id}`: the voucher with its issue and redemption history,
  each linked to its receipt. An unknown id gives a translated 404.
- `GET /api/vouchers/export`: CSV `code,balance,label,original_amount,status,purpose,created_at`,
  following the current filter with no row cap. `csvSafe` is applied to code
  and label, and each export is written to the audit log.
- The import moved from `GET /settings/vouchers/import` (removed, along with
  its Settings block, template and four locale keys) onto `/vouchers`.
  `POST /api/vouchers/import` is unchanged apart from:
  - a row whose `purpose` is `single_purpose` is refused: its VAT was
    declared at issue, and importing it as a multi-purpose balance would
    tax it twice;
  - a row whose `status` is `redeemed`/`void` is reported as "nothing left
    to import" rather than "bad balance";
  - a successful commit fires `HX-Trigger: vouchers-imported`, so the list
    reloads.
- A replica till shows a non-modal note that the full list is on the main
  till.
- Menu tile "Gift vouchers" (Lucide gift icon added to the menu icon set),
  `/vouchers` reserved against plugin page routes, `httpx.RenderBlock`
  (`RenderContentFragment` now delegates to it unchanged).
- i18n: 35 new keys in en/ar/fa/tr, plus language-pack PRs for de/es/pt.
  Help: `vouchers.md` claims `/vouchers` and `/vouchers/{id}`, and
  `voucher-import.md` points at the new location, in all five help
  languages. Screenshots: `vouchers.png` added, the stale
  `voucher-import.png` removed. README updated.

## Review
Independent reviewer: Fable (code by Opus 5.5). Verdict: safe to merge, no
blockers or majors.
- Minor 1, full export re-imported floods the preview with "balance must be
  > 0": **fixed** with the `not_active` reason and help text; test
  `TestParseVoucherBalances_RefusesNotActiveRowsWithOwnReason` (seen failing
  first).
- Minor 2, a stored `'=X` value round-trips as `=X`: **accepted**. This is a
  pre-existing `stripCSVDefuse` edge, already documented there (ut-docs#321).
  The code comment no longer claims the round trip is exact.
- Minor 3, document-global `from:select` trigger: **fixed** (`input
  delay:300ms, change, submit` on the form).
- Nit, filter parsed twice: **fixed**. Nit, no BOM: matches every other CSV
  export, left as is.
- Checked: authz on all four routes plus demo mode; SQL only in
  `internal/data`, parameterised, `LIKE … ESCAPE`, no N+1; money as minor
  units with the same currency decimals on export and import; XSS
  (`url.PathEscape`, html/template); no `showModal`, no inline handlers,
  logical CSS. No file writes, so the `MkdirAll` / `paths.Data` checks don't
  apply.

## Tester (driven run, two fresh tills, auth on)
Passed:
- menu tile;
- import (preview, then confirm) incl. a `/` and space code and a formula
  label;
- selling, part-redeeming and fully redeeming vouchers through the sale
  screen;
- list, search, filter, detail and sale link;
- export, and the round trip onto a fresh till;
- cashier 403 on every route and no tile;
- OSK: one keyboard (`inputmode="none"`), no focus grab;
- 1024×600 in en/de/fa, light and dark.

Bugs found and fixed here:
1. At 360px the filter row pushed the page sideways (en/de/fa). Fixed: the
   row wraps.
2. The "specific item" tag wrapped into a squashed pill. Fixed: `nowrap`.
3. The list didn't refresh after confirming an import. Fixed: `HX-Trigger`
   event; test `TestVoucherImportPage_CommitFiresListRefreshEvent`, which
   failed with the fix stashed.

The Tester's Playwright spec `e2e/tests/vouchers-page-4251.spec.ts` (360px
in en and fa, plus refresh after import) failed on the pre-fix code for the
real reasons and passes now, alongside `phone-layout-sweep-3297` (89/89).

Follow-ups filed:
- ut-docs#4256: the journal receipt for a voucher sale has no item line and
  no voucher number. This is pre-existing, and the page now links to it.
- ut-docs#4257: keep the filter in the URL, non-ASCII case-insensitive
  search, and mid-word card-label breaks in German at 360px.

Not checked: real touch hardware (emulated), Arabic/Turkish rendering, the
replica banner in a live two-till setup (covered by a handler test), the
200-row cap in the UI (covered by a handler test), the look of the tag and
filter fixes at 1024px beyond the e2e geometry checks.

## TDD re-verification (reviewer, throwaway worktree)
- Single-purpose refusal disabled: `TestParseVoucherBalances_RefusesSinglePurposeRows`
  and `TestVouchersExport_RoundTripsThroughImport` fail.
- LIKE escaping removed: `TestListVouchers_NewestFirstSearchStatusLimit` fails.
- Export gate removed: `TestVouchersPage_ForbiddenForCashierAndAnonymous` fails.

All restored and passing.

## Gate
`gofmt`, `go build ./...`, `go vet ./internal/...` and golangci-lint
(0 issues) are clean. `go test` passes for data, catimport, pages/...,
uislot and httpx. All `ci.yml` guards pass, with these local-only gaps:
- `shellcheck` is not installed (no `.sh` changed);
- `guard-deadcode-baseline` can't load packages with the container's go1.26
  deadcode binary (`requires go1.27`). This is environmental, and CI runs it.

## Verdict
Safe to merge. The language-pack PRs land right after core (new keys).
