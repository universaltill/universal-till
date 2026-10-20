# Code review — help translations lag English (ut-docs#4698)

- **Date:** 2026-10-20
- **Card:** universaltill/ut-docs#4698 (`complexity:easy`, lane:cloud-41)
- **Author model:** Opus 5.5. **Reviewer:** Fable 5.1, a fresh independent subagent.
- **Scope:** `web/help/{de,tr,fa,ar}/{order-status,display,catalog}.md`,
  `web/help/img/manifest.json`, `scripts/ci/i18n-baseline/help-drift-baseline.json`.

## What shipped

- **order-status (tr/fa/ar):** English item 6 is translated. It says that Collected or Cancel removes the
  order from the list and that nothing is deleted from the Journal. Also added from English: item 1's
  "this is the *active* queue", item 7's "Collected and Cancelled are the end of the line", and the
  kitchen ⚠ note's **Resend kitchen ticket** step. The Resend step was missing in de too.
  In tr and fa, item 1 was reworded so it no longer opens with a bold label, which matches English's
  bold lead-in count. The three `order-status` baseline entries are removed. All 8 items now match English.
- **display item 8 (de/tr/fa/ar):** the Service trade → Salon menu layout sentences and the
  `/help/menu` link.
- **catalog item 2 (de/tr/fa/ar):** the `/help/printing` sentence about what the Print labels tab needs.
- **Terms:** taken from `web/locales/{tr,fa,ar}.json`, the de language pack,
  `plugins/layout-salon/locales/*.json`, and each locale's own `menu.md`/`printing.md` titles.
- The manifest hashes for the 9 changed fa/ar/tr files are refreshed. de is not a docs-shots locale.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | tr order-status item 1 says "raftaki" (shelf) for the rail | Fixed: "yan çubuktaki", as in tr/menu.md |
| 2 | nit | tr display: "kendi karolarında" reads as plural tiles | Fixed: "kendilerine ait bir karoda" |
| 3 | nit | fa order-status item 7 uses adjective forms, not the UI labels | Fixed: «تحویل شد» و «لغو شد» |
| 4 | nit | ar order-status item 6 "تعليم الطلب بـ" is terse | Fixed: "وضع علامة … على الطلب" |
| 5 | nit | ar catalog "هذه العلامة" is ambiguous with the «لن يُباع» flag | Fixed: "علامة التبويب هذه" |
| 6 | nit | fa catalog sentence is slightly ungrammatical | Fixed: "برای اینکه بدانید…" |
| 7 | nit | de "Ausgeblendete Menü-Kacheln" vs UI "Menükacheln" | Accepted: matches de/menu.md, the help's existing spelling |
| 8 | major (out of scope) | tr/fa/ar display item 11 lack the Android paragraph; tr/fa also lack the Pi cold-boot sentences | Deferred → ut-docs#4702 |
| 9 | minor (out of scope) | catalog "Filter by category" bullet is still English in all four locales | Deferred → ut-docs#4702 |
| 10 | info | catalog "Clearing out old, removed items" is missing | Already tracked in baseline (ut-docs#341) |
| 11 | info | `settings.shop_type.service_layout_note` untranslated in tr/fa/ar | Deferred → ut-docs#4702 |
| 12 | info | plugins.md tr/fa/ar one step short | Tracked on ut-docs#960 |

## Verified

- `guard-help-drift.sh` passes with no order-status entry left. `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-competitor-naming.sh`, `guard-mail-addresses.sh` and `guard-rtl-logical-css.sh` all pass.
  The reviewer re-ran the help guards independently.
- `go test ./internal/manual/... ./scripts/ci/checkhelpdrift/... ./web/...` passes, as does the full `go test ./internal/...`.
- The reviewer recomputed the manifest hashes and confirmed the baseline diff is exactly 3 entries removed.
  The reviewer also rendered the new bold constructs through goldmark to check the markup.
- **Driven run:** a throwaway till was built from this branch. `/help/{order-status,display,catalog}`
  returned 200 in tr/fa/ar/de. The fa order-status list renders 8 items with `dir="rtl"`. The new
  `/help/menu` and `/help/printing` links are present. A 1024px screenshot of the fa list was read:
  no clipping and no horizontal scroll.
  Not looked at: phone width and dark theme. The change is prose inside an existing layout.

## Verdict

Safe to merge.
