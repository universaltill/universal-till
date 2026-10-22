# Review: the language packs keep plan names untranslated too (ut-docs#4742)

**Date:** 2026-10-21 · **Lane:** lane:cloud-41 · **Built by:** Opus 5.5 (inline, complexity:easy) · **Reviewed by:** Sonnet 5.5 (independent subagent)

## What shipped
ADR-0060 amendment 2026-10-17 point 2 says plan names are product names.
Core's `web/locales/plan_names_test.go` (ut-docs#4738) enforces the rule
for core's own locales. The external packs had no such check: de, es and pt
said "Chain" for weeks, and `check-key-drift.sh` asks for translated values,
so it could not see the problem.

- `scripts/ci/check-lang-pack-drift.sh`: new `check_plan_names` runs per
  pack after the pack's own `check-key-drift.sh`. Every single-segment
  `subscription.plan.<id>` key in both core en.json and the pack must equal
  core's value. The exception is `subscription.plan.local`, which must
  start with "Local". A future `local_plus` is covered with no edit. A
  violation fails that pack and names it in the follow-up-PR block. Keys a
  pack lacks stay the pack-side script's missing-key drift.
- `scripts/ci/check-lang-pack-drift.test.sh`: a `plans` fixture mode and
  four cases. One pack keeping the names passes. The fail cases are an old
  paid name ("Chain"), a translated free name, and a translated `local_plus`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | `plan_names_test.go` is not on this branch's base, so the rule can't be compared | Accepted. It lands with universal-till#2089 (ut-docs#4738), which merges first. The rule matches that test, and also covers future keys. |
| 2 | low | Fail cases didn't assert *why* they failed | **Fixed.** `expect_msg` makes them require "plan name translated (ADR-0060)". |
| 3 | cosmetic | `re.match` with `$` also matches before a trailing newline | **Fixed.** Now `re.fullmatch`. |
| 4 | info | `local_plus` is fixture-only | Intentional. It proves future keys are covered. |

## Verified
- TDD: with the script change reverted, the three new fail cases go red
  ("expected non-zero exit, got 0"). With it restored, all 11 cases pass.
  The reviewer re-ran this independently.
- The live `check-lang-pack-drift.sh` against the real de/es/pt packs
  passes, so `main` will not go red.
- `shellcheck` is clean on both files, and `guard-ci-tests-sandboxed.sh`
  passes.

**Verdict:** safe to merge.
