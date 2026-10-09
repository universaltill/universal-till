# Review: release notes v0.31.5

Date: 2026-10-09. Written by Opus 5.5 (lane:local); reviewed by Sonnet against every first-parent merge since v0.31.4.

## What shipped

Owner-facing notes in `web/release-notes/{en,de,ar,fa,tr}/v0.31.5.md`. The release was due after five feat/fix merges since v0.31.4:
- camera-identify pick learning (#4006, with its #4005 host functions, which are developer-only);
- plugin content slots in Settings and Administration (#3946);
- the subscription chip in the status bar (#3459);
- permissions looked up once per request (#3935).

Left out as internal or developer-only: #3178, #3505 and #3406.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | High | The status-bar bullet overclaimed "stays one row". The bar still wraps when it is genuinely crowded; what shipped is a narrower chip that ellipsizes and no longer adds height. | **Fixed** in all five languages. The CSS was checked (`max-inline-size` 14rem, `::before` hit area). |
| 2 | Medium | The Administration half of #3946 was missing. | **Fixed:** a sentence added in all five. |
| 3 | Medium | "Once per button" was imprecise; it is per page load instead of per menu entry and button. | **Fixed** in all five (tr too). |
| 4 | Low | "A single section" was wrong; it is one section per plugin entry. | **Fixed:** "their own section". |
| 5 | Low | "Never slowed down" was too absolute. | **Fixed:** "does not hold up selling". |
| 6 | Low | de "Abo-Bezeichnung" did not match the help's "Abonnement". | **Fixed.** |

Terminology was checked against `web/locales/*.json` and `web/help/*`: Settings, Plugins, Administration (Verwaltung/Yönetim/الإدارة/مدیریت), status bar and till.

## Verification

`scripts/ci/guard-release-notes.sh v0.31.5` ok; `go test ./internal/releasenotes` ok.

## Verdict

Safe to merge, then release v0.31.5.
