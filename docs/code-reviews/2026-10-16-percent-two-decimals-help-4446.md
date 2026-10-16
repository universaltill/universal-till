# Review: manual states the two-decimal limit on tax rates (ut-docs#4446)

**Date:** 2026-10-16 · **Branch:** `docs/4446-rate-decimals` · **Lane:** cloud-54
**Reviewer:** independent subagent, different model from the author (Sonnet reviewing an Opus-authored diff).

## What shipped
- `web/help/{en,de,ar,fa,tr}/tax-codes.md` and `country-settings.md`: one
  in-line clause saying a rate takes at most two decimals, with an accepted
  (`7.25` / `8.25`) and a refused (`8.875`) example. No new bullets, steps
  or headings, so `guard-help-drift.sh` structure counts are unchanged.
- `internal/httpx/percent_help_test.go`: every help locale must show both
  examples in both topics, and the examples must really match / not match
  `PercentPatternLocal`, so a pattern change or a new locale that misses the
  sentence fails CI.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | de `country-settings.md`: "mit … Nachkommastellen" repeated, clumsy | Fixed — clause moved after "sind zulässig" |
| 2 | nit | de `tax-codes.md` "geht" colloquial | Fixed — "wird akzeptiert" |
| 3 | nit | tr "olur / olmaz" colloquial | Fixed — "kabul edilir / edilmez" |
| 4 | nit | fa "آری / نه" informal, inconsistent with the other topic | Fixed — aligned on "پذیرفته می‌شود" |
| 5 | minor | Test silently skipped a locale missing a topic | Fixed — a missing topic now fails the test |
| 6 | note | Promotions, payment-fee and plugin takeaway-rate topics also omit the limit (omissions, not contradictions) | Deferred — Backlog card |

Factual check: both pages also refuse >2 decimals server-side
(`taxrate.ParsePercent` rejects `len(frac) > 2`), so "refused" holds even if
the browser pattern is bypassed.

## Verified
- TDD: with the help edits stashed the test fails (`web/help/tr/country-settings.md: missing … 8.875`); restored, it passes.
- `go vet ./internal/httpx`, `go test ./internal/httpx`, `guard-help-drift`, `guard-help-topics`, `guard-compliance-claims`, `guard-competitor-naming` pass; the other `ci.yml` build guards pass locally except `guard-deadcode-baseline` (local deadcode binary built with an older Go) and `guard-shellcheck-version` (no shellcheck in the container) — toolchain only, CI runs both.
- No UI surface and no locale key. `guard-docs-shots` keys on topic markdown, so `make docs-shots` was re-run (120 shots passed). Every PNG came out byte-identical and only the 8 topic hashes in `web/help/img/manifest.json` changed.

**Verdict:** safe to merge.
