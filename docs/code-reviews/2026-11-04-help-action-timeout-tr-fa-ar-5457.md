# Review: sell help — 30 s action-timeout text in tr/fa/ar — ut-docs#5457

Date: 2026-11-04 · Branch: `fix/5457-sell-help-timeout-tr-fa-ar`

## What shipped

Follow-up to ut-docs#5404 (commit a472fa9). That commit added the 30 s
action-timeout sentences to `web/help/{en,de}/sell.md`, in the 2nd bullet of
"Selling with no network connection". This change adds the same text to
`web/help/{tr,fa,ar}/sell.md`, in the same place: straight after "never has to
be dismissed by hand" and before the **Online** light sentence. It also updates
`web/help/img/manifest.json` `topics.sell.{fa,ar,tr}` to the new markdown
hashes. No other manifest entries change and no screenshots change.

Scope decisions, made before this review:
- The card says ar is missing an offline bullet. That is false on main, so
  none was added.
- #5458's tile-outline and basket-refresh sentences have not merged yet. They
  are out of scope and tracked on ut-docs#5477.

## Process

Docs-only (prose plus manifest hashes). Dev: Sonnet subagent. Tester: driven
run (below). Review: independent Opus subagent, a different model from Dev.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R-1 | nit | ar wrote the duration with Arabic-Indic digits ("٣٠ ثانية"). The rest of `ar/sell.md` uses Latin digits in prose ("حتى 8 ساعات", "بعد 200 صنف", "10.7%"), and so does ar help in general (about 550 Latin digit runs against 40 Arabic-Indic, the Arabic-Indic ones in 9 files). Consistency within the file wins. | fixed: now "خلال 30 ثانية". `topics.sell.ar` recomputed with sha256sum (151a2309…) |

fa keeps Persian digits ("۳۰ ثانیه"): `fa/sell.md` uses Persian digits in all
its other prose numbers (۲۰۰, ۸ ساعت, ۱۹٪…), so it is already consistent.

Translation read, clause by clause against en (a472fa9), in tr, fa and ar:
- If the server doesn't answer within 30 s, a tap that only showed information
  gets the same banner. tr "aynı banner", fa "همان بنر", ar "الشريط نفسه". All
  three use the banner word from the sentence just before.
- A tap that changes something (adding an item, saving a setting) gets a banner
  saying it may or may not have been done, and to check before trying again.
  Each locale uses the wording of its own `common.error.outcome_unknown`:
  - tr "yapılmış olabilir ya da olmayabilir / Tekrar denemeden önce kontrol edin"
  - fa "ممکن است … انجام شده باشد یا نشده باشد / پیش از تلاش دوباره بررسی کنید"
  - ar "قد يكون الإجراء قد تم وقد لا يكون / تحقق قبل أن تحاول مرة أخرى"
- That message stays until it is closed with ✕. This is present in all three.
- Taking a payment, a refund, backup or restore, import, and joining a main
  till are never cut off this way and wait for the till's answer. All five
  items are present in each locale. The import and main-till terms match the
  rest of each locale's help: tr "içe aktarma" and "ana kasa", fa "درون‌ریزی"
  and "صندوق اصلی", ar "استيراد" and "صندوق رئيسي".

No clause is missing or added in any of the three locales.

Other checks:
- `scripts/ci/help-terms.txt` has no tr, fa or ar entries.
- No compliance claims and no competitor names.

## Extra verification

- Reviewer re-ran every guard after the R-1 fix, and each exited 0:
  - `guard-docs-shots.sh` (32 topics × 4 locales fresh)
  - `guard-help-drift.sh`
  - `guard-help-topics.sh`
  - `guard-help-terms.sh`
  - `guard-help-breadcrumbs.sh`
  - `guard-compliance-claims.sh`
  - `guard-competitor-naming.sh`
  - `guard-mail-addresses.sh`

  `go test -count=1 ./internal/manual/...` passed.
- Manifest: compared with origin/main, only `topics.sell.{fa,ar,tr}` changed,
  and each value equals `sha256sum web/help/<loc>/sell.md`:
  - fa 4dec357b…
  - ar 151a2309… (after R-1)
  - tr 08a7f7d3…
- Negative manifest check (Tester): the Tester put the old tr hash (705ffd95…)
  back, and `guard-docs-shots.sh` failed with rc=1 and "topic markdown changed
  since its screenshot was taken: tr/sell". After the file was restored, the
  guard passed again.
- Driven run (Tester): the Tester built the binary and ran it with
  `UT_AUTH=off`, then loaded `/help/sell?lang={tr,fa,ar}` in Playwright/chromium
  at 1280×900.
  - Exactly one `<li>` per locale holds the new text, under the localized
    offline heading.
  - tr renders dir=ltr. fa and ar render dir=rtl, and the li's computed
    direction is rtl.
  - No `**`, `](` or `#` artefacts appear.
- Tester's visual attestation (quoted): "I looked at the sell help page's
  offline section in tr (LTR) and fa/ar (RTL), light theme, 1280×900 desktop.
  The new sentence sits in the right bullet, straight after 'never has to be
  dismissed by hand'. The text wraps cleanly with nothing clipped or
  overlapping, and bold terms render as bold." Not looked at: dark theme, the
  kiosk viewport, en/de (unchanged). The driven run came before R-1, which only
  changes two digits in ar and does not affect layout.

Pre-flight: Till user-visible behaviour: web/help topic updated (tr/fa/ar; en/de already done in a472fa9); docs-shots n/a (prose only, no screenshot change; manifest hashes refreshed); reference/ n/a; README n/a; en.json keys n/a (none changed); lang packs n/a. Any change: this review record, plus follow-up ut-docs#5477.

## Verdict

Approve (after R-1 fix).

## Deferred

- ut-docs#5477: carry #5458's tile-outline and basket-refresh sentences into
  tr/fa/ar once #5458 merges.
