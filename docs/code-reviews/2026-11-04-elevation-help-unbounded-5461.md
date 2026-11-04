# Review: elevation help — backup/restore PIN check is never cut off (tr/fa/ar) — ut-docs#5461

Date: 2026-11-04 · Lane: `lane:cloud-41` · Branch: `docs/5461-elevation-help-unbounded`

## What shipped

Follow-up from ut-docs#5454 (review finding R-1). Since #5454 a PIN prompt on a
path in `UT_UNBOUNDED_PATHS` (backup now, restore) is not timed at 30 s; the
en/de `elevation.md` step 4 already said so. This adds the same sentence to
`web/help/{tr,fa,ar}/elevation.md` step 4, after the "nothing is retried"
sentence:

- tr: "Burada onayladığınız bir yedekleme veya geri yükleme bu şekilde asla yarıda kesilmez: kasanın yanıtını bekler."
- fa: "پشتیبان‌گیری یا بازیابی‌ای که اینجا تأیید می‌کنید هرگز به این شکل قطع نمی‌شود: منتظر پاسخ صندوق می‌ماند."
- ar: "عملية النسخ الاحتياطي أو الاستعادة التي توافق عليها هنا لا تُقطع بهذه الطريقة أبدًا: فهي تنتظر جواب الصندوق."

Terms taken from each locale's UI (`settings.backup.now`, `settings.backup.restore`,
`settings.backup.title`) and the till word already used in each topic.

## Process

Docs-only, three sentences: BA/Architect/UX trivial (the card's AC is the spec).
Dev: Sonnet subagent. Review: independent Opus subagent (different model).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | ar: dual agreement (اللذان / يُقطعان / كل منهما) after «أو» reads heavier than EN/DE; singular with «عملية» is the standard form | fixed |

Reviewer also confirmed the claim against code: `web/public/app.js`
`UT_UNBOUNDED_PATHS` contains `/api/backup/`; the htmx timeout hook returns early
for unbounded paths (prompt forms too) and `utPostWithElevation` starts its timer
only for bounded paths; the prompts post to `/api/backup/now` and
`/api/backup/restore` (`internal/pages/backup_api.go`).

## Verification

`guard-help-drift.sh`, `guard-help-terms.sh`, `guard-help-topics.sh`,
`guard-help-breadcrumbs.sh`, `guard-compliance-claims.sh`,
`guard-competitor-naming.sh`, `guard-i18n.sh`: all exit 0;
`go test -count=1 ./internal/manual/...` ok. Re-ran drift/terms/topics after the ar fix: 0.

Pre-flight: web/help topic updated (tr/fa/ar; en/de already done in #5454); docs-shots n/a (text-only, no UI change); reference/ n/a; README n/a; en.json keys n/a (none changed); lang packs n/a; review record this file.

## Verdict

Approve. No deferred items.
