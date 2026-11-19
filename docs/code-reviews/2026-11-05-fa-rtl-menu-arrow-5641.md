# Review: Persian menu path uses the RTL arrow (ut-docs#5641)

Date: 2026-11-05 · Branch: `fix/5641-fa-rtl-menu-arrow` · Lane: cloud-41
Built by a Sonnet subagent. Reviewed independently by an Opus subagent, which also did the Tester checks.

## What shipped

- `web/locales/fa.json`: in `setup.auto_register.hint`, the menu path
  "تنظیمات → ثبت صندوق" becomes "تنظیمات ← ثبت صندوق". Nothing else in the
  string or the file changes. The other 30 menu paths in fa.json already used
  "←". ar.json had no "→" at all.
- `web/locales/rtl_path_arrow_test.go` (new, external package
  `locales_test`): for every embedded `*.json` whose locale is right-to-left
  per `httpx.IsRTL`, the test fails on any value containing "→" and names the
  file and key. It fails when no RTL locale is found, so it can't pass
  vacuously. A helper test covers both cases: an rtl "→" fails, while "←" and
  ltr "→" pass. `httpx.IsRTL` is the one place the RTL set is defined
  (ar, fa, he, ur, ps, ckb, dv, yi), so fa and ar are both covered. The
  external test package keeps `web/locales` free of an `httpx` import.
  It mirrors ut-cloud's `internal/i18n/locales_rtl_path_arrow_test.go`
  (ut-cloud#853).

### Weblate

The card asked for the fix "through Weblate". Core tr/ar/fa are still
built-in files in this repo. Moving them to Weblate is ADR-0160 §9 step 4
(ut-docs#4372, blocked:dep), so no Weblate component owns fa.json yet, and
ADR-0160 §9 keeps today's rules in force until then. Recent code PRs edit
fa.json directly (58d46c1, ba54300). A direct one-character edit is the
right path today.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | Low (out of scope) | `web/help/fa/sell.md:23` has the menu path "Journal → تاریخچه فروش". The arrow points the wrong way, and "Journal" is left in English. The AC covers till locale JSON only, and the new test scans only `web/locales/*.json`, so help topics are not covered. `guard-help-breadcrumbs.sh` checks the label of `Settings →` breadcrumbs, not the arrow direction. | Deferred → Backlog card (help-topic RTL arrow + a guard over `web/help/{ar,fa}`). Not widened here. |
| R2 | Nit | The test rejects every "→" in an rtl string, not only menu paths. A future rtl string that genuinely needs "→" will fail it. | Accepted. The comment in the test says such a string should be reworded, which matches ut-cloud's guard. Neither file has such a string today. |
| R3 | Nit | `rtlSeen == 0` stops a vacuous pass but not the loss of one of the two RTL files (for example, if ar.json were removed). | Accepted. Removing a locale file is a deliberate change, and the guard still checks whatever RTL files exist. |

Plugin locales: no `plugins/*/locales/{fa,ar}.json` exist (only `layout-salon`
and `tax-tr`). The language packs that `check-lang-pack-drift.sh` checks are
de/es/pt, so none of them is RTL.

## Verification beyond unit tests

- **TDD re-verified by the reviewer, inline:** with only the fa.json change
  stashed, `go test ./web/locales/` failed with
  `fa.json: setup.auto_register.hint uses "→" in a right-to-left locale; use "←"`.
  After restoring the change it passed with `-count=1`. `git diff` matched the
  pre-check diff byte for byte.
- **Go:** `gofmt -l .` printed nothing. `go build ./...` and
  `go vet ./web/locales/` pass. `go test -count=1 ./web/locales/ ./internal/httpx/`
  passes (there is no `internal/i18n` package in this repo).
  `golangci-lint run ./...` reports 0 issues.
- **Guards:** i18n, i18n-meta, fixed-dates, rtl-logical-css,
  compliance-claims, competitor-naming, mail-addresses, help-topics,
  help-breadcrumbs and core-neutral all pass.
- **Screenshots:** this is a locale-only text change. Any setup-wizard fa
  screenshot that shows the hint is picked up by the weekly
  `docs-shots-locale-drift.yml` (#4582), so no `make docs-shots` run is
  needed in this branch.

Pre-flight: gofmt clean; go build OK; go vet ./web/locales OK; go test web/locales + internal/httpx OK; golangci-lint 0 issues; guards i18n / i18n-meta / fixed-dates / rtl-logical-css / compliance-claims / competitor-naming / mail-addresses / help-topics / help-breadcrumbs / core-neutral all pass. Till user-visible behaviour: one fa string. `web/help/` n/a (no topic describes this hint; the separate fa help arrow is R1, deferred); `make docs-shots` n/a (locale-only change, covered by docs-shots-locale-drift); `reference/` n/a; README n/a. en.json keys: n/a (none added or changed, so no meta, baseline or pack follow-ups). ADR: n/a (ADR-0160 §9 followed as written). Review record: this file. Follow-ups: R1 → Backlog card.

## Deferred

- R1: the "→" in the fa help topic `sell.md`, plus a guard for RTL help
  topics.

## Verdict

Approve. The fix changes only the arrow. The guard fails on a regression
(re-verified) and can't pass vacuously. Every gate run is green.
