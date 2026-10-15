# Review — invoice register header at phone width (ut-docs#4402)

**Branch:** `fix/4402-invoices-header-phone` · **Lane:** `lane:cloud-24` · **Card:** complexity:easy
**Author:** Opus 5.5 build routine (inline — a two-line template change, below
the fan-out threshold) · **Reviewer:** Fable (independent subagent, different model)

## What shipped
- `web/ui/pages/invoices.html`: `.page-head` gets `page-head-wrap` and its action
  row (Export CSV / ← Journal) `flex-wrap:wrap` — the same page-scoped fix as
  `invoice.html` (ut-docs#4393), `journal_detail.html` (ut-docs#4279) and
  `catalog.html` (ut-docs#2092).
- `e2e/tests/invoices-phone-4402.spec.ts`: at 360×740, in en/fa/tr, with one
  issued invoice (so the populated register renders, not the empty state), it
  checks that the action row sits below the title, `scrollWidth <= 360`, and
  each header button is inside the viewport. `afterEach` clears the seller
  identity (turns invoicing back off) and resets the basket.
- `web/help/img/manifest.json`: surface hash only
  (`update-docs-shots-surface-hash.sh`, commit trailer `Docs-Shots-Unchanged: true`).
  The manual's `/invoices` shot is taken at 1024px, where the header fits on one
  line and `flex-wrap` changes nothing. A full `make docs-shots` in this
  container only produced font-rendering churn (also on unrelated `reports.png`),
  so the PNGs were left as they are.

## Measured before the fix
At 360px (en) the h1 was squeezed beside the two buttons: "🧾" on one line,
"Invoices" under it, buttons wrapped to two lines each. No sideways scroll.

## TDD
Author: with the template fix stashed the spec fails with
`en action row sits below the title — Expected >= 258.125, Received 190.09375`;
restored, it passes. Reviewer independently reproduced both (fix reverted in a
scratch copy → same failure, same numbers; with fix → 1 passed).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | nit | "Row below title" depends on the content overflowing 360px. | Accepted — it fails loudly (not a false pass) if a locale ever fits on one line; same as #4393 finding 3. |
| 2 | note | The `app.css` `.page-head-wrap` comment hand-lists its users and is stale. | Left alone on purpose: PR #4393 replaces the list with a `grep` pointer; editing it here would conflict. |
| 3 | note | Review record missing at review time. | Fixed (this file). |
| — | pre-existing | `←` in "← Journal" points the wrong way under RTL (also invoice/journal detail). | Out of scope; Backlog card ut-docs#4408. |

## Verified beyond automated tests
Screenshots looked at, 360×740: en, fa (RTL — title right-aligned, buttons
wrapped below, mirrored, nothing clipped), tr; populated register. 1024px:
unchanged (docs-shot viewport, title + buttons fit).

## Gates
- `go build ./...` passed (no Go change).
- Every `scripts/ci` guard in `ci.yml` passed except container-only failures:
  deadcode (tool built with go1.26, repo needs go1.27), shellcheck not
  installed, and `retry-with-backoff.sh` (a helper that needs arguments). No Go
  or shell change in this diff.

**Verdict:** safe to merge.
