# Review — invoice page header at phone width (ut-docs#4393)

**Branch:** `fix/4393-invoice-header-phone` · **Lane:** `lane:cloud-54` · **Card:** complexity:easy
**Author:** Opus 5.5 build routine · **Reviewer:** Fable (independent subagent, different model)

## What shipped
- `web/ui/pages/invoice.html`: `.page-head` gets `page-head-wrap` and its action
  row (Print / ← Receipt) `flex-wrap:wrap`, the same page-scoped fix as
  `journal_detail.html` (ut-docs#4279) and `catalog.html` (ut-docs#2092).
- `web/public/app.css`: the `.page-head-wrap` comment no longer hand-lists
  its users (the list was already stale: `inventory.html` was missing).
- `e2e/tests/invoice-phone-4393.spec.ts`: at 360×740, in en/fa/tr, it issues an
  invoice and checks that the action row sits below the title, that
  `scrollWidth <= 360` and that each header button is inside the viewport. It
  clears the seller identity in `afterEach`.
- `web/help/img/manifest.json`: surface hash only. Before rebasing, a full `make docs-shots` run changed no PNG. After rebasing onto the #1954 demo-sales seeding, this container rendered `invoices.png` and `reports.png` with a fallback font (main's PNGs use Inter). That is environment churn, not this change, so the hash was refreshed with `scripts/ci/update-docs-shots-surface-hash.sh` and main's PNGs were kept. No manual screenshot shows `/invoice/{no}` at phone width.

## Measured finding (differs from the card)
Before the fix, the page did **not** scroll sideways at 360px in en, fa or tr.
The defect was a squeezed title: the h1 was ~120px wide and "INV-000001" broke
mid-token. The spec asserts that symptom.

## TDD
The author verified it inline. With the template fix stashed, the spec fails
with `en action row sits below the title`. With the fix restored, it passes.
The reviewer re-ran the spec on the fix: 1 passed.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The template comment said the page scrolled sideways. It was copied from #4279 and contradicts the measurement. | Fixed |
| 2 | nit | The `app.css` usage list was stale (`inventory.html` was missing). | Fixed: the comment now points at `grep` instead of a list |
| 3 | nit | The "row below title" assertion depends on the content being wider than the phone. | Accepted. It fails on the unfixed page and the 1px tolerance is well under the 16px gap. Every alternative (h1 width, line count) is just as content-dependent. |

State leak check (reviewer): posting an empty seller returns 204 and turns
invoicing back off. Each default-project worker has its own till. Leaving one
sale and one invoice behind matches the #4279 spec; no `/journal` spec counts
them.

## Verified beyond automated tests
I looked at screenshots at 360px in en, fa (RTL) and tr. The title stays on one
line and the buttons wrap underneath, with no clipping. At 1024px nothing
changes, because `flex-wrap` only wraps when the content overflows.

## Gates
- `go build ./...` passed.
- `go test` passed for `internal/pages`, `internal/httpx` and `web`.
- Every `scripts/ci` guard in `ci.yml` passed except two. Those two failed only
  because of the container: the deadcode tool here is built with go1.26 and the
  repo needs go1.27, and shellcheck is not installed. This diff has no Go or
  shell changes, so neither is affected.

## Deferred
- ut-docs#4402: `/invoices` (the invoice register) has the same header shape.

**Verdict:** safe to merge.
