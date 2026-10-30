# Review: phone cards keep a flagged row's status in view (ut-docs#4583)

Date: 2026-10-30 · Branch: `fix/4583-card-status-shown` · Lane: `lane:cloud-41`
Author: Opus 5.5 · Reviewer: Sonnet 5.5 (independent subagent, fresh context)

## What shipped
- `web/public/table-cards.js`: a cell marked `data-card-keep` never gets
  `.t-cards-extra`, so at ≤480px it never waits behind **More**. Generic and
  per cell; the page decides which rows need it.
- `internal/pages/import_page.go`: the status cell of a warned, skipped or
  failed import row (Preview and commit summary) carries `data-card-keep`.
  A clean "ok" status stays behind More. Before this change, a collapsed
  warned card differed from a clean one only by its tint (colour as the only
  signal). A skipped row's fix controls (include box, corrected name/price)
  were also hidden behind More; they are now on the card.
- Tests: `internal/pages/import_card_status_4583_test.go` (new);
  `e2e/tests/catalog-import-preview-warnings-4520.spec.ts` no longer taps
  More in its phone run, and asserts a warned card has no More while a clean
  card's status is hidden behind a visible More.
- Manual: `web/help/{en,de,ar,fa,tr}/menu.md` phone-cards paragraph gains one
  sentence. Reference: ut-docs `reference/list-and-dialog-pattern.md`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | No review record in the WIP commit | Fixed (this file) |
| 2 | nit | Header comment said `td[data-card-keep]`, code accepts any cell | Fixed: comment now says `[data-card-keep]` |
| 3 | nit | A clean row's editable generated-SKU input still sits behind More on a phone | Accepted: existing behaviour, not a warning signal |
| 4 | nit | Go test has no failed-row (commit summary) case | Accepted: same `Skipped \|\| Failed` branch as the skipped case |

The reviewer confirmed that re-annotation after htmx swaps is idempotent, that a
warned row gets no More holder and no stale `.t-cards-open`, that no other
table emits the attribute, and that the Go test and e2e assertions are not
false passes.

## Verified beyond unit tests
- TDD: the Go test failed before the `import_page.go` change and passes after.
  With the old `table-cards.js`, the phone e2e run fails at "⚠ visible"; with
  the fix it passes. Both directions were run.
- `catalog-import-preview-warnings-4520` and `phone-table-cards-3359` specs: 8/8 pass.
- Driven Playwright run at 360×800 in ar (RTL) and de, with screenshots:
  - the warned card shows "Status: ⚠ reason" without a tap;
  - the skipped card shows its status and its "Import anyway" controls;
  - the clean card keeps More;
  - there is no sideways overflow.
- Full gate: gofmt, build, vet, golangci-lint (0 issues), `go test ./...`,
  and every guard in `ci.yml`'s build job.

## Verdict
Safe to merge.
