# Review — tr/fa/ar users help: first-admin item translated, duplicate removed (ut-docs#4697)

Date: 2026-10-20 · Lane: lane:cloud-41 · Built by: Sonnet · Reviewed by: Opus 5.5 (fresh context)

## What shipped

In `web/help/{tr,fa,ar}/users.md`, the "How to use it" list had no translation of
English item 11 (the setup wizard signs you in as the till's first admin; it does
not connect the till to YOUR online account; see Store registration & claiming).
Its slot was filled by a near-copy of item 14 ("Locked out? Return to desktop"),
so `guard-help-drift.sh` saw a matching item count and passed.

- New item 11 in each locale, translated from English. Its link text is the
  locale's `claim.md` title, and its back-office phrase (My stores, fleet) is the
  wording from that locale's `claim.md` summary.
- The old item 11 (manager role needed) is now item 12. The duplicate is deleted.
- `web/help/img/manifest.json`: the `users` tr/fa/ar topic hashes are refreshed
  (sha256 of the markdown). This is a prose-only change, so no screenshot changed.

de was checked and was already correct.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | tr item 11 said "ilk yöneticisi". In tr help, *yönetici* is the manager role (item 12: "yönetici veya admin rolü"), so the text made you the first manager. | fixed → "ilk admini" |
| 2 | minor | ar item 11 dropped the YOUR emphasis. ar `claim.md` uses "بحسابك أنت". | fixed |
| 3 | minor | fa item 11 "حساب شما" had no emphasis. fa `claim.md` uses "حسابِ شما". | fixed |
| 4 | nit | ar item 11 "يسجّل لك المعالج الدخول" was colloquial and dropped "setup". Item 4 uses "معالج الإعداد". | fixed |
| 5 | info | Out of scope: tr/fa/ar `plugins.md` and `order-status.md` are each one item short of English, the same shape as this bug (baselined in `help-drift-baseline.json`). Some de/tr/fa/ar items lag English with an equal count: `display.md` item 8 has no Salon sentence, `catalog.md` item 2 has no /help/printing link. | follow-up card |

The reviewer checked items 1–14 one-to-one against English in all three locales.
It also confirmed that the diff touches only the expected lines and three manifest
values.

## Verified beyond automated tests

I built the till and served it from a scratch data dir seeded the way
`e2e/run-till.sh` does (`UT_AUTH=off`). `GET /help/users?lang=` returned 200
for tr, fa, ar and en.
- Each page rendered 14 items with no duplicate.
- Each item 11 links to `/help/claim`.
- fa and ar render with `dir="rtl"`.

Gate: `guard-help-drift`, `guard-help-topics`, `guard-docs-shots`, `guard-i18n`,
`guard-rtl-logical-css`, `guard-compliance-claims`, `guard-competitor-naming` and
`guard-mail-addresses` are green, and `go test ./internal/manual/...` is ok.
There is no Go change, so the Go build and lint gate is not affected.

**Verdict:** safe to merge.
