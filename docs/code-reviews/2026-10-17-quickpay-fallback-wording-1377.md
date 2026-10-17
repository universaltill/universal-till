# Review — manual: quick-pay fallback wording (ut-docs#1377)

- **Date:** 2026-10-17 · **Lane:** lane:cloud-41 · **Author:** Opus 5.5 · **Reviewer:** Sonnet 5.5 (Fable was out of credits)
- **Change:** `web/help/{en,de,ar,fa,tr}/sell.md`. When no preferred payment method is set, the one-tap Pay-panel button was described as "cash". It is the first active non-voucher payment method. The sentence now reads "the first active payment method in your list — Cash on a new till", translated into each locale's existing vocabulary (Kasse/Bargeld, kasa/Nakit, صندوق/النقد, صندوق/نقدی).
- **Behaviour checked:**
  - `internal/pages/index_page.go`: `ListActivePaymentMethods` orders by `sort_order, id`. `payments.default_method` moves the chosen method to the front, voucher types are dropped from the grid, and `defaultMethod = gridMethods[0]`.
  - `001_init.sql` seeds cash as `sort_order` 1, so a new till still shows Cash.
- **Review findings:**
  - Approve.
  - Optional nit: no Settings screen edits `sort_order`, so "your list" is loose. The visible list is the Preferred-method dropdown, which uses the same order. Kept as is.
- **Gates:**
  - `make docs-shots` was re-run: the topic hashes changed and no screenshot pixels changed.
  - The help, i18n, compliance and naming guards and the build-job guard set are green locally.
  - `guard-deadcode-baseline` and `guard-shellcheck-version` fail locally on the container's toolchain (go1.26 deadcode, no shellcheck binary), not on this diff.
- No code change. The ut-plugin-language-* packs carry no help copies.
