# Code review: release notes v0.30.11, and the fa label for "Generate missing SKUs"

- **Date:** 2026-09-30
- **Branch:** `docs/release-notes-v0.30.11`
- **Scope:** the owner-facing release note for v0.30.11 in en/de/ar/fa/tr, which covers ut-docs#3097. It also fixes the fa wording for the #3097 labels in `web/locales/fa.json` and `web/help/fa/catalog.md`.
- **Reviewer:** Sonnet, an independent pass by a different model from the Opus author.
- **Verdict:** safe to merge after the fixes below.

## Findings

| # | Finding | Outcome |
|---|---|---|
| 1 | "Every catalog item now has a SKU", followed by "if any are still missing", contradicts itself in all 5 languages. | Fixed: it now reads "catalog items without a SKU now get one", in every language. |
| 2 | ar says "الصندوق الرئيسي" for the main till, while the v0.30.10 note says "الجهاز الرئيسي". | Kept. `ar.json` itself uses "الصندوق الرئيسي" 9 times against 4, so the note matches the UI. |
| 3 | fa "ناموجود" reads as "out of stock", not "missing". The same word was also in the shipped fa UI keys and help. | Fixed everywhere, as "جاافتاده": the note, `catalog.sku_backfill.btn`/`.heading` in `fa.json`, and the fa catalog help bullet. |
| 4 | The de wording "vorab zu sehen" was stilted. | Fixed: "eine Vorschau … zu sehen". |

## Checks

- `guard-release-notes.sh v0.30.11`, `go test ./internal/releasenotes/ ./internal/pages/catalog/`, and the `guard-i18n` and `guard-help-drift` guards pass.
- The docs-shots surface hash was refreshed: the fa label change touches no manual screenshot, because the button only shows while items lack a SKU and the seed has none.
