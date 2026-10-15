# Review: release notes v0.34.6

Date: 2026-10-15 · Branch `docs/release-notes-v0.34.6`
Written by the `:54` build lane; reviewed independently by a Sonnet subagent.

## What shipped
Owner-facing "What's new" notes for v0.34.6 in en/de/tr/ar/fa. There have
been 6 feat/fix merges since v0.34.5, so a release is due.
- Improved:
  - Import retires the setup-seeded gift voucher item.
  - The main till refuses plain-HTTP sync from a till that already used TLS.
- Fixed:
  - Invoice list and single-invoice headers wrap on a phone.
  - Record dialogs keep the status bar and Lock keyboard-reachable.
- Left out on purpose: the docs-shots font pin, which is internal tooling only.

## Findings (all fixed)
- Minor, all locales: the invoice bullet claimed the invoice number was no
  longer squeezed. Only the single-invoice page has one, so the claim was
  narrowed to the title.
- Minor, fa: "ورود اطلاعات" did not match the till's term for import,
  which is "درون‌ریزی". The heading also said the second item is "never
  created" when it is actually removed. Both are reworded.
- Nit, tr: the heading was reworded to "…ikinci bir hediye çeki ürünü
  kalmaz".
- Nit, ar: changed to "الصندوق الأساسي" for the main till, matching
  earlier notes.

## Verified
- Each bullet was checked against the merged diffs.
- Lock label and seeded item name per locale were checked against
  `web/locales/*.json` (de against the v0.34.5 note).
- `guard-release-notes.sh v0.34.6` passes.
- `go test ./internal/updates/` passes.
- The compliance-claims and competitor-naming guards pass.

## Verdict
Safe to merge.
