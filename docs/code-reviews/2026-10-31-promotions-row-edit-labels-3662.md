# Review: promotions row-edit accessible names (ut-docs#3662)

- Scope: `web/ui/pages/promotions.html`, `internal/pages/promotions_page_test.go`.
- Part 1 of the card (select touch target below 44px) was already fixed on
  `main` by ut-docs#3882 (46px floor for every select); no change needed.
- Part 2: every control of the promotions row-edit form now has an
  `aria-label` from existing `promotions.*` keys; the two date inputs also get
  a `title` so sighted users see Starts / Ends. No new keys.
- Author: Sonnet (lane:cloud-54). Reviewer: Opus, fresh context.
- Findings: none blocking. Low: the test only checked that `aria-label=`
  existed (a swapped Starts/Ends would pass) — fixed, it now asserts the exact
  label and the date `title`. Nit: form lookup inside the loop — hoisted.
- Verified: the new test fails on the old template (all six controls) and
  passes now; `TestPromotions*` green; guard-i18n, guard-i18n-meta,
  guard-rtl-logical-css, guard-compliance-claims green; gofmt clean.
