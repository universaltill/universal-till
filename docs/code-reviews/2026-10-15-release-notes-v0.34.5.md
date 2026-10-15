# Review — release notes v0.34.5

- **Date:** 2026-10-15
- **Scope:** `web/release-notes/{en,de,tr,ar,fa}/v0.34.5.md`, the owner-facing "What's new" that `release.yml` requires.
- **Written by:** Opus 5.5 (pipeline lane `lane:cloud-24`). **Reviewed by:** Sonnet, independently in a fresh context.

## What it covers
These are the first-parent merges since v0.34.4. Each was read from its PR and its card's close-out.
- **New:** new shops start with a gift voucher item (ut-docs#4259).
- **Improved:**
  - CODE128 on-screen barcode for any voucher code (ut-docs#4248).
  - More of the printed Z-report follows the shop's language (ut-docs#4330).
- **Fixed:**
  - The gift voucher item is kept while vouchers are open (ut-docs#4259).
  - A manager on a self-order till keeps status, Lock and exit, and sees the update notice (ut-docs#4324, ut-docs#4394).
  - The Journal receipt header wraps on a phone (ut-docs#4279).
  - Back office problem messages wrap on a phone (ut-docs#4226).
  - Added after the first draft, for merges that landed later: printed CODE128 barcodes on receipts, vouchers and labels fit 80mm paper (ut-docs#4403); additional tills re-prove and reconnect to a main till restored with a new certificate (ut-docs#4410).
- **Left out on purpose:** these are groundwork or internal (RELEASING.md step 1, ut-docs#4219).
  - Groundwork: ut-docs#4147 (until an AI plugin declares ai.agent, #4142), ut-docs#4337 and ut-docs#4392 (the guest-order page #4338 has not shipped), ut-docs#4273 (until #4391).
  - Internal: ut-docs#359 (docs-shots data), ut-docs#256 (comment and test), ut-docs#4311 (CI), ut-docs#4385 (internal; its owner-visible part was in v0.34.4).

## Findings
Fixed:
- **(medium) all five:** "named in your shop's language" overclaimed. The seed falls back to English when the language pack is not installed yet. Each language now says so in a short aside.

Accepted:
- **(low)** "A barcode for any voucher code": a 36-character code is scaled down and may not scan on a low-resolution screen. The claim holds for the card numbers the change targets.
- **(low)** my. delete protection is enforced till-side (`delete_item` refusal), so crediting my. is accurate. my. needed no change.

## Verified
- Every claim was checked against the merged diffs.
- Every bold label matches `web/locales/{en,ar,fa,tr}.json`. German was checked against a fresh clone of `ut-plugin-language-de`.
- The section headings match v0.34.3 and v0.34.4 in each language.
- `scripts/ci/guard-release-notes.sh v0.34.5` passes. So do the compliance-claims and competitor-naming guards, and `go test ./internal/updates/ ./internal/pages/ -run 'Notes|ReleaseNotes|3091'`.
- The notes contain no PR or card numbers.
- Two bullets added after review for merges that landed later (ut-docs#4403 printed CODE128 fit, ut-docs#4410 LAN re-proof); author-checked against the merge diffs, no new UI labels.
