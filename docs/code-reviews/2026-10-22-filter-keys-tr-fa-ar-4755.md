# Code review — filter keys translated in tr/fa/ar (ut-docs#4755)

Date: 2026-10-22 · Branch: `fix/4755-filter-keys-tr-fa-ar` · Lane: `lane:cloud-41`

## What shipped

`filter.category_all` was translated in #4702, but three sibling keys
still showed English in the Turkish, Persian and Arabic locales. This
change translates them in `web/locales/{tr,fa,ar}.json`. Nine values
changed and nothing else. No key is new, so no language-pack follow-up is
needed. All three keys are in the shrink-only meta baseline, so
`en.meta.json` is unchanged.

| key | tr | fa | ar |
|---|---|---|---|
| `filter.category_label` | Kategoriye göre filtrele | فیلتر بر اساس دسته | التصفية حسب الفئة |
| `filter.category_active` | Kategoriye göre filtrele (filtre uygulandı) | فیلتر بر اساس دسته (فیلتر فعال) | التصفية حسب الفئة (مفعّلة) |
| `filter.no_matches` | Filtrenizle eşleşen ürün yok | کالایی با فیلتر شما مطابقت ندارد | لا توجد أصناف تطابق التصفية |

Each `category_label` matches the wording of the "filter by category"
bullet in that locale's `web/help/<lang>/catalog.md`. The word for
"items" follows `kitchenstations.items.no_results`.

## Review

Sonnet wrote the strings. Opus 5.5 reviewed them in a fresh context.

- **No blockers and no majors.** The reviewer checked:
  - script by codepoint: Persian uses ی/ک, Arabic uses ي;
  - that brackets in RTL text display correctly;
  - that the JSON has no duplicate keys and only these nine values changed;
  - that the wording is consistent with each locale's existing terms.
- **nit (fa), fixed:** `category_active` was "(فیلتر شده)". Screen readers
  announce this label through aria-live, so the announcement said
  "فیلتر … فیلتر". It is now "(فیلتر فعال)", the reviewer's option B.
- No TDD claim to re-verify: this is a data-only change, guarded by
  `guard-i18n.sh`.

## Verified beyond automated tests

- Driven run: I booted a throwaway till (`UT_AUTH=off`, temp data dir) and
  fetched `/catalog` and `/inventory` with `?lang=tr|fa|ar|en`. The
  rendered HTML has the new strings for the trigger's `data-label-active`
  and for the `catalog-no-matches` and `stock-no-matches` rows. `fa` and
  `ar` render `dir="rtl"`. `en` is unchanged.
- Visual: **no screenshots were taken.** `category_active` appears only on
  an icon-only button (`aria-label`, `title` and the aria-live
  announcement), so it does not change the layout. The visible strings are
  the dialog `<h3>` and the empty-state rows. They are short, and they sit
  in containers that already hold longer de/fa text.

## Gate

- Passed: `guard-i18n.sh`, `guard-i18n-meta.sh`,
  `guard-compliance-claims.sh`, `guard-competitor-naming.sh`,
  `guard-mail-addresses.sh`, `guard-help-drift.sh`, `guard-help-topics.sh`.
- Passed: `go build ./...` and `go vet ./...`.
- `go test ./...` passed, with two exceptions:
  - `internal/pages` failed once with "open tr.json: no such file". My
    rebase rewrote the locale files while that run was reading them. It
    passed when re-run alone on the rebased tree (265s).
  - `internal/plugins` hit the 600s per-package timeout locally. CI runs it
    in its own `test-heavy` job, and that CI result is the verdict for it.

## Verdict

Safe to merge. Nothing is deferred.
