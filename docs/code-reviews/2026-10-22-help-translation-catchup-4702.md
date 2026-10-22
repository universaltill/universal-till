# Code review — help/UI translation catch-up (ut-docs#4702)

- **Date:** 2026-10-22
- **Card:** universaltill/ut-docs#4702 (`complexity:easy`, lane:cloud-54)
- **Author model:** Sonnet 5.5. **Reviewer:** Opus 5.5, a fresh independent subagent.
- **Scope:** `web/help/{tr,fa,ar}/display.md`, `web/help/{de,tr,fa,ar}/catalog.md`,
  `web/locales/{tr,fa,ar}.json`, `web/help/img/manifest.json`.

## What shipped

- display item 11 (tr/fa/ar): the Android screen-pinning paragraph and the Raspberry Pi cold-boot sentences.
- catalog "Filter by category" bullet translated in de/tr/fa/ar.
- `settings.shop_type.service_layout_note` and `filter.category_all` translated in tr/fa/ar.
- Manifest hashes refreshed with `make docs-shots`; unrelated `multitill.png` noise reverted.
- Terms from each locale's `settings.display.mode_*`, `page.title.*` and the Android `values-*/strings.xml`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The layout note named the hidden-tiles page differently from its title in tr/fa/ar | Fixed: uses `page.title.hidden_menu_tiles` |
| 2 | minor | tr red-bar text "güvende" vs app "güvenli"; ar diacritic | Fixed: matches `kiosk_pin_not_engaged` |
| 3 | nit | fa cold-boot lost "can take up to" | Fixed: "ممکن است تا یک دقیقه" |
| 4 | nit | ar "صندوق" lacked quotes | Fixed |
| 5 | minor | fa/ar keep English "Got it" for Android's own prompt, which the OS localises | Accepted: Android's wording varies by device, and the bold label stays findable |
| 6 | info | `filter.category_label`, `filter.category_active`, `filter.no_matches` still English in tr/fa/ar | Follow-up card |

## Verification

`guard-docs-shots`, `guard-i18n`, `guard-help-topics`, `guard-help-drift`, `guard-i18n-meta`, compliance and
competitor-naming guards green; `go build ./...` and the locale/help tests green; `internal/pages` tests ok.
